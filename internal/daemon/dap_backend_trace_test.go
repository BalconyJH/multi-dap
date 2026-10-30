package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

type nativeTraceCall struct {
	owner   session.ControllerID
	request tracecontrol.Request
}

type fakeNativeTraceCore struct {
	*fakeCore
	nativeMu       sync.Mutex
	enabled        bool
	nativeCalls    []nativeTraceCall
	snapshotOwners []session.ControllerID
	traceCall      func(context.Context, session.ControllerID, tracecontrol.Request) (tracecontrol.Result, error)
	snapshotCall   func(context.Context, session.ControllerID) (tracecontrol.Status, error)
	traceStatus    tracecontrol.Status
}

func (c *fakeNativeTraceCore) TraceEnabled() bool { return c.enabled }

func (c *fakeNativeTraceCore) Trace(ctx context.Context, owner session.ControllerID, request tracecontrol.Request) (tracecontrol.Result, error) {
	c.nativeMu.Lock()
	c.nativeCalls = append(c.nativeCalls, nativeTraceCall{owner, tracecontrol.CloneRequest(request)})
	c.nativeMu.Unlock()
	if c.traceCall != nil {
		return c.traceCall(ctx, owner, request)
	}
	return tracecontrol.Result{Status: c.traceStatus}, nil
}

func (c *fakeNativeTraceCore) TraceSnapshot(ctx context.Context, owner session.ControllerID) (tracecontrol.Status, error) {
	c.nativeMu.Lock()
	c.snapshotOwners = append(c.snapshotOwners, owner)
	c.nativeMu.Unlock()
	if c.snapshotCall != nil {
		return c.snapshotCall(ctx, owner)
	}
	return c.traceStatus, nil
}

