package tracecontrol

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracebridge"
)

type fakeBackend struct {
	opened, polled, read, started, stopped, closed, aborted atomic.Int32
	poisoned                                                atomic.Bool
	done                                                    chan struct{}
	doneOnce                                                sync.Once
	openFn                                                  func(context.Context) (tracebridge.Status, error)
	statusFn                                                func(context.Context) (tracebridge.Status, error)
	readFn                                                  func(context.Context, tracebridge.ReadArgs) (tracebridge.ReadResult, error)
	startFn                                                 func(context.Context) (tracebridge.ControlResult, error)
	closeFn                                                 func(context.Context) error
}

func newFakeBackend() *fakeBackend { return &fakeBackend{done: make(chan struct{})} }

func nativeStatus(generation, count uint64) tracebridge.Status {
	info := make([]byte, 64)
	binary.LittleEndian.PutUint32(info[8:], 3)
	return tracebridge.Status{
		InfoHex: hex.EncodeToString(info), Generation: generation, PacketCount: count, TraceState: "unknown",
	}
}

func (f *fakeBackend) Open(ctx context.Context, _ tracebridge.OpenArgs) (tracebridge.Status, error) {
	f.opened.Add(1)
	if f.openFn != nil {
		return f.openFn(ctx)
	}
	return nativeStatus(1, 2), nil
}
func (f *fakeBackend) Status(ctx context.Context) (tracebridge.Status, error) {
	f.polled.Add(1)
	if f.statusFn != nil {
		return f.statusFn(ctx)
	}
	return nativeStatus(1, 2), nil
}
func (f *fakeBackend) Read(ctx context.Context, args tracebridge.ReadArgs) (tracebridge.ReadResult, error) {
	f.read.Add(1)
	if f.readFn != nil {
		return f.readFn(ctx, args)
	}
	packets := make([]string, args.Count)
	for index := range packets {
		packets[index] = strings.Repeat("0", 64)
	}
	return tracebridge.ReadResult{Generation: args.Generation, Start: args.Start, Count: args.Count, PacketsHex: packets}, nil
}
func (f *fakeBackend) StartTrace(ctx context.Context, authorized bool) (tracebridge.ControlResult, error) {
	if !authorized {
		return tracebridge.ControlResult{}, tracebridge.ErrNotAuthorized
	}
	f.started.Add(1)
	if f.startFn != nil {
		return f.startFn(ctx)
	}
	return tracebridge.ControlResult{Accepted: true, Generation: 1, TraceState: "start_requested"}, nil
}
func (f *fakeBackend) StopTrace(_ context.Context, authorized bool) (tracebridge.ControlResult, error) {
	if !authorized {
		return tracebridge.ControlResult{}, tracebridge.ErrNotAuthorized
	}
	f.stopped.Add(1)
	return tracebridge.ControlResult{Accepted: true, Generation: 1, TraceState: "stop_requested"}, nil
}
func (f *fakeBackend) Close(ctx context.Context) error {
	f.closed.Add(1)
	if f.closeFn != nil {
		return f.closeFn(ctx)
	}
	f.doneOnce.Do(func() { close(f.done) })
	return nil
}
func (f *fakeBackend) Abort() {
	f.aborted.Add(1)
	f.poisoned.Store(true)
	f.doneOnce.Do(func() { close(f.done) })
}
func (f *fakeBackend) Poisoned() bool        { return f.poisoned.Load() }
func (f *fakeBackend) Done() <-chan struct{} { return f.done }

func newController(t *testing.T, factory func(context.Context) (Backend, error)) *Controller {
	t.Helper()
	controller, err := New(Options{
		Factory:      func(ctx context.Context, _ io.Closer) (Backend, error) { return factory(ctx) },
		Installation: filepath.Join(t.TempDir(), "multi"),
		MultiVersion: "8.1.4", DLLSHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-controller.ShutdownDone():
		case <-time.After(3 * time.Second):
			t.Error("fake native helper was not reaped")
		}
	})
	return controller
}

func openRequest() Request {
	return Request{Op: OpOpen, Open: OpenRequest{
		Source:                      timemachine.Source{Kind: "live", SessionID: 17, SourceID: "PRIVATE_SOURCE"},
		AcknowledgeUnverifiedSource: true,
	}}
}

