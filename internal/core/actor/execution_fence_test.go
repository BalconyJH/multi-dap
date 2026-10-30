package actor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

func fenceReply[T any](t *testing.T, replies <-chan reply[T]) reply[T] {
	t.Helper()
	select {
	case result := <-replies:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("queued request did not complete")
		return reply[T]{}
	}
}

func fenceBarrier(t *testing.T, a *Actor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := a.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
}

type fenceQueueStatusCommand struct{ reply chan reply[bool] }

func (c fenceQueueStatusCommand) apply(r *runtime) {
	respond(c.reply, r.inflight == nil && len(r.pending) == 0, nil)
}

func fenceQueueIdle(t *testing.T, a *Actor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		replies := make(chan reply[bool], 1)
		if err := a.send(ctx, fenceQueueStatusCommand{reply: replies}); err != nil {
			t.Fatal(err)
		}
		idle, err := await(ctx, a.done, replies, false)
		if err != nil {
			t.Fatal(err)
		}
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

var unknownExecutionStatuses = []multi.Status{
	multi.StatusNil, multi.StatusNoProcess, multi.StatusDying, multi.StatusForking, multi.StatusZombie,
}

func fenceState(status multi.Status) map[string]any {
	return map[string]any{"status": int(status), "process_info": map[string]string{"stopStamp": "1", "pid": "1234"}}
}

func TestQueuedExecutionCannotOutliveControlLease(t *testing.T) {
	for _, operation := range []multi.ExecutionOperation{
		multi.ExecutionContinue, multi.ExecutionPause, multi.ExecutionStepIn, multi.ExecutionNext,
	} {
		for _, transition := range []string{"detach", "release", "release-reacquire"} {
			t.Run(operation.String()+"/"+transition, func(t *testing.T) {
				b := newTestBridge(t, stopped(1))
				a := newSingleCoreActor(t, b, nil)
				ctx := context.Background()
				if err := a.Open(ctx); err != nil {
					t.Fatal(err)
				}
				if err := a.AcquireControl(ctx, "frontend"); err != nil {
					t.Fatal(err)
				}
				drainCalls(b.calls)
				b.blockMethod("state")
				defer b.unblock("state")
				polled := make(chan error, 1)
				go func() { polled <- a.Poll(ctx) }()
				waitCall(t, b.calls, "state")
				replies := make(chan reply[ExecutionResult], 1)
				if err := a.send(ctx, executionCommand{owner: "frontend", request: ExecutionRequest{CoreID: 0, Operation: operation}, reply: replies}); err != nil {
					t.Fatal(err)
				}
				fenceBarrier(t, a)
				if transition == "detach" {
					if err := a.Detach(ctx, "frontend"); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := a.ReleaseControl(ctx, "frontend"); err != nil {
						t.Fatal(err)
					}
					if transition == "release-reacquire" {
						if err := a.AcquireControl(ctx, "frontend"); err != nil {
							t.Fatal(err)
						}
					}
				}
				b.unblock("state")
				if err := <-polled; err != nil {
					t.Fatal(err)
				}
				if got := fenceReply(t, replies); !errors.Is(got.err, session.ErrNotLeaseHolder) {
					t.Fatalf("stale execution = %v", got.err)
				}
				// The completed stopped sample may read H; no execution may follow.
				for len(b.calls) != 0 {
					if method := <-b.calls; method != bridgeCommandMethod {
						t.Fatalf("stale execution reached bridge: %q", method)
					}
				}
			})
		}
	}
}

func TestExecutionDispatchRechecksResumeGateForBothPaths(t *testing.T) {
	for _, operation := range []multi.ExecutionOperation{
		multi.ExecutionContinue, multi.ExecutionPause, multi.ExecutionStepIn, multi.ExecutionNext,
	} {
		t.Run(operation.String(), func(t *testing.T) {
			r := &runtime{reducer: session.New(session.Options{RequireResetAfterDownload: true})}
			if err := r.reducer.AcquireControl("frontend"); err != nil {
				t.Fatal(err)
			}
			works := []rpcWork{
				r.singleCoreExecutionWork("frontend", ExecutionRequest{CoreID: 0, Operation: operation}, nil),
				r.executionDomainWork("frontend", multi.ExecutionRequest{Operation: operation}, nil),
			}
			r.reducer.NoteDownload()
			for _, work := range works {
				err := work.validate(r)
				if operation == multi.ExecutionPause {
					if err != nil {
						t.Fatalf("pause must remain admitted for recovery: %v", err)
					}
				} else if !errors.Is(err, session.ErrResetRequired) {
					t.Fatalf("resume gate was not rechecked: %v", err)
				}
			}
			r.reducer.NoteReset()
			for _, work := range works {
				if err := work.validate(r); err != nil {
					t.Fatalf("confirmed reset did not restore resume admission: %v", err)
				}
			}
			r.controlGeneration++
			for _, work := range works {
				if err := work.validate(r); !errors.Is(err, session.ErrNotLeaseHolder) {
					t.Fatalf("execution path missed the lease generation: %v", err)
				}
			}
		})
	}
}

func newFenceBreakpointActor(t *testing.T, b *testBridge, runner BreakpointRunner, index SourceIndex, resolver SourceResolver) *Actor {
	t.Helper()
	b.setComponents("fixture.component.5 (debugger.name.core0)\n")
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs()[:1],
		PollInterval: time.Hour, RPCDeadline: time.Second,
		BreakpointRunner: runner, SourceIndex: index, SourceResolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(context.Background(), "frontend"); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBreakpointMutationRequiresStoppedStateAndCurrentLeaseAtDispatch(t *testing.T) {
	for _, resolve := range []bool{false, true} {
		for _, transition := range []string{"running", "new-stop", "release-reacquire"} {
			name := transition
			if resolve {
				name += "/resolver"
			}
			t.Run(name, func(t *testing.T) {
				next := stopped(1)
				if transition == "running" {
					next = running(1)
				} else if transition == "new-stop" {
					next = stopped(2)
				}
				b := newTestBridge(t, stopped(1), next)
				runner := &testBreakpointRunner{}
				resolver := &testSourceResolver{observations: []source.Observation{{Core: 0, Presence: source.PresencePresent}}}
				var index SourceIndex = testSourceIndex{"src/main.c": {0}}
				if resolve {
					index = testSourceCoverage{{Core: 0, Presence: source.PresenceUnknown}}
				}
				a := newFenceBreakpointActor(t, b, runner, index, resolver)
				ctx := context.Background()
				drainCalls(b.calls)
				b.blockMethod("state")
				defer b.unblock("state")
				polled := make(chan error, 1)
				go func() { polled <- a.Poll(ctx) }()
				waitCall(t, b.calls, "state")
				replies := make(chan reply[breakpoint.ReplaceResult], 1)
				if err := a.send(ctx, setBreakpointsCommand{owner: "frontend", identity: source.Identity{Key: "src/main.c"}, lines: []int{18}, reply: replies}); err != nil {
					t.Fatal(err)
				}
				fenceBarrier(t, a)
				want := ErrInspectionStale
				if transition == "release-reacquire" {
					if err := a.ReleaseControl(ctx, "frontend"); err != nil {
						t.Fatal(err)
					}
					if err := a.AcquireControl(ctx, "frontend"); err != nil {
						t.Fatal(err)
					}
					want = session.ErrNotLeaseHolder
				}
				b.unblock("state")
				if err := <-polled; err != nil {
					t.Fatal(err)
				}
				if got := fenceReply(t, replies); !errors.Is(got.err, want) {
					t.Fatalf("stale replacement = %v, want %v", got.err, want)
				}
				if resolver.calls != 0 || len(runner.requests) != 0 {
					t.Fatalf("stale mutation reached resolver/placer: %d/%d", resolver.calls, len(runner.requests))
				}
				if transition == "running" {
					for _, lines := range [][]int{nil, {19}} {
						if _, err := a.SetBreakpoints(ctx, "frontend", source.Identity{Key: "src/main.c"}, lines); !errors.Is(err, ErrBreakpointsNotStopped) {
							t.Fatalf("running replacement = %v", err)
						}
					}
				}
			})
		}
	}
}

func TestQueuedBreakpointCleanupWaitsForNextNaturalStop(t *testing.T) {
	b := newTestBridge(t, stopped(1), running(1), stopped(2))
	runner := &lifecycleBreakpointRunner{cleared: make(chan breakpoint.Physical, 4)}
	a := newFenceBreakpointActor(t, b, runner, testSourceIndex{"src/main.c": {0}}, nil)
	ctx := context.Background()
	if _, err := a.SetBreakpoints(ctx, "frontend", source.Identity{Key: "src/main.c"}, []int{18}); err != nil {
		t.Fatal(err)
	}
	drainCalls(b.calls)
	b.blockMethod("state")
	defer b.unblock("state")
	polled := make(chan error, 1)
	go func() { polled <- a.Poll(ctx) }()
	waitCall(t, b.calls, "state")
	if err := a.Detach(ctx, "frontend"); err != nil {
		t.Fatal(err)
	}
	b.unblock("state")
	if err := <-polled; err != nil {
		t.Fatal(err)
	}
	fenceBarrier(t, a)
	select {
	case <-runner.cleared:
		t.Fatal("queued cleanup cleared while running")
	default:
	}
	if counts, err := a.BreakpointCounts(ctx); err != nil || counts.Pending != 1 {
		t.Fatalf("deferred cleanup = %#v, %v", counts, err)
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.cleared:
	case <-time.After(time.Second):
		t.Fatal("new stopped epoch did not clear pending record")
	}
}

func TestBreakpointCleanupRefreshesObservationAfterContinue(t *testing.T) {
	b := newTestBridge(t, stopped(1), running(1), stopped(2))
	runner := &lifecycleBreakpointRunner{cleared: make(chan breakpoint.Physical, 4)}
	a := newFenceBreakpointActor(t, b, runner, testSourceIndex{"src/main.c": {0}}, nil)
	ctx := context.Background()
	if _, err := a.SetBreakpoints(ctx, "frontend", source.Identity{Key: "src/main.c"}, []int{18}); err != nil {
		t.Fatal(err)
	}
	observer, err := a.Attach(ctx, "observer")
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	if err := a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue}); err != nil {
		t.Fatal(err)
	}
	drainCalls(b.calls)
	b.blockMethod("state")
	defer b.unblock("state")
	if err := a.Detach(ctx, "frontend"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, b.calls, "state")
	select {
	case <-runner.cleared:
		t.Fatal("cleanup ran before post-execution state observation")
	default:
	}
	b.unblock("state")
	assertEvent(t, observer.Events, EventInvalidated)
	assertEvent(t, observer.Events, EventResumed)
	fenceBarrier(t, a)
	select {
	case <-runner.cleared:
		t.Fatal("post-execution running observation allowed cleanup")
	default:
	}
	if counts, err := a.BreakpointCounts(ctx); err != nil || counts.Pending != 1 {
		t.Fatalf("post-execution cleanup lost its pending record: %#v, %v", counts, err)
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.cleared:
	case <-time.After(time.Second):
		t.Fatal("pending cleanup did not recover at next stop")
	}
}