func boundNativeTraceBackend(t *testing.T) (*Backend, *fakeNativeTraceCore) {
	t.Helper()
	core := &fakeNativeTraceCore{
		fakeCore: newFakeCore(), enabled: true,
		traceStatus: tracecontrol.Status{
			Handle: 1, State: "open", NativeGeneration: 1,
			SourceBinding: "operator_declared", Timebase: "nanoseconds", TraceState: "unknown",
		},
	}
	backend, err := NewBackend(core)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.BindPublisher(&fakePublisher{accept: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	return backend, core
}

func nativeCapture(t *testing.T, start, generation uint64, count int) *timemachine.Capture {
	t.Helper()
	profile, err := timemachine.ProfileForVersion("8.1.4")
	if err != nil {
		t.Fatal(err)
	}
	packets := make([]string, count)
	for index := range packets {
		packets[index] = strings.Repeat("0", 64)
	}
	return &timemachine.Capture{
		Schema: timemachine.CaptureSchema, MultiVersion: profile.Version, ABI: profile.ABI,
		Source:  timemachine.Source{Kind: "live", SessionID: 17, SourceID: "explicit source"},
		InfoHex: strings.Repeat("0", profile.ABI.InfoSize*2),
		Generations: []timemachine.Generation{{
			ID: generation, Reason: "initial", FirstIndex: start, PacketsHex: packets,
		}},
	}
}

func nativeTraceErrorCode(t *testing.T, err error) dap.NativeTraceErrorCode {
	t.Helper()
	var typed *dap.NativeTraceError
	if !errors.As(err, &typed) {
		t.Fatalf("expected sanitized trace error, got %v", err)
	}
	return typed.Code
}

func TestNativeTraceBackendForwardsOnlyCurrentOwnerAndTypedRequests(t *testing.T) {
	backend, core := boundNativeTraceBackend(t)
	core.traceCall = func(_ context.Context, _ session.ControllerID, request tracecontrol.Request) (tracecontrol.Result, error) {
		result := tracecontrol.Result{Status: core.traceStatus}
		if request.Op == tracecontrol.OpRead {
			result.Batch = nativeCapture(t, request.Read.Start, request.Read.Generation, request.Read.Count)
		}
		return result, nil
	}
	attach(t, backend)
	coreID := uint32(0)
	cases := []struct {
		action dap.Action
		op     tracecontrol.Operation
	}{
		{dap.Action{Kind: dap.ActionTraceOpen, TraceOpen: dap.NativeTraceOpenArguments{
			SourceKind: "live", SessionID: 17, SourceID: "explicit source", CoreID: &coreID, AcknowledgeUnverifiedSource: true,
		}}, tracecontrol.OpOpen},
		{dap.Action{Kind: dap.ActionTracePoll, TraceHandle: dap.NativeTraceHandleArguments{Handle: 1}}, tracecontrol.OpStatus},
		{dap.Action{Kind: dap.ActionTraceRead, TraceRead: dap.NativeTraceReadArguments{
			Handle: 1, Start: math.MaxUint64 - 1, Generation: math.MaxUint64, Count: 1,
		}}, tracecontrol.OpRead},
		{dap.Action{Kind: dap.ActionTraceStart, TraceControl: dap.NativeTraceControlArguments{Handle: 1, Authorized: true}}, tracecontrol.OpStart},
		{dap.Action{Kind: dap.ActionTraceStop, TraceControl: dap.NativeTraceControlArguments{Handle: 1, Authorized: true}}, tracecontrol.OpStop},
		{dap.Action{Kind: dap.ActionTraceClose, TraceHandle: dap.NativeTraceHandleArguments{Handle: 1}}, tracecontrol.OpClose},
	}
	for _, test := range cases {
		result, err := backend.Execute(context.Background(), test.action)
		if err != nil {
			t.Fatalf("trace %s: %v", test.op, err)
		}
		var decoded tracecontrol.Result
		if err := json.Unmarshal(result.TraceBody, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Status.NativeRuntimeQualified || decoded.Status.HardwareQualified {
			t.Fatal("wiring claimed native runtime or hardware qualification")
		}
	}
	core.nativeMu.Lock()
	calls := append([]nativeTraceCall(nil), core.nativeCalls...)
	core.nativeMu.Unlock()
	core.mu.Lock()
	owner := core.attachOwners[0]
	executions := len(core.executions)
	core.mu.Unlock()
	if len(calls) != len(cases) || executions != 0 {
		t.Fatalf("native calls = %d, target execution requests = %d", len(calls), executions)
	}
	for index, call := range calls {
		if call.owner != owner || call.request.Op != cases[index].op {
			t.Fatalf("call %d = %#v; current owner %q", index, call, owner)
		}
	}
	open := calls[0].request.Open
	if !open.AcknowledgeUnverifiedSource || open.Source.SessionID != 17 || open.Source.SourceID != "explicit source" ||
		open.Source.InitialContext != nil || open.Source.CoreID == nil || *open.Source.CoreID != coreID {
		t.Fatalf("mapped source = %#v", open)
	}
	if read := calls[2].request.Read; read.Start != math.MaxUint64-1 || read.Generation != math.MaxUint64 {
		t.Fatal("backend narrowed the uint64 read request")
	}
	if !calls[3].request.Authorized || !calls[4].request.Authorized {
		t.Fatal("explicit start/stop authorization was lost")
	}
}

func TestNativeTraceStatusIsLocalEvenAfterNativeFailure(t *testing.T) {
	backend, core := boundNativeTraceBackend(t)
	core.traceStatus.State = "faulted"
	core.traceStatus.NeedsReconciliation = true
	core.traceStatus.LastReceipt = tracecontrol.Receipt{
		Operation: "stop", Outcome: "failed", Reason: "native_operation_failed",
		NativeStateUncertain: true, CollectionMayContinue: true,
	}
	core.traceCall = func(context.Context, session.ControllerID, tracecontrol.Request) (tracecontrol.Result, error) {
		return tracecontrol.Result{}, tracecontrol.ErrReconciliationRequired
	}
	attach(t, backend)
	_, err := backend.Execute(context.Background(), dap.Action{
		Kind: dap.ActionTracePoll, TraceHandle: dap.NativeTraceHandleArguments{Handle: 1},
	})
	if nativeTraceErrorCode(t, err) != dap.NativeTraceReconciliationRequired {
		t.Fatal("poll did not preserve the reconciliation classification")
	}
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionTraceStatus})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Status tracecontrol.Status `json:"status"`
	}
	if err := json.Unmarshal(result.TraceBody, &body); err != nil {
		t.Fatal(err)
	}
	if !body.Status.NeedsReconciliation || !body.Status.LastReceipt.CollectionMayContinue ||
		body.Status.LastReceipt.Outcome != "failed" {
		t.Fatalf("local receipt = %#v", body.Status)
	}
	core.nativeMu.Lock()
	nativeCalls, snapshots := len(core.nativeCalls), append([]session.ControllerID(nil), core.snapshotOwners...)
	core.nativeMu.Unlock()
	core.mu.Lock()
	owner := core.attachOwners[0]
	core.mu.Unlock()
	if nativeCalls != 1 || len(snapshots) != 1 || snapshots[0] != owner {
		t.Fatalf("status issued native IPC or lost owner: calls=%d, owners=%v", nativeCalls, snapshots)
	}
}