func openController(t *testing.T, controller *Controller) uint64 {
	t.Helper()
	result, err := controller.Execute(context.Background(), "owner", openRequest())
	if err != nil {
		t.Fatal(err)
	}
	return result.Status.Handle
}

func TestControllerValidatesSourceAndConsentBeforeFactory(t *testing.T) {
	var factories atomic.Int32
	controller := newController(t, func(context.Context) (Backend, error) {
		factories.Add(1)
		return newFakeBackend(), nil
	})
	for _, change := range []func(*Request){
		func(r *Request) { r.Open.AcknowledgeUnverifiedSource = false },
		func(r *Request) { r.Open.Source.SessionID = 0 },
		func(r *Request) { r.Open.Source.Kind = "synthetic_fixture" },
		func(r *Request) { r.Open.Source.Kind = "snapshot" },
		func(r *Request) { r.Open.Source.SourceID = "" },
		func(r *Request) { r.Open.Source.SourceID = "PRIVATE\nSOURCE" },
		func(r *Request) {
			r.Open.Source.InitialContext = &timemachine.Context{TaskID: "1", AddressSpaceID: "0"}
		},
		func(r *Request) { r.Authorized = true },
		func(r *Request) { r.Handle = 1 },
	} {
		request := openRequest()
		change(&request)
		if _, err := controller.Execute(context.Background(), "owner", request); err == nil {
			t.Fatal("invalid source admitted")
		}
	}
	if factories.Load() != 0 {
		t.Fatal("invalid source reached the native factory")
	}
	if _, err := controller.Execute(context.Background(), "", openRequest()); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("empty owner = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := controller.Execute(ctx, "owner", openRequest()); !errors.Is(err, context.Canceled) || factories.Load() != 0 {
		t.Fatal("pre-cancelled open reached the factory")
	}
}

func TestControllerReadAndCloseNeverControlCollection(t *testing.T) {
	backend := newFakeBackend()
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	handle := openController(t, controller)
	result, err := controller.Execute(context.Background(), "owner", Request{
		Op: OpRead, Handle: handle, Read: ReadRequest{Generation: 1, Count: 2},
	})
	if err != nil || result.Batch == nil || len(result.Batch.Generations[0].PacketsHex) != 2 {
		t.Fatalf("read = %#v, %v", result, err)
	}
	if result.Batch.Source.Kind != "live" || result.Status.SourceBinding != "operator_declared" ||
		result.Status.HardwareQualified || result.Status.NativeRuntimeQualified {
		t.Fatal("read upgraded source qualification")
	}
	result, err = controller.Execute(context.Background(), "owner", Request{Op: OpClose, Handle: handle})
	if err != nil || !result.Receipt.NativeDisconnectConfirmed || result.Status.State != "closed" ||
		!result.Receipt.CollectionMayContinue {
		t.Fatalf("close = %#v, %v", result, err)
	}
	controller.Invalidate("owner", "frontend_detached")
	if backend.started.Load() != 0 || backend.stopped.Load() != 0 || backend.closed.Load() != 1 || backend.aborted.Load() != 0 {
		t.Fatal("read/close/detach issued unexpected native control")
	}
}

func TestControllerOwnersHandlesAndExplicitTraceAuthorization(t *testing.T) {
	backend := newFakeBackend()
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	handle := openController(t, controller)
	if _, err := controller.Execute(context.Background(), "other", Request{Op: OpStatus, Handle: handle}); !errors.Is(err, ErrNotOwner) {
		t.Fatal("different owner reached the capture")
	}
	if _, err := controller.Execute(context.Background(), "owner", Request{Op: OpStatus, Handle: handle + 1}); !errors.Is(err, ErrStaleHandle) {
		t.Fatal("stale handle accepted")
	}
	controller.Invalidate("other", "frontend_detached")
	for _, op := range []Operation{OpStart, OpStop} {
		if _, err := controller.Execute(context.Background(), "owner", Request{Op: op, Handle: handle}); err == nil {
			t.Fatal("implicit native collection control admitted")
		}
		result, err := controller.Execute(context.Background(), "owner", Request{Op: op, Handle: handle, Authorized: true})
		if err != nil || result.Receipt.Outcome != "requested" || result.Status.PacketCountKnown ||
			result.Status.TraceState != string(op)+"_requested" {
			t.Fatalf("control outcome = %#v, %v", result, err)
		}
	}
	if backend.started.Load() != 1 || backend.stopped.Load() != 1 || backend.aborted.Load() != 0 {
		t.Fatal("authorization changed the native call count")
	}
}