func TestEmptyBreakpointReplacementRefreshesObservationAfterContinue(t *testing.T) {
	for _, status := range append([]multi.Status{multi.StatusRunning}, unknownExecutionStatuses...) {
		t.Run(status.String(), func(t *testing.T) {
			b := newTestBridge(t, stopped(1), fenceState(status))
			runner := &lifecycleBreakpointRunner{cleared: make(chan breakpoint.Physical, 4)}
			a := newFenceBreakpointActor(t, b, runner, testSourceIndex{"src/main.c": {0}}, nil)
			ctx := context.Background()
			if _, err := a.SetBreakpoints(ctx, "frontend", source.Identity{Key: "src/main.c"}, []int{18}); err != nil {
				t.Fatal(err)
			}
			if err := a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue}); err != nil {
				t.Fatal(err)
			}
			if _, err := a.SetBreakpoints(ctx, "frontend", source.Identity{Key: "src/main.c"}, nil); !errors.Is(err, ErrInspectionStale) {
				t.Fatalf("post-execution clear = %v", err)
			}
			select {
			case <-runner.cleared:
				t.Fatal("replacement cleared a breakpoint after resume")
			default:
			}
			if counts, err := a.BreakpointCounts(ctx); err != nil || counts.DAPOwned != 1 {
				t.Fatalf("rejected replacement lost ownership: %#v, %v", counts, err)
			}
		})
	}
}

