package actor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

type traceServiceCall struct {
	owner   string
	request tracecontrol.Request
}

type traceInvalidation struct {
	owner  string
	reason string
}

type testTraceService struct {
	mu            sync.Mutex
	enabled       bool
	holder        string
	status        tracecontrol.Status
	calls         []traceServiceCall
	invalidations []traceInvalidation
	execute       func(context.Context, string, tracecontrol.Request) (tracecontrol.Result, error)
}

func newTestTraceService() *testTraceService {
	return &testTraceService{
		enabled: true, holder: "frontend",
		status: tracecontrol.Status{Handle: 1, State: "ready", TraceState: "unknown"},
	}
}

func (s *testTraceService) Enabled() bool { return s.enabled }

func (s *testTraceService) Execute(ctx context.Context, owner string, request tracecontrol.Request) (tracecontrol.Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, traceServiceCall{owner: owner, request: tracecontrol.CloneRequest(request)})
	execute := s.execute
	if execute == nil {
		switch request.Op {
		case tracecontrol.OpOpen:
			s.holder, s.status.State = owner, "ready"
			s.status.NeedsReconciliation = false
		case tracecontrol.OpClose:
			s.holder, s.status.State = "", "closed"
		}
	}
	status := s.status
	s.mu.Unlock()
	if execute != nil {
		return execute(ctx, owner, request)
	}
	return tracecontrol.Result{Status: status}, nil
}

func (s *testTraceService) Snapshot() tracecontrol.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *testTraceService) Invalidate(owner, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidations = append(s.invalidations, traceInvalidation{owner: owner, reason: reason})
	if s.holder == "" || (owner != "" && owner != s.holder) {
		return
	}
	s.status.State = "unknown"
	s.status.NeedsReconciliation = true
	s.status.LastReceipt = tracecontrol.Receipt{Outcome: "abandoned", Reason: reason, NativeStateUncertain: true}
}

func (s *testTraceService) recordedCalls() []traceServiceCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]traceServiceCall(nil), s.calls...)
}

func (s *testTraceService) invalidationCount(reason string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, invalidation := range s.invalidations {
		if invalidation.reason == reason {
			count++
		}
	}
	return count
}

func newTraceActor(t *testing.T, b *testBridge, service TraceService, deadline time.Duration) *Actor {
	t.Helper()
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(),
		PollInterval: time.Hour, RPCDeadline: deadline, BootstrapDeadline: time.Second,
		EventBuffer: 16, Trace: service,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func openTraceActor(t *testing.T, b *testBridge, service TraceService, deadline time.Duration) *Actor {
	t.Helper()
	a := newTraceActor(t, b, service, deadline)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(context.Background(), "frontend"); err != nil {
		t.Fatal(err)
	}
	drainCalls(b.calls)
	return a
}

func traceStatusRequest() tracecontrol.Request {
	return tracecontrol.Request{Op: tracecontrol.OpStatus, Handle: 1}
}

func traceOpenRequest() tracecontrol.Request {
	return tracecontrol.Request{
		Op: tracecontrol.OpOpen,
		Open: tracecontrol.OpenRequest{
			Source:                      timemachine.Source{Kind: "live", SessionID: 17, SourceID: "synthetic-session"},
			AcknowledgeUnverifiedSource: true,
		},
	}
}

func awaitTraceCall(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native trace runner did not start")
	}
}

func TestTraceAdmissionRejectsInvalidLifecycleOwnerHandleAndCore(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	a := newTraceActor(t, b, service, time.Second)
	if !a.TraceEnabled() {
		t.Fatal("configured native trace capability is absent")
	}
	if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("trace before open = %v", err)
	}
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); !errors.Is(err, session.ErrNotLeaseHolder) {
		t.Fatalf("trace without lease = %v", err)
	}
	if err := a.AcquireControl(context.Background(), "frontend"); err != nil {
		t.Fatal(err)
	}
	drainCalls(b.calls)
	if _, err := a.Trace(context.Background(), "other", traceStatusRequest()); !errors.Is(err, session.ErrNotLeaseHolder) {
		t.Fatalf("foreign trace owner = %v", err)
	}
	for _, request := range []tracecontrol.Request{
		{Op: tracecontrol.OpStatus},
		{Op: tracecontrol.OpStart, Handle: 1},
		{Op: tracecontrol.Operation("PRIVATE_UNKNOWN_OPERATION"), Handle: 1},
	} {
		if _, err := a.Trace(context.Background(), "frontend", request); !errors.Is(err, tracecontrol.ErrInvalidRequest) {
			t.Fatalf("invalid trace request = %v", err)
		}
	}
	stale := traceStatusRequest()
	stale.Handle = 2
	if _, err := a.Trace(context.Background(), "frontend", stale); !errors.Is(err, tracecontrol.ErrStaleHandle) {
		t.Fatalf("stale handle = %v", err)
	}
	unacknowledged := traceOpenRequest()
	unacknowledged.Open.AcknowledgeUnverifiedSource = false
	if _, err := a.Trace(context.Background(), "frontend", unacknowledged); !errors.Is(err, tracecontrol.ErrUnverifiedSource) {
		t.Fatalf("unacknowledged source = %v", err)
	}
	unknownCore := uint32(99)
	unconfigured := traceOpenRequest()
	unconfigured.Open.Source.CoreID = &unknownCore
	if _, err := a.Trace(context.Background(), "frontend", unconfigured); err == nil {
		t.Fatal("unconfigured core declaration was accepted")
	}
	if err := a.BeginConfiguration(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); err == nil {
		t.Fatal("configuration-window trace request was accepted")
	}
	if err := a.ConfigurationDone(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(service.recordedCalls()) != 0 || len(b.calls) != 0 {
		t.Fatal("rejected trace request performed native or bridge work")
	}
}

