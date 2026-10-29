// Package tracecontrol owns a native trace helper's logical lifetime. The
// session actor supplies authorization and serialization; this layer fences
// late helper work and records uncertainty without changing target execution.
package tracecontrol

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"path/filepath"
	"sync"

	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracebridge"
)

type Operation string

const (
	OpOpen   Operation = "open"
	OpStatus Operation = "status"
	OpRead   Operation = "read"
	OpStart  Operation = "start"
	OpStop   Operation = "stop"
	OpClose  Operation = "close"
)

var (
	ErrUnavailable            = errors.New("trace: native helper is unavailable")
	ErrInvalidRequest         = errors.New("trace: invalid request")
	ErrUnverifiedSource       = errors.New("trace: explicit unverified source acknowledgement is required")
	ErrNotOwner               = errors.New("trace: controller does not own this capture")
	ErrStaleHandle            = errors.New("trace: stale capture handle")
	ErrBusy                   = errors.New("trace: another operation is in progress")
	ErrStaleOperation         = errors.New("trace: operation belongs to an expired owner generation")
	ErrReconciliationRequired = errors.New("trace: native state requires operator reconciliation")
	ErrStaleGeneration        = tracebridge.ErrStaleGeneration
)

type OpenRequest struct {
	Source                      timemachine.Source
	AcknowledgeUnverifiedSource bool
}

type ReadRequest struct {
	Start      uint64
	Generation uint64
	Count      int
}

type Request struct {
	Op         Operation
	Handle     uint64
	Open       OpenRequest
	Read       ReadRequest
	Authorized bool
}

type Receipt struct {
	Operation                 string `json:"operation"`
	Outcome                   string `json:"outcome"`
	Reason                    string `json:"reason,omitempty"`
	NativeStateUncertain      bool   `json:"native_state_uncertain"`
	HelperAbortRequested      bool   `json:"helper_abort_requested"`
	NativeDisconnectConfirmed bool   `json:"native_disconnect_confirmed"`
	CollectionMayContinue     bool   `json:"collection_may_continue"`
}

// Status is safe for ordinary lifecycle diagnostics. Private source identity,
// program paths and raw packets appear only in explicitly requested batches.
type Status struct {
	Handle                 uint64  `json:"handle"`
	State                  string  `json:"state"`
	Busy                   bool    `json:"busy"`
	NativeGeneration       uint64  `json:"native_generation,string"`
	PacketCount            uint64  `json:"packet_count,string"`
	PacketCountKnown       bool    `json:"packet_count_known"`
	Timebase               string  `json:"timebase"`
	TraceState             string  `json:"trace_state"`
	SourceBinding          string  `json:"source_binding"`
	NeedsReconciliation    bool    `json:"needs_reconciliation"`
	NativeRuntimeQualified bool    `json:"native_runtime_qualified"`
	HardwareQualified      bool    `json:"hardware_qualified"`
	LastReceipt            Receipt `json:"last_receipt"`
}

type Result struct {
	Status  Status               `json:"status"`
	Batch   *timemachine.Capture `json:"batch,omitempty"`
	Receipt Receipt              `json:"receipt"`
}

type Backend interface {
	Open(context.Context, tracebridge.OpenArgs) (tracebridge.Status, error)
	Status(context.Context) (tracebridge.Status, error)
	Read(context.Context, tracebridge.ReadArgs) (tracebridge.ReadResult, error)
	StartTrace(context.Context, bool) (tracebridge.ControlResult, error)
	StopTrace(context.Context, bool) (tracebridge.ControlResult, error)
	Close(context.Context) error
	Abort()
	Poisoned() bool
	Done() <-chan struct{}
}

type Factory func(context.Context, io.Closer) (Backend, error)

type Options struct {
	Factory Factory
	// AcquireSource reserves a host-only exclusion handle. Close must be
	// idempotent and bounded; it must not issue native or target operations.
	AcquireSource func(context.Context, uint32) (io.Closer, error)
	Installation  string
	MultiVersion  string
	DLLSHA256     string
}