func TestControllerGenerationChangeIsRecoverableWithoutReturningBatch(t *testing.T) {
	backend := newFakeBackend()
	backend.statusFn = func(context.Context) (tracebridge.Status, error) { return nativeStatus(2, 1), nil }
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	handle := openController(t, controller)
	result, err := controller.Execute(context.Background(), "owner", Request{Op: OpRead, Handle: handle, Read: ReadRequest{Generation: 1, Count: 1}})
	if !errors.Is(err, tracebridge.ErrStaleGeneration) || result.Batch != nil ||
		result.Status.NativeGeneration != 2 || result.Status.NeedsReconciliation || backend.read.Load() != 0 {
		t.Fatalf("generation change = %#v, %v", result, err)
	}
	result, err = controller.Execute(context.Background(), "owner", Request{Op: OpRead, Handle: handle, Read: ReadRequest{Generation: 2, Count: 1}})
	if err != nil || result.Batch == nil || result.Batch.Generations[0].ID != 2 {
		t.Fatalf("explicit fresh generation read = %#v, %v", result, err)
	}
}

func TestControllerMetadataChangeWithoutGenerationPoisonsCapture(t *testing.T) {
	backend := newFakeBackend()
	backend.statusFn = func(context.Context) (tracebridge.Status, error) {
		status := nativeStatus(1, 2)
		info, _ := hex.DecodeString(status.InfoHex)
		binary.LittleEndian.PutUint32(info[8:], 2)
		status.InfoHex = hex.EncodeToString(info)
		return status, nil
	}
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	handle := openController(t, controller)
	result, err := controller.Execute(context.Background(), "owner", Request{Op: OpStatus, Handle: handle})
	if !errors.Is(err, ErrReconciliationRequired) || !result.Status.NeedsReconciliation || backend.aborted.Load() != 1 {
		t.Fatalf("metadata replacement = %#v, %v", result, err)
	}
}

func TestControllerUncertainControlNeverRetriesOrClaimsRecovery(t *testing.T) {
	backend := newFakeBackend()
	backend.startFn = func(context.Context) (tracebridge.ControlResult, error) {
		backend.poisoned.Store(true)
		return tracebridge.ControlResult{}, errors.New("PRIVATE_NATIVE_FAILURE")
	}
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	handle := openController(t, controller)
	result, err := controller.Execute(context.Background(), "owner", Request{Op: OpStart, Handle: handle, Authorized: true})
	if !errors.Is(err, ErrReconciliationRequired) || strings.Contains(err.Error(), "PRIVATE") ||
		!result.Receipt.NativeStateUncertain || result.Receipt.NativeDisconnectConfirmed {
		t.Fatalf("uncertain control = %#v, %v", result, err)
	}
	for _, op := range []Operation{OpStart, OpStatus, OpClose} {
		_, err := controller.Execute(context.Background(), "owner", Request{Op: op, Handle: handle, Authorized: op == OpStart})
		if !errors.Is(err, ErrReconciliationRequired) {
			t.Fatalf("uncertain state admitted %s: %v", op, err)
		}
	}
	if backend.started.Load() != 1 || backend.stopped.Load() != 0 || backend.closed.Load() != 0 {
		t.Fatal("uncertain native operation was retried or silently cleaned up")
	}
}

func TestControllerDetachFencesBlockedReadAndNeverStopsTrace(t *testing.T) {
	backend := newFakeBackend()
	entered, release := make(chan struct{}), make(chan struct{})
	backend.readFn = func(_ context.Context, args tracebridge.ReadArgs) (tracebridge.ReadResult, error) {
		close(entered)
		<-release
		return tracebridge.ReadResult{Generation: 1, Start: args.Start, Count: 1, PacketsHex: []string{strings.Repeat("0", 64)}}, nil
	}
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	handle := openController(t, controller)
	completed := make(chan error, 1)
	go func() {
		result, err := controller.Execute(context.Background(), "owner", Request{Op: OpRead, Handle: handle, Read: ReadRequest{Generation: 1, Count: 1}})
		if result.Batch != nil {
			t.Error("late capture escaped the expired owner")
		}
		completed <- err
	}()
	<-entered
	controller.Invalidate("owner", "frontend_detached")
	if !controller.Snapshot().NeedsReconciliation || backend.aborted.Load() != 1 {
		t.Fatal("detach did not immediately invalidate blocked trace work")
	}
	close(release)
	if err := <-completed; !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("late read = %v", err)
	}
	if backend.stopped.Load() != 0 || backend.closed.Load() != 0 {
		t.Fatal("detach silently commanded native cleanup")
	}
}