func TestTraceUnavailablePreservesOrdinaryActorBehavior(t *testing.T) {
	for _, service := range []TraceService{nil, &testTraceService{enabled: false}} {
		b := newTestBridge(t, stopped(1))
		a := openTraceActor(t, b, service, time.Second)
		if a.TraceEnabled() {
			t.Fatal("unavailable trace capability advertised")
		}
		if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); !errors.Is(err, tracecontrol.ErrUnavailable) {
			t.Fatalf("unavailable trace = %v", err)
		}
		if _, err := a.TraceSnapshot(context.Background(), "frontend"); !errors.Is(err, tracecontrol.ErrUnavailable) {
			t.Fatalf("unavailable trace snapshot = %v", err)
		}
		if err := a.Poll(context.Background()); err != nil {
			t.Fatalf("ordinary actor polling failed: %v", err)
		}
		a.Close()
	}
}

func TestTraceOnRunningTargetAddsNoTargetOperationsOrTransitions(t *testing.T) {
	b := newTestBridge(t, running(1))
	service := newTestTraceService()
	a := openTraceActor(t, b, service, time.Second)
	before, err := a.Snapshot(context.Background())
	if err != nil || before.State != session.TargetRunning {
		t.Fatalf("initial state = %#v, %v", before, err)
	}
	attachment, err := a.Attach(context.Background(), "frontend")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	for _, request := range []tracecontrol.Request{
		traceStatusRequest(),
		{Op: tracecontrol.OpRead, Handle: 1, Read: tracecontrol.ReadRequest{Generation: 1, Count: 1}},
		{Op: tracecontrol.OpStart, Handle: 1, Authorized: true},
		{Op: tracecontrol.OpStop, Handle: 1, Authorized: true},
	} {
		if _, err := a.Trace(context.Background(), "frontend", request); err != nil {
			t.Fatal(err)
		}
	}
	after, err := a.Snapshot(context.Background())
	if err != nil || after != before {
		t.Fatalf("trace changed target observation: %#v => %#v, %v", before, after, err)
	}
	if len(b.calls) != 0 || len(service.recordedCalls()) != 4 {
		t.Fatal("trace requests added or lost target/native operations")
	}
	select {
	case event := <-attachment.Events:
		t.Fatalf("trace invented a target event: %#v", event)
	default:
	}
}

func TestTraceDeadlineDoesNotPoisonOrBlockMBPExecutor(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	service.execute = func(context.Context, string, tracecontrol.Request) (tracecontrol.Result, error) {
		close(entered)
		<-release // Deliberately violates cancellation to exercise isolation.
		return tracecontrol.Result{Status: service.Snapshot()}, nil
	}
	a := openTraceActor(t, b, service, 80*time.Millisecond)
	generation := b.executor.Generation()
	result := make(chan error, 1)
	go func() {
		_, err := a.Trace(context.Background(), "frontend", traceStatusRequest())
		result <- err
	}()
	awaitTraceCall(t, entered)
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("trace deadline = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native deadline did not converge")
	}
	if err := a.Poll(context.Background()); err != nil {
		t.Fatalf("native timeout poisoned or blocked MBP: %v", err)
	}
	if b.executor.Generation() != generation {
		t.Fatal("native timeout changed MBP generation")
	}
	if snapshot, err := a.Snapshot(context.Background()); err != nil || snapshot.State != session.TargetStopped {
		t.Fatalf("native timeout faulted target state: %#v, %v", snapshot, err)
	}
	if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); !errors.Is(err, tracecontrol.ErrReconciliationRequired) {
		t.Fatalf("timed-out subsystem admitted another runner: %v", err)
	}
	if len(service.recordedCalls()) != 1 || service.invalidationCount("trace_timeout") != 1 {
		t.Fatal("native timeout did not fence its runner exactly once")
	}
	snapshot, err := a.TraceSnapshot(context.Background(), "frontend")
	if err != nil || !snapshot.NeedsReconciliation || snapshot.LastReceipt.Reason != "trace_timeout" {
		t.Fatalf("native timeout receipt unavailable: %#v, %v", snapshot, err)
	}
	releaseOnce()
}