type Controller struct {
	mu            sync.Mutex
	options       Options
	profile       timemachine.Profile
	backend       Backend
	lease         io.Closer
	retired       Backend
	factoryActive bool
	owner         string
	source        timemachine.Source
	epoch         uint64
	nextID        uint64
	cancel        context.CancelFunc
	infoHex       string
	touched       bool
	status        Status
	closed        bool
	helpers       sync.WaitGroup
	shutdownDone  chan struct{}
}

func New(options Options) (*Controller, error) {
	profile, err := timemachine.ProfileForVersion(options.MultiVersion)
	if err != nil || !filepath.IsAbs(options.Installation) {
		return nil, ErrInvalidRequest
	}
	if digest, err := hex.DecodeString(options.DLLSHA256); err != nil || len(digest) != 32 {
		return nil, ErrInvalidRequest
	}
	return &Controller{
		options: options, profile: profile,
		status: Status{State: "idle", Timebase: "unknown", TraceState: "unknown", SourceBinding: "operator_declared"},
	}, nil
}

func (c *Controller) Enabled() bool { return c != nil && c.options.Factory != nil }

func (c *Controller) Snapshot() Status {
	if c == nil {
		return Status{State: "unavailable", Timebase: "unknown", TraceState: "unknown"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshDeathLocked()
	return c.status
}

func (c *Controller) refreshDeathLocked() {
	if c.backend == nil || c.status.Busy {
		return
	}
	select {
	case <-c.backend.Done():
		c.retired = c.backend
		c.backend = nil
		c.status.State, c.status.NeedsReconciliation = "faulted", true
		c.status.LastReceipt = Receipt{
			Operation: "observe", Outcome: "failed", Reason: "helper_exited",
			NativeStateUncertain: true, CollectionMayContinue: true,
		}
	default:
	}
}

func CloneRequest(request Request) Request {
	request.Open.Source = cloneSource(request.Open.Source)
	return request
}

func cloneSource(source timemachine.Source) timemachine.Source {
	if source.CoreID != nil {
		value := *source.CoreID
		source.CoreID = &value
	}
	if source.InitialContext != nil {
		value := *source.InitialContext
		source.InitialContext = &value
	}
	return source
}

// ValidateRequest is also used before actor admission. Runtime-specific source
// binding and the capture handle are checked again by Execute.
func ValidateRequest(request Request) error {
	if request.Op == OpOpen {
		if request.Handle != 0 || request.Read != (ReadRequest{}) || request.Authorized {
			return ErrInvalidRequest
		}
		if !request.Open.AcknowledgeUnverifiedSource {
			return ErrUnverifiedSource
		}
		if request.Open.Source.Kind != "live" && request.Open.Source.Kind != "snapshot" {
			return ErrInvalidRequest
		}
		// A declaration made at open cannot be silently reused after a buffer
		// replacement or for an arbitrary subsequent packet interval.
		if request.Open.Source.InitialContext != nil {
			return ErrInvalidRequest
		}
		profile, _ := timemachine.ProfileForVersion("8.1.4")
		return validateSource(profile, request.Open.Source)
	}
	if request.Handle == 0 || request.Handle > math.MaxInt32 || request.Open != (OpenRequest{}) {
		return ErrInvalidRequest
	}
	switch request.Op {
	case OpStatus, OpClose:
		if request.Read != (ReadRequest{}) || request.Authorized {
			return ErrInvalidRequest
		}
	case OpRead:
		if request.Authorized || request.Read.Generation == 0 ||
			request.Read.Count < 1 || request.Read.Count > 4096 ||
			request.Read.Start > math.MaxUint64-uint64(request.Read.Count) {
			return ErrInvalidRequest
		}
	case OpStart, OpStop:
		if request.Read != (ReadRequest{}) || !request.Authorized {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func validateSource(profile timemachine.Profile, source timemachine.Source) error {
	if source.SessionID == 0 || source.SessionID > math.MaxInt32 {
		return ErrInvalidRequest
	}
	capture := timemachine.Capture{
		Schema: timemachine.CaptureSchema, MultiVersion: profile.Version, ABI: profile.ABI,
		Source: source, InfoHex: hex.EncodeToString(make([]byte, profile.ABI.InfoSize)),
		Generations: []timemachine.Generation{{ID: 1, Reason: "initial", PacketsHex: []string{}}},
	}
	data, err := json.Marshal(capture)
	if err != nil {
		return ErrInvalidRequest
	}
	if _, err := timemachine.DecodeCapture(bytes.NewReader(data)); err != nil {
		return ErrInvalidRequest
	}
	return nil
}

type operationResult struct {
	status  *tracebridge.Status
	control *tracebridge.ControlResult
	batch   *timemachine.Capture
}

// Execute never holds the state lock while waiting on helper I/O. Invalidate
// can therefore fence the lifetime and abort a child while a call is blocked.
func (c *Controller) Execute(ctx context.Context, owner string, request Request) (Result, error) {
	if !c.Enabled() {
		return Result{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := ValidateRequest(request); err != nil {
		return Result{}, err
	}
	if owner == "" {
		return Result{}, ErrNotOwner
	}
	request = CloneRequest(request)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Result{}, ErrUnavailable
	}
	c.refreshDeathLocked()
	if c.status.NeedsReconciliation {
		result := Result{Status: c.status, Receipt: c.status.LastReceipt}
		c.mu.Unlock()
		return result, ErrReconciliationRequired
	}
	if c.status.Busy {
		c.mu.Unlock()
		return Result{}, ErrBusy
	}
	if request.Op == OpOpen {
		if c.backend != nil || c.owner != "" || c.nextID == math.MaxInt32 {
			c.mu.Unlock()
			return Result{}, ErrBusy
		}
		c.nextID++
		c.owner, c.source = owner, cloneSource(request.Open.Source)
		c.status = Status{
			Handle: c.nextID, State: "opening", Timebase: "unknown",
			TraceState: "unknown", SourceBinding: "operator_declared",
		}
		c.infoHex, c.touched = "", false
		c.helpers.Add(1)
	} else {
		if c.owner != owner {
			c.mu.Unlock()
			return Result{}, ErrNotOwner
		}
		if c.backend == nil || request.Handle != c.status.Handle || c.status.State != "open" {
			c.mu.Unlock()
			return Result{}, ErrStaleHandle
		}
	}
	c.epoch++
	epoch := c.epoch
	workCtx, cancel := context.WithCancel(ctx)
	c.cancel, c.status.Busy = cancel, true
	backend := c.backend
	source := cloneSource(c.source)
	previousTouched := c.touched
	if request.Op == OpStart || request.Op == OpStop {
		c.touched = true
	}
	c.mu.Unlock()
	defer cancel()
	if request.Op == OpOpen {
		defer func() {
			if backend == nil {
				c.helpers.Done()
				return
			}
			go func() {
				<-backend.Done()
				c.helpers.Done()
			}()
		}()
	}

	if request.Op == OpOpen {
		if c.options.AcquireSource != nil {
			lease, err := c.options.AcquireSource(workCtx, source.SessionID)
			if err != nil || lease == nil {
				if lease != nil {
					_ = lease.Close()
				}
				return c.factoryFailed(epoch, "source_lease_unavailable")
			}
			c.mu.Lock()
			if c.epoch != epoch || c.owner != owner {
				c.mu.Unlock()
				_ = lease.Close()
				return Result{}, ErrStaleOperation
			}
			c.lease = lease
			c.mu.Unlock()
		}
		c.mu.Lock()
		if c.epoch != epoch || c.owner != owner {
			c.mu.Unlock()
			return Result{}, ErrStaleOperation
		}
		c.factoryActive = true
		lease := c.lease
		c.mu.Unlock()
		var err error
		backend, err = c.options.Factory(workCtx, lease)
		c.mu.Lock()
		c.factoryActive = false
		stale := c.epoch != epoch || c.owner != owner
		if stale {
			c.lease = nil
		}
		c.mu.Unlock()
		if stale {
			if backend != nil {
				backend.Abort()
			}
			releaseAfterReap(lease, backend)
			return Result{}, ErrStaleOperation
		}
		if err != nil || backend == nil {
			if backend != nil {
				backend.Abort()
			}
			return c.factoryFailed(epoch, "helper_start_failed")
		}
		c.mu.Lock()
		if c.epoch != epoch || c.owner != owner {
			lease, c.lease = c.lease, nil
			c.mu.Unlock()
			backend.Abort()
			releaseAfterReap(lease, backend)
			return Result{}, ErrStaleOperation
		}
		c.backend = backend
		c.mu.Unlock()
	}
	outcome, err := c.perform(workCtx, backend, request, source)
	c.mu.Lock()
	if c.epoch != epoch || c.owner != owner {
		c.mu.Unlock()
		return Result{}, ErrStaleOperation
	}
	c.cancel, c.status.Busy = nil, false
	if outcome.status != nil {
		if observedErr := c.observe(*outcome.status); observedErr != nil {
			err = observedErr
		}
	}
	if err != nil {
		if errors.Is(err, tracebridge.ErrStaleGeneration) {
			c.status.LastReceipt = Receipt{
				Operation: string(request.Op), Outcome: "rejected", Reason: "source_generation_changed",
				CollectionMayContinue: true,
			}
			result := Result{Status: c.status, Receipt: c.status.LastReceipt}
			c.mu.Unlock()
			return result, tracebridge.ErrStaleGeneration
		}
		// The transport only leaves cancellation unpoisoned when no request
		// was dispatched. All uncertain dispatched outcomes are sticky.
		if workCtx.Err() != nil && !backend.Poisoned() {
			c.touched = previousTouched
			if request.Op == OpOpen {
				c.backend, c.owner, c.status.State = nil, "", "idle"
				if c.lease != nil {
					lease := c.lease
					c.lease = nil
					releaseAfterReap(lease, backend)
				}
			}
			c.status.LastReceipt = Receipt{Operation: string(request.Op), Outcome: "cancelled_before_dispatch"}
			result := Result{Status: c.status, Receipt: c.status.LastReceipt}
			c.mu.Unlock()
			if request.Op == OpOpen {
				backend.Abort()
			}
			return result, workCtx.Err()
		}
		c.status.State, c.status.NeedsReconciliation = "faulted", true
		c.status.LastReceipt = Receipt{
			Operation: string(request.Op), Outcome: "failed", Reason: "helper_failure",
			NativeStateUncertain: true, HelperAbortRequested: true, CollectionMayContinue: true,
		}
		c.retired = backend
		c.backend = nil
		result := Result{Status: c.status, Receipt: c.status.LastReceipt}
		c.mu.Unlock()
		backend.Abort()
		return result, ErrReconciliationRequired
	}
	receipt := Receipt{Operation: string(request.Op), Outcome: "observed", CollectionMayContinue: true}
	switch request.Op {
	case OpOpen:
		c.status.State = "open"
	case OpRead:
		receipt.Outcome = "read"
	case OpStart, OpStop:
		c.status.NativeGeneration = outcome.control.Generation
		c.status.TraceState = outcome.control.TraceState
		c.status.PacketCountKnown = false
		c.status.PacketCount, c.status.Timebase, c.infoHex = 0, "unknown", ""
		receipt.Outcome = "requested"
	case OpClose:
		c.backend, c.owner, c.status.State = nil, "", "closed"
		receipt.Outcome, receipt.NativeDisconnectConfirmed = "closed", true
		if c.lease != nil {
			if err := c.lease.Close(); err != nil {
				c.status.State, c.status.NeedsReconciliation = "faulted", true
				receipt.Outcome, receipt.Reason = "cleanup_failed", "source_lease_release_failed"
				c.status.LastReceipt = receipt
				result := Result{Status: c.status, Receipt: receipt}
				c.mu.Unlock()
				return result, ErrReconciliationRequired
			}
			c.lease = nil
		}
	}
	c.status.LastReceipt = receipt
	result := Result{Status: c.status, Batch: outcome.batch, Receipt: receipt}
	c.mu.Unlock()
	return result, nil
}

func (c *Controller) factoryFailed(epoch uint64, reason string) (Result, error) {
	c.mu.Lock()
	if c.epoch != epoch {
		c.mu.Unlock()
		return Result{}, ErrStaleOperation
	}
	if c.lease != nil {
		if err := c.lease.Close(); err != nil {
			c.status.State, c.status.NeedsReconciliation, c.status.Busy = "faulted", true, false
			c.cancel = nil
			c.status.LastReceipt = Receipt{Operation: "open", Outcome: "cleanup_failed", Reason: "source_lease_release_failed"}
			result := Result{Status: c.status, Receipt: c.status.LastReceipt}
			c.mu.Unlock()
			return result, ErrReconciliationRequired
		}
		c.lease = nil
	}
	c.cancel, c.owner, c.status.Busy, c.status.State = nil, "", false, "idle"
	c.status.LastReceipt = Receipt{Operation: "open", Outcome: "failed_before_native_open", Reason: reason}
	result := Result{Status: c.status, Receipt: c.status.LastReceipt}
	c.mu.Unlock()
	return result, ErrUnavailable
}

func (c *Controller) perform(ctx context.Context, backend Backend, request Request, source timemachine.Source) (operationResult, error) {
	switch request.Op {
	case OpOpen:
		status, err := backend.Open(ctx, tracebridge.OpenArgs{
			Installation: c.options.Installation, MultiVersion: c.options.MultiVersion,
			DLLSHA256: c.options.DLLSHA256, SessionID: source.SessionID,
		})
		if err != nil {
			return operationResult{}, err
		}
		return operationResult{status: &status}, nil
	case OpStatus:
		status, err := backend.Status(ctx)
		if err != nil {
			return operationResult{}, err
		}
		return operationResult{status: &status}, nil
	case OpRead:
		status, err := backend.Status(ctx)
		if err != nil {
			return operationResult{}, err
		}
		result := operationResult{status: &status}
		if status.Generation != request.Read.Generation {
			return result, tracebridge.ErrStaleGeneration
		}
		batch, err := backend.Read(ctx, tracebridge.ReadArgs{
			Start: request.Read.Start, Generation: request.Read.Generation, Count: request.Read.Count,
		})
		if err != nil {
			return result, err
		}
		if batch.Generation != status.Generation || batch.Start != request.Read.Start ||
			batch.Count < 0 || batch.Count > request.Read.Count || batch.Count != len(batch.PacketsHex) {
			return result, ErrInvalidRequest
		}
		reason := "initial"
		if batch.Start != 0 {
			reason = "packet_gap"
		}
		capture := timemachine.Capture{
			Schema: timemachine.CaptureSchema, MultiVersion: c.profile.Version,
			ABI: c.profile.ABI, Source: source, InfoHex: status.InfoHex,
			Generations: []timemachine.Generation{{
				ID: batch.Generation, Reason: reason, FirstIndex: batch.Start,
				PacketsHex: append([]string{}, batch.PacketsHex...),
			}},
		}
		data, err := json.Marshal(capture)
		if err != nil {
			return result, ErrInvalidRequest
		}
		validated, err := timemachine.DecodeCapture(bytes.NewReader(data))
		if err != nil {
			return result, ErrInvalidRequest
		}
		result.batch = &validated
		return result, nil
	case OpStart, OpStop:
		var control tracebridge.ControlResult
		var err error
		if request.Op == OpStart {
			control, err = backend.StartTrace(ctx, request.Authorized)
		} else {
			control, err = backend.StopTrace(ctx, request.Authorized)
		}
		if err != nil {
			return operationResult{}, err
		}
		if !control.Accepted || control.Confirmed || control.Generation == 0 {
			return operationResult{}, ErrInvalidRequest
		}
		return operationResult{control: &control}, nil
	case OpClose:
		return operationResult{}, backend.Close(ctx)
	default:
		return operationResult{}, ErrInvalidRequest
	}
}

func (c *Controller) observe(status tracebridge.Status) error {
	infoBytes, err := hex.DecodeString(status.InfoHex)
	if err != nil || status.Generation == 0 || status.Generation < c.status.NativeGeneration {
		return ErrInvalidRequest
	}
	info, err := timemachine.DecodeInfo(c.profile, infoBytes)
	if err != nil {
		return ErrInvalidRequest
	}
	if status.Generation == c.status.NativeGeneration &&
		((c.infoHex != "" && c.infoHex != status.InfoHex) ||
			(c.status.PacketCountKnown && status.PacketCount < c.status.PacketCount)) {
		return ErrInvalidRequest
	}
	c.infoHex = status.InfoHex
	c.status.NativeGeneration, c.status.PacketCount = status.Generation, status.PacketCount
	c.status.PacketCountKnown = true
	c.status.Timebase, c.status.TraceState = info.Unit, status.TraceState
	return nil
}

// Invalidate makes stale work unable to commit before requesting asynchronous
// child termination. It never calls native disconnect, stop, or target control.
func (c *Controller) Invalidate(owner, reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if owner != "" && owner != c.owner {
		c.mu.Unlock()
		return
	}
	switch reason {
	case "frontend_detached", "daemon_closed", "actor_faulted", "trace_timeout", "trace_cancelled", "control_released":
	default:
		reason = "actor_invalidated"
	}
	if reason == "daemon_closed" {
		c.closed = true
	}
	if c.backend == nil && !c.status.Busy {
		lease, retired := io.Closer(nil), c.retired
		if reason == "daemon_closed" && !c.factoryActive {
			lease, c.lease = c.lease, nil
		}
		c.mu.Unlock()
		releaseAfterReap(lease, retired)
		return
	}
	c.epoch++
	if c.cancel != nil {
		c.cancel()
	}
	backend := c.backend
	if backend != nil {
		c.retired = backend
	}
	c.backend, c.cancel = nil, nil
	c.status.Busy, c.status.NeedsReconciliation, c.status.State = false, true, "faulted"
	c.status.LastReceipt = Receipt{
		Operation: "invalidate", Outcome: "abandoned", Reason: reason,
		NativeStateUncertain: true, HelperAbortRequested: true, CollectionMayContinue: true,
	}
	var lease io.Closer
	if !c.factoryActive && (reason == "daemon_closed" || backend == nil) {
		lease, c.lease = c.lease, nil
	}
	c.mu.Unlock()
	if backend != nil {
		backend.Abort()
	}
	releaseAfterReap(lease, backend)
}

// ShutdownDone closes admission and fences native work before returning the
// actual reaping barrier. It does not imply that native collection stopped.
func (c *Controller) ShutdownDone() <-chan struct{} {
	if c == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	c.Invalidate("", "daemon_closed")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdownDone == nil {
		c.shutdownDone = make(chan struct{})
		go func() {
			c.helpers.Wait()
			close(c.shutdownDone)
		}()
	}
	return c.shutdownDone
}

func releaseAfterReap(lease io.Closer, backend Backend) {
	if lease == nil {
		return
	}
	go func() {
		if backend != nil {
			<-backend.Done()
		}
		_ = lease.Close()
	}()
}