func TestControllerLateFactoryIsAbortedBeforeNativeOpen(t *testing.T) {
	backend := newFakeBackend()
	entered, release := make(chan struct{}), make(chan struct{})
	controller := newController(t, func(context.Context) (Backend, error) {
		close(entered)
		<-release
		return backend, nil
	})
	completed := make(chan error, 1)
	go func() {
		_, err := controller.Execute(context.Background(), "owner", openRequest())
		completed <- err
	}()
	<-entered
	controller.Invalidate("owner", "control_released")
	close(release)
	if err := <-completed; !errors.Is(err, ErrStaleOperation) || backend.opened.Load() != 0 || backend.aborted.Load() != 1 {
		t.Fatal("late helper started a native connection")
	}
}

func TestControllerSpontaneousExitIsVisibleInLocalSnapshot(t *testing.T) {
	backend := newFakeBackend()
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	openController(t, controller)
	backend.doneOnce.Do(func() { close(backend.done) })
	status := controller.Snapshot()
	if !status.NeedsReconciliation || status.LastReceipt.Reason != "helper_exited" ||
		status.LastReceipt.NativeDisconnectConfirmed {
		t.Fatalf("dead helper snapshot = %#v", status)
	}
}

type sourceLease struct {
	closed chan struct{}
	once   sync.Once
	err    error
}

func newSourceLease() *sourceLease { return &sourceLease{closed: make(chan struct{})} }

func (l *sourceLease) Close() error {
	if l.err == nil {
		l.once.Do(func() { close(l.closed) })
	}
	return l.err
}

func (l *sourceLease) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

func withSourceLease(controller *Controller, lease *sourceLease) {
	controller.options.AcquireSource = func(context.Context, uint32) (io.Closer, error) { return lease, nil }
}

func TestControllerSourceContentionPrecedesFactoryAndCanBeRetried(t *testing.T) {
	var factories atomic.Int32
	backend := newFakeBackend()
	controller := newController(t, func(context.Context) (Backend, error) {
		factories.Add(1)
		return backend, nil
	})
	controller.options.AcquireSource = func(context.Context, uint32) (io.Closer, error) {
		return nil, errors.New("PRIVATE_SOURCE_OWNER")
	}
	result, err := controller.Execute(context.Background(), "owner", openRequest())
	if !errors.Is(err, ErrUnavailable) || result.Status.State != "idle" ||
		result.Receipt.Reason != "source_lease_unavailable" || result.Status.NeedsReconciliation ||
		factories.Load() != 0 || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("source contention = %#v, %v", result, err)
	}
	lease := newSourceLease()
	withSourceLease(controller, lease)
	openController(t, controller)
	if factories.Load() != 1 || lease.isClosed() {
		t.Fatal("source contention prevented an explicit retry or released the active lease")
	}
}

func TestControllerConfirmedCloseReleasesSource(t *testing.T) {
	backend := newFakeBackend()
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	lease := newSourceLease()
	withSourceLease(controller, lease)
	handle := openController(t, controller)
	if lease.isClosed() {
		t.Fatal("active source lease was released")
	}
	if _, err := controller.Execute(context.Background(), "owner", Request{Op: OpClose, Handle: handle}); err != nil {
		t.Fatal(err)
	}
	if !lease.isClosed() || backend.stopped.Load() != 0 {
		t.Fatal("confirmed disconnect did not release the source, or silently stopped collection")
	}
}

func TestControllerUncertaintyRetainsSourceUntilDaemonShutdown(t *testing.T) {
	backend := newFakeBackend()
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	lease := newSourceLease()
	withSourceLease(controller, lease)
	openController(t, controller)
	controller.Invalidate("owner", "frontend_detached")
	if lease.isClosed() || !controller.Snapshot().NeedsReconciliation {
		t.Fatal("frontend loss prematurely released uncertain source ownership")
	}
	<-controller.ShutdownDone()
	select {
	case <-lease.closed:
	case <-time.After(time.Second):
		t.Fatal("daemon shutdown did not release reaped source ownership")
	}
	if _, err := controller.Execute(context.Background(), "owner", openRequest()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("shutdown reopened admission")
	}
}