func TestTraceOpenUsesBootstrapBudgetWithoutChangingOrdinaryRPCBudget(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	service.execute = func(ctx context.Context, _ string, _ tracecontrol.Request) (tracecontrol.Result, error) {
		select {
		case <-time.After(160 * time.Millisecond):
			return tracecontrol.Result{Status: service.Snapshot()}, nil
		case <-ctx.Done():
			return tracecontrol.Result{}, ctx.Err()
		}
	}
	a := openTraceActor(t, b, service, 80*time.Millisecond)
	if _, err := a.Trace(context.Background(), "frontend", traceOpenRequest()); err != nil {
		t.Fatalf("native open did not receive bootstrap budget: %v", err)
	}
	if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ordinary trace call ignored RPC budget: %v", err)
	}
}

func TestTraceSnapshotReportsActorLatchWhenProviderHasNoActiveHandle(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	service.holder = ""
	service.status.State = "idle"
	service.execute = func(ctx context.Context, _ string, _ tracecontrol.Request) (tracecontrol.Result, error) {
		<-ctx.Done()
		return tracecontrol.Result{}, ctx.Err()
	}
	a := openTraceActor(t, b, service, 40*time.Millisecond)
	_, err := a.Trace(context.Background(), "frontend", traceStatusRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("actor deadline = %v", err)
	}
	if service.Snapshot().NeedsReconciliation {
		t.Fatal("fixture unexpectedly supplied a native fault receipt")
	}
	snapshot, err := a.TraceSnapshot(context.Background(), "frontend")
	if err != nil || !snapshot.NeedsReconciliation || snapshot.State != "faulted" ||
		snapshot.LastReceipt.Reason != "trace_timeout" {
		t.Fatalf("actor-level admission failure was hidden: %#v, %v", snapshot, err)
	}
}

func TestDispatchedTraceCancellationInvalidatesOnlyNativeLifetime(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	entered := make(chan struct{})
	service.execute = func(ctx context.Context, _ string, _ tracecontrol.Request) (tracecontrol.Result, error) {
		close(entered)
		<-ctx.Done()
		return tracecontrol.Result{}, ctx.Err()
	}
	a := openTraceActor(t, b, service, time.Second)
	generation := b.executor.Generation()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reply := make(chan reply[tracecontrol.Result], 1)
	if err := a.send(context.Background(), traceCommand{
		ctx: ctx, owner: "frontend", request: traceStatusRequest(), reply: reply,
	}); err != nil {
		t.Fatal(err)
	}
	awaitTraceCall(t, entered)
	cancel()
	select {
	case result := <-reply:
		if !errors.Is(result.err, context.Canceled) || !result.value.Status.NeedsReconciliation {
			t.Fatalf("dispatched cancellation = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatched cancellation did not converge")
	}
	if service.invalidationCount("trace_cancelled") != 1 || b.executor.Generation() != generation {
		t.Fatal("native cancellation damaged MBP or repeated invalidation")
	}
	if err := a.Poll(context.Background()); err != nil {
		t.Fatalf("native cancellation poisoned target polling: %v", err)
	}
}

func TestQueuedTraceCancellationDoesNotInvalidateNativeOrBridge(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	a := openTraceActor(t, b, service, time.Second)
	b.blockMethod("state")
	polled := make(chan error, 1)
	go func() { polled <- a.Poll(context.Background()) }()
	waitCall(t, b.calls, "state")
	ctx, cancel := context.WithCancel(context.Background())
	reply := make(chan reply[tracecontrol.Result], 1)
	if err := a.send(context.Background(), traceCommand{ctx: ctx, owner: "frontend", request: traceStatusRequest(), reply: reply}); err != nil {
		t.Fatal(err)
	}
	// A later actor response establishes that the trace work was queued.
	if _, err := a.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel()
	b.unblock("state")
	if err := <-polled; err != nil {
		t.Fatal(err)
	}
	if result := <-reply; !errors.Is(result.err, context.Canceled) {
		t.Fatalf("queued native cancellation = %v", result.err)
	}
	if len(service.recordedCalls()) != 0 || service.invalidationCount("trace_cancelled") != 0 {
		t.Fatal("undispatched cancellation touched native state")
	}
	if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); err != nil {
		t.Fatalf("undispatched cancellation poisoned native state: %v", err)
	}
}