func TestUnknownPostExecutionStateCannotRetryBreakpointCleanup(t *testing.T) {
	for _, status := range unknownExecutionStatuses {
		t.Run(status.String(), func(t *testing.T) {
			b := newTestBridge(t, stopped(1), fenceState(status), stopped(2))
			runner := &lifecycleBreakpointRunner{cleared: make(chan breakpoint.Physical, 4)}
			a := newFenceBreakpointActor(t, b, runner, testSourceIndex{"src/main.c": {0}}, nil)
			ctx := context.Background()
			if _, err := a.SetBreakpoints(ctx, "frontend", source.Identity{Key: "src/main.c"}, []int{18}); err != nil {
				t.Fatal(err)
			}
			if err := a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue}); err != nil {
				t.Fatal(err)
			}
			drainCalls(b.calls)
			if err := a.Detach(ctx, "frontend"); err != nil {
				t.Fatal(err)
			}
			fenceQueueIdle(t, a)
			select {
			case <-runner.cleared:
				t.Fatal("unknown lifecycle reused the retained stopped state for cleanup")
			default:
			}
			states := 0
			for len(b.calls) != 0 {
				if method := <-b.calls; method == "state" {
					states++
				} else {
					t.Fatalf("unknown state issued extra target I/O: %s", method)
				}
			}
			if states != 1 {
				t.Fatalf("unknown state triggered %d automatic refresh attempts, want one", states)
			}
			if counts, err := a.BreakpointCounts(ctx); err != nil || counts.Pending != 1 {
				t.Fatalf("unknown lifecycle lost pending cleanup: %#v, %v", counts, err)
			}
			if err := a.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-runner.cleared:
			case <-time.After(time.Second):
				t.Fatal("definitive natural stop did not recover pending cleanup")
			}
		})
	}
}