func TestControllerShutdownPinsLeaseThroughoutLateFactory(t *testing.T) {
	backend := newFakeBackend()
	entered, release := make(chan struct{}), make(chan struct{})
	lease := newSourceLease()
	controller := newController(t, nil)
	withSourceLease(controller, lease)
	controller.options.Factory = func(_ context.Context, acquired io.Closer) (Backend, error) {
		if acquired != lease {
			t.Error("factory did not receive its source lifetime handle")
		}
		close(entered)
		<-release
		if lease.isClosed() {
			t.Error("lease was closed during process-handle inheritance")
		}
		return backend, nil
	}
	completed := make(chan error, 1)
	go func() {
		_, err := controller.Execute(context.Background(), "owner", openRequest())
		completed <- err
	}()
	<-entered
	done := controller.ShutdownDone()
	select {
	case <-done:
		t.Error("shutdown completed while helper creation was pending")
	default:
	}
	if lease.isClosed() {
		t.Error("shutdown invalidated handles still in use by the factory")
	}
	close(release)
	if err := <-completed; !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("late factory = %v", err)
	}
	<-done
	select {
	case <-lease.closed:
	case <-time.After(time.Second):
		t.Fatal("late factory source lease leaked")
	}
	if backend.opened.Load() != 0 || backend.aborted.Load() != 1 {
		t.Fatal("late helper made native contact")
	}
}

type delayedReapBackend struct{ *fakeBackend }

func (b *delayedReapBackend) Abort() {
	b.aborted.Add(1)
	b.poisoned.Store(true)
}

func TestControllerShutdownWaitsForReapNotAbortRequest(t *testing.T) {
	backend := &delayedReapBackend{newFakeBackend()}
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	lease := newSourceLease()
	withSourceLease(controller, lease)
	openController(t, controller)
	done := controller.ShutdownDone()
	select {
	case <-done:
		t.Error("abort request was treated as actual reaping")
	default:
	}
	if lease.isClosed() {
		t.Error("source released before helper reaping")
	}
	backend.doneOnce.Do(func() { close(backend.done) })
	<-done
	select {
	case <-lease.closed:
	case <-time.After(time.Second):
		t.Fatal("reaped source lease was not released")
	}
}

func TestControllerTracksFailedFactoryBackendUntilReaped(t *testing.T) {
	backend := &delayedReapBackend{newFakeBackend()}
	controller := newController(t, func(context.Context) (Backend, error) {
		return backend, tracebridge.ErrCleanupUnconfirmed
	})
	result, err := controller.Execute(context.Background(), "owner", openRequest())
	if !errors.Is(err, ErrUnavailable) || result.Receipt.Outcome != "failed_before_native_open" ||
		backend.opened.Load() != 0 || backend.aborted.Load() != 1 {
		t.Fatalf("failed helper factory = %#v, %v", result, err)
	}
	done := controller.ShutdownDone()
	select {
	case <-done:
		t.Error("failed-start helper ownership was lost before actual reaping")
	default:
	}
	backend.doneOnce.Do(func() { close(backend.done) })
	<-done
}

func TestControllerLeaseReleaseFailureDoesNotClaimRecovery(t *testing.T) {
	backend := newFakeBackend()
	controller := newController(t, func(context.Context) (Backend, error) { return backend, nil })
	lease := newSourceLease()
	lease.err = errors.New("PRIVATE_HOST_LOCK_ERROR")
	withSourceLease(controller, lease)
	handle := openController(t, controller)
	result, err := controller.Execute(context.Background(), "owner", Request{Op: OpClose, Handle: handle})
	if !errors.Is(err, ErrReconciliationRequired) || !result.Status.NeedsReconciliation ||
		result.Receipt.Reason != "source_lease_release_failed" || !result.Receipt.NativeDisconnectConfirmed ||
		strings.Contains(err.Error(), "PRIVATE") || lease.isClosed() {
		t.Fatalf("failed source cleanup = %#v, %v", result, err)
	}
}