func TestNativeTraceCapabilityCannotBeForcedOnUnsupportedCore(t *testing.T) {
	for _, core := range []Core{
		newFakeCore(),
		&fakeNativeTraceCore{fakeCore: newFakeCore()},
	} {
		backend, err := NewBackendWithOptions(core, BackendOptions{
			Capabilities: dap.Capabilities{SupportsReadMemoryRequest: true, MultiDapSupportsNativeTraceRequests: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(backend.Close)
		capabilities := backend.DAPCapabilities()
		if capabilities.MultiDapSupportsNativeTraceRequests || !capabilities.SupportsReadMemoryRequest {
			t.Fatalf("capability overlay = %#v", capabilities)
		}
		result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionTraceStatus})
		if nativeTraceErrorCode(t, err) != dap.NativeTraceUnavailable || len(result.TraceBody) != 0 {
			t.Fatal("unsupported core did not fail closed")
		}
	}
	backend, _ := boundNativeTraceBackend(t)
	if !backend.DAPCapabilities().MultiDapSupportsNativeTraceRequests {
		t.Fatal("wired optional trace interface was not advertised")
	}
}

func TestNativeTraceDirectBackendCallsRespectConfigurationAndArguments(t *testing.T) {
	backend, core := boundNativeTraceBackend(t)
	action := dap.Action{Kind: dap.ActionTraceStatus}
	_, err := backend.Execute(context.Background(), action)
	if nativeTraceErrorCode(t, err) != dap.NativeTraceNotOwner {
		t.Fatal("trace accepted an absent frontend")
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionAttach}); err != nil {
		t.Fatal(err)
	}
	_, err = backend.Execute(context.Background(), action)
	if nativeTraceErrorCode(t, err) != dap.NativeTraceNotOwner {
		t.Fatal("trace accepted a configuring frontend")
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionConfigurationDone}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []dap.Action{
		{Kind: dap.ActionTracePoll},
		{Kind: dap.ActionTraceRead, TraceRead: dap.NativeTraceReadArguments{Handle: 1, Generation: 1, Count: 4097}},
		{Kind: dap.ActionTraceStart, TraceControl: dap.NativeTraceControlArguments{Handle: 1}},
		{Kind: dap.ActionTraceStatus, TraceHandle: dap.NativeTraceHandleArguments{Handle: 1}},
	} {
		result, err := backend.Execute(context.Background(), invalid)
		if nativeTraceErrorCode(t, err) != dap.NativeTraceInvalidArguments || len(result.TraceBody) != 0 {
			t.Fatal("invalid in-process action was not rejected")
		}
	}
	core.nativeMu.Lock()
	defer core.nativeMu.Unlock()
	if len(core.nativeCalls) != 0 || len(core.snapshotOwners) != 0 {
		t.Fatal("invalid or unconfigured request reached core")
	}
}

func TestNativeTraceLateOldFrontendResultsAreDiscarded(t *testing.T) {
	for _, kind := range []dap.ActionKind{dap.ActionTraceRead, dap.ActionTraceStatus} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			backend, core := boundNativeTraceBackend(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			capture := nativeCapture(t, 0, 1, 1)
			capture.Source.SourceID = "PRIVATE_OLD_FRONTEND_BATCH"
			core.traceCall = func(context.Context, session.ControllerID, tracecontrol.Request) (tracecontrol.Result, error) {
				close(entered)
				<-release
				return tracecontrol.Result{Status: core.traceStatus, Batch: capture}, nil
			}
			core.snapshotCall = func(context.Context, session.ControllerID) (tracecontrol.Status, error) {
				close(entered)
				<-release
				return core.traceStatus, nil
			}
			attach(t, backend)
			action := dap.Action{Kind: kind}
			if kind == dap.ActionTraceRead {
				action.TraceRead = dap.NativeTraceReadArguments{Handle: 1, Generation: 1, Count: 1}
			}
			type completion struct {
				result dap.ActionResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				result, err := backend.Execute(context.Background(), action)
				done <- completion{result, err}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("trace did not enter fake core")
			}
			if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionDisconnect}); err != nil {
				t.Fatal(err)
			}
			attach(t, backend)
			releaseOnce.Do(func() { close(release) })
			select {
			case finished := <-done:
				if nativeTraceErrorCode(t, finished.err) != dap.NativeTraceStaleOperation || len(finished.result.TraceBody) != 0 {
					t.Fatal("old frontend trace result escaped owner fence")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("late trace did not finish")
			}
			core.mu.Lock()
			owners := append([]session.ControllerID(nil), core.attachOwners...)
			core.mu.Unlock()
			if len(owners) != 2 || owners[0] == owners[1] {
				t.Fatalf("frontend owner did not rotate: %v", owners)
			}
		})
	}
}