func TestInspectionAfterExecutionRequiresFreshObservation(t *testing.T) {
	cases := []struct {
		name   string
		status multi.Status
	}{
		{"running", multi.StatusRunning},
		{"refused", multi.StatusNil},
		{"same-stop", multi.StatusStopped},
	}
	for _, status := range unknownExecutionStatuses {
		cases = append(cases, struct {
			name   string
			status multi.Status
		}{status.String(), status})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			b := newTestBridge(t, stopped(1))
			runner := &testInspectionRunner{}
			b.setComponents("fixture.component.5 (debugger.name.core0)\n")
			a, err := New(Options{
				Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs()[:1],
				PollInterval: time.Hour, RPCDeadline: time.Second,
				Inspection: runner, SourceMap: testSourceMapper{},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Close)
			ctx := context.Background()
			if err := a.Open(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.AcquireControl(ctx, "frontend"); err != nil {
				t.Fatal(err)
			}
			if err := a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue}); err != nil {
				t.Fatal(err)
			}
			if test.name == "refused" {
				b.failMethod("state", "multi_refused", "fixture refusal")
			} else {
				b.appendStates(fenceState(test.status))
			}
			drainCalls(b.calls)
			replies := make(chan reply[inspection.Stack], 1)
			if err := a.send(ctx, stackCommand{threadID: 1, page: inspection.Page{Count: 4}, reply: replies}); err != nil {
				t.Fatal(err)
			}
			got := fenceReply(t, replies)
			if test.name == "same-stop" {
				if got.err != nil || runner.stackCalls != 1 {
					t.Fatalf("fresh stopped inspection = %#v, calls %d", got, runner.stackCalls)
				}
			} else if got.err == nil || runner.stackCalls != 0 {
				t.Fatalf("unconfirmed inspection = %#v, calls %d", got, runner.stackCalls)
			}
			states := 0
			for len(b.calls) != 0 {
				if <-b.calls == "state" {
					states++
				}
			}
			if states != 1 {
				t.Fatalf("state refresh attempts = %d, want exactly one", states)
			}
		})
	}
}