func TestQueuedTraceDoesNotSurviveReleaseAndReacquireOfSameOwner(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	a := openTraceActor(t, b, service, time.Second)
	if _, err := a.Trace(context.Background(), "frontend", tracecontrol.Request{Op: tracecontrol.OpClose, Handle: 1}); err != nil {
		t.Fatal(err)
	}
	b.blockMethod("state")
	polled := make(chan error, 1)
	go func() { polled <- a.Poll(context.Background()) }()
	waitCall(t, b.calls, "state")
	reply := make(chan reply[tracecontrol.Result], 1)
	if err := a.send(context.Background(), traceCommand{
		ctx: context.Background(), owner: "frontend", request: traceStatusRequest(), reply: reply,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.ReleaseControl(context.Background(), "frontend"); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(context.Background(), "frontend"); err != nil {
		t.Fatal(err)
	}
	b.unblock("state")
	if err := <-polled; err != nil {
		t.Fatal(err)
	}
	if result := <-reply; !errors.Is(result.err, tracecontrol.ErrStaleOperation) {
		t.Fatalf("old lease trace = %v", result.err)
	}
	if calls := service.recordedCalls(); len(calls) != 1 || calls[0].request.Op != tracecontrol.OpClose {
		t.Fatalf("old queued trace reached native service: %#v", calls)
	}
	if _, err := a.Trace(context.Background(), "frontend", traceOpenRequest()); err != nil {
		t.Fatalf("new lease could not open after explicit close: %v", err)
	}
}

func TestDetachFencesInflightTraceAndLeavesMBPUsable(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	service.execute = func(context.Context, string, tracecontrol.Request) (tracecontrol.Result, error) {
		close(entered)
		<-release
		return tracecontrol.Result{Batch: &timemachine.Capture{Source: timemachine.Source{SourceID: "PRIVATE_OLD_SOURCE"}}}, nil
	}
	a := openTraceActor(t, b, service, time.Second)
	result := make(chan reply[tracecontrol.Result], 1)
	go func() {
		value, err := a.Trace(context.Background(), "frontend", traceStatusRequest())
		result <- reply[tracecontrol.Result]{value: value, err: err}
	}()
	awaitTraceCall(t, entered)
	if err := a.Detach(context.Background(), "frontend"); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(context.Background(), "replacement"); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-result:
		if !errors.Is(response.err, tracecontrol.ErrStaleOperation) || response.value.Batch != nil {
			t.Fatalf("old trace completion escaped owner fence: %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("detach waited for the blocked native runner")
	}
	if err := a.Poll(context.Background()); err != nil {
		t.Fatalf("native detach blocked the new controller's MBP: %v", err)
	}
	if service.invalidationCount("frontend_detached") != 1 {
		t.Fatal("detach did not invalidate exactly once")
	}
	for _, call := range service.recordedCalls() {
		if call.request.Op == tracecontrol.OpStop || call.request.Op == tracecontrol.OpClose {
			t.Fatal("detach implicitly changed native collection or disconnected the API")
		}
	}
	releaseOnce()
}

func TestTraceSnapshotRequiresLeaseButDoesNoNativeWork(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	a := openTraceActor(t, b, service, time.Second)
	if _, err := a.TraceSnapshot(context.Background(), "other"); !errors.Is(err, session.ErrNotLeaseHolder) {
		t.Fatalf("foreign snapshot = %v", err)
	}
	if err := a.Detach(context.Background(), "observer"); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := a.TraceSnapshot(context.Background(), "frontend"); err != nil || snapshot.Handle != 1 || snapshot.NeedsReconciliation {
		t.Fatalf("local trace snapshot = %#v, %v", snapshot, err)
	}
	if len(service.recordedCalls()) != 0 || len(b.calls) != 0 {
		t.Fatal("local trace snapshot performed target/native work")
	}
	if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); err != nil {
		t.Fatalf("unrelated observer detach invalidated capture owner: %v", err)
	}
}

func TestTraceFaultAndShutdownInvalidateWithoutNativeControl(t *testing.T) {
	b := newTestBridge(t, stopped(1), stopped(0))
	service := newTestTraceService()
	a := openTraceActor(t, b, service, time.Second)
	if err := a.Poll(context.Background()); err == nil {
		t.Fatal("regressing target evidence did not fault")
	}
	if snapshot, err := a.TraceSnapshot(context.Background(), "frontend"); err != nil || !snapshot.NeedsReconciliation {
		t.Fatalf("fault receipt unavailable: %#v, %v", snapshot, err)
	}
	if _, err := a.Trace(context.Background(), "frontend", traceStatusRequest()); !errors.Is(err, ErrReconciliationNeeded) {
		t.Fatalf("faulted target accepted native operation: %v", err)
	}
	if service.invalidationCount("actor_faulted") != 1 {
		t.Fatal("target fault did not invalidate native lifetime once")
	}
	a.Close()
	a.Close()
	if service.invalidationCount("daemon_closed") != 1 || len(service.recordedCalls()) != 0 {
		t.Fatal("shutdown repeated cleanup or issued native control")
	}
}

func TestCloseDoesNotWaitForUncooperativeIsolatedTrace(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	service.execute = func(context.Context, string, tracecontrol.Request) (tracecontrol.Result, error) {
		close(entered)
		<-release
		return tracecontrol.Result{}, nil
	}
	a := openTraceActor(t, b, service, time.Second)
	result := make(chan error, 1)
	go func() {
		_, err := a.Trace(context.Background(), "frontend", traceStatusRequest())
		result <- err
	}()
	awaitTraceCall(t, entered)
	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Actor.Close waited for the isolated native runner")
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("trace during shutdown = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("trace caller was stranded by shutdown")
	}
	if service.invalidationCount("daemon_closed") != 1 {
		t.Fatal("shutdown failed to invalidate native lifetime")
	}
	releaseOnce()
}

func TestTraceBridgeGenerationFenceRejectsPrivateLateResult(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	service.execute = func(context.Context, string, tracecontrol.Request) (tracecontrol.Result, error) {
		close(entered)
		<-release
		return tracecontrol.Result{Batch: &timemachine.Capture{Source: timemachine.Source{SourceID: "PRIVATE_SOURCE"}}}, nil
	}
	a := openTraceActor(t, b, service, time.Second)
	result := make(chan reply[tracecontrol.Result], 1)
	go func() {
		value, err := a.Trace(context.Background(), "frontend", traceStatusRequest())
		result <- reply[tracecontrol.Result]{value: value, err: err}
	}()
	awaitTraceCall(t, entered)
	b.executor.Close()
	releaseOnce()
	response := <-result
	if !errors.Is(response.err, ErrBridgeStaleCompletion) || response.value.Batch != nil {
		t.Fatalf("generation-stale trace result = %#v", response)
	}
}

func TestTraceRequestOwnsDeclaredCoreDuringIsolatedWork(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	entered, release := make(chan struct{}), make(chan struct{})
	observedCore := make(chan uint32, 1)
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	service.execute = func(_ context.Context, _ string, request tracecontrol.Request) (tracecontrol.Result, error) {
		close(entered)
		<-release
		observedCore <- *request.Open.Source.CoreID
		return tracecontrol.Result{Status: service.Snapshot()}, nil
	}
	a := openTraceActor(t, b, service, time.Second)
	core := uint32(0)
	request := traceOpenRequest()
	request.Open.Source.CoreID = &core
	result := make(chan error, 1)
	go func() {
		_, err := a.Trace(context.Background(), "frontend", request)
		result <- err
	}()
	awaitTraceCall(t, entered)
	core = 99
	releaseOnce()
	if err := <-result; err != nil {
		t.Fatalf("caller mutation changed isolated request: %v", err)
	}
	if got := <-observedCore; got != 0 {
		t.Fatalf("isolated native request used caller-owned core %d", got)
	}
}

func TestTraceProviderFailureIsSanitizedAndPreservesCause(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	service := newTestTraceService()
	privateError := errors.New("PRIVATE_LIBRARY_PATH")
	service.execute = func(context.Context, string, tracecontrol.Request) (tracecontrol.Result, error) {
		return tracecontrol.Result{}, privateError
	}
	a := openTraceActor(t, b, service, time.Second)
	_, err := a.Trace(context.Background(), "frontend", traceStatusRequest())
	if err == nil || !errors.Is(err, privateError) || strings.Contains(err.Error(), "PRIVATE_LIBRARY_PATH") {
		t.Fatalf("native provider failure = %v", err)
	}
	if err := a.Poll(context.Background()); err != nil {
		t.Fatalf("ordinary native failure poisoned MBP: %v", err)
	}
}