func TestNativeTraceBackendSanitizesFailuresAndDropsReturnedBatch(t *testing.T) {
	for _, test := range []struct {
		cause error
		code  dap.NativeTraceErrorCode
	}{
		{errors.New("PRIVATE_NATIVE_FAILURE"), dap.NativeTraceFailed},
		{tracecontrol.ErrUnavailable, dap.NativeTraceUnavailable},
		{tracecontrol.ErrInvalidRequest, dap.NativeTraceInvalidArguments},
		{tracecontrol.ErrUnverifiedSource, dap.NativeTraceSourceAcknowledgementRequired},
		{tracecontrol.ErrNotOwner, dap.NativeTraceNotOwner},
		{session.ErrNotLeaseHolder, dap.NativeTraceNotOwner},
		{tracecontrol.ErrStaleHandle, dap.NativeTraceStaleHandle},
		{tracecontrol.ErrBusy, dap.NativeTraceBusy},
		{tracecontrol.ErrStaleOperation, dap.NativeTraceStaleOperation},
		{tracecontrol.ErrReconciliationRequired, dap.NativeTraceReconciliationRequired},
		{tracecontrol.ErrStaleGeneration, dap.NativeTraceGenerationChanged},
		{context.Canceled, dap.NativeTraceCancelled},
		{context.DeadlineExceeded, dap.NativeTraceTimeout},
	} {
		backend, core := boundNativeTraceBackend(t)
		batch := nativeCapture(t, 0, 1, 1)
		batch.Source.SourceID = "PRIVATE_PARTIAL_BATCH"
		core.traceCall = func(context.Context, session.ControllerID, tracecontrol.Request) (tracecontrol.Result, error) {
			return tracecontrol.Result{Batch: batch}, fmt.Errorf("PRIVATE_WRAPPER: %w", test.cause)
		}
		attach(t, backend)
		result, err := backend.Execute(context.Background(), dap.Action{
			Kind: dap.ActionTraceRead, TraceRead: dap.NativeTraceReadArguments{Handle: 1, Generation: 1, Count: 1},
		})
		if nativeTraceErrorCode(t, err) != test.code || !errors.Is(err, test.cause) ||
			strings.Contains(err.Error(), "PRIVATE") || len(result.TraceBody) != 0 {
			t.Fatalf("unsafe native error mapping: %v", err)
		}
	}
}

func TestNativeTraceMaximumBatchFitsDAPAndBadCoreResultsFailClosed(t *testing.T) {
	backend, core := boundNativeTraceBackend(t)
	capture := nativeCapture(t, 0, 1, dap.MaxNativeTracePackets)
	core.traceCall = func(context.Context, session.ControllerID, tracecontrol.Request) (tracecontrol.Result, error) {
		return tracecontrol.Result{Status: core.traceStatus, Batch: capture}, nil
	}
	attach(t, backend)
	action := dap.Action{Kind: dap.ActionTraceRead, TraceRead: dap.NativeTraceReadArguments{
		Handle: 1, Generation: 1, Count: dap.MaxNativeTracePackets,
	}}
	result, err := backend.Execute(context.Background(), action)
	if err != nil {
		t.Fatal(err)
	}
	success := true
	var wire bytes.Buffer
	if err := dap.NewEncoder(&wire).Encode(dap.Envelope{
		Seq: 1, Type: "response", RequestSeq: 1, Success: &success, Command: "multi-dap/trace/read", Body: result.TraceBody,
	}); err != nil {
		t.Fatal(err)
	}
	if wire.Len() >= 1<<20 {
		t.Fatalf("native batch DAP frame = %d bytes", wire.Len())
	}
	for _, mutation := range []func(*timemachine.Capture){
		func(c *timemachine.Capture) { c.Generations[0].ID++ },
		func(c *timemachine.Capture) { c.Generations[0].FirstIndex++ },
		func(c *timemachine.Capture) {
			c.Generations[0].PacketsHex = append(c.Generations[0].PacketsHex, strings.Repeat("0", 64))
		},
		func(c *timemachine.Capture) { c.Generations[0].PacketsHex[0] = strings.Repeat("PRIVATE", 100000) },
		func(c *timemachine.Capture) { c.Source.InitialContext = &timemachine.Context{} },
		func(c *timemachine.Capture) { c.Source.Kind = "synthetic_fixture" },
		func(c *timemachine.Capture) { c.InfoHex = "not-hex" },
		func(c *timemachine.Capture) { c.ABI.ByteOrder = strings.Repeat("PRIVATE", 100000) },
	} {
		capture = nativeCapture(t, 0, 1, dap.MaxNativeTracePackets)
		mutation(capture)
		result, err := backend.Execute(context.Background(), action)
		if nativeTraceErrorCode(t, err) != dap.NativeTraceInvalidResponse || len(result.TraceBody) != 0 {
			t.Fatal("unbounded or invalid native batch escaped typed response validation")
		}
	}
	capture = nativeCapture(t, 0, 1, 1)
	result, err = backend.Execute(context.Background(), dap.Action{
		Kind: dap.ActionTracePoll, TraceHandle: dap.NativeTraceHandleArguments{Handle: 1},
	})
	if nativeTraceErrorCode(t, err) != dap.NativeTraceInvalidResponse || len(result.TraceBody) != 0 {
		t.Fatal("poll emitted raw trace data")
	}
}
