package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/core/stop"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

type fakeCore struct {
	mu sync.Mutex

	threads    []actor.Thread
	snapshot   session.Snapshot
	attachErr  error
	leaseErr   error
	threadErr  error
	executeErr error
	beginErr   error
	doneErr    error
	cancelErr  error
	breakErr   error
	stackErr   error
	scopesErr  error
	varsErr    error
	evalErr    error
	resetErr   error

	breakResult     breakpoint.ReplaceResult
	stackResult     inspection.Stack
	stackResults    map[int]inspection.Stack
	scopesResult    []inspection.Scope
	varsResult      inspection.VariablePage
	evalResult      inspection.Variable
	breakOwner      session.ControllerID
	breakIdentity   source.Identity
	breakLines      []int
	stackThread     int
	stackPage       inspection.Page
	stackCalls      []stackCall
	varsReference   int32
	varsPage        inspection.Page
	varsFormat      inspection.Format
	evalFrame       int32
	evalExpression  inspection.Expression
	evalFormat      inspection.Format
	executionResult actor.ExecutionResult

	attachments  map[session.ControllerID]chan actor.Event
	attachOwners []session.ControllerID
	detached     map[session.ControllerID]int
	executions   []executionCall
	begun        int
	completed    int
	cancelled    int
	resetCalls   int
	configOrder  []string
}

type executionCall struct {
	owner   session.ControllerID
	request actor.ExecutionRequest
}

type stackCall struct {
	threadID int
	page     inspection.Page
}

func newFakeCore() *fakeCore {
	return &fakeCore{
		threads:     []actor.Thread{{ID: 1, CoreID: 0, Name: "core0"}, {ID: 2, CoreID: 1, Name: "core1"}},
		snapshot:    session.Snapshot{State: session.TargetStopped, StopEpoch: 7},
		attachments: make(map[session.ControllerID]chan actor.Event),
		detached:    make(map[session.ControllerID]int),
	}
}

func (c *fakeCore) Attach(_ context.Context, owner session.ControllerID) (actor.Attachment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attachErr != nil {
		return actor.Attachment{}, c.attachErr
	}
	events := make(chan actor.Event, 8)
	c.attachments[owner] = events
	c.attachOwners = append(c.attachOwners, owner)
	return actor.Attachment{Snapshot: c.snapshot, Generation: uint64(len(c.attachOwners)), Events: events}, nil
}

func (c *fakeCore) Detach(_ context.Context, owner session.ControllerID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detached[owner]++
	if events, ok := c.attachments[owner]; ok {
		close(events)
		delete(c.attachments, owner)
	}
	return nil
}

func (c *fakeCore) AcquireControl(_ context.Context, _ session.ControllerID) error { return c.leaseErr }
func (c *fakeCore) BeginConfiguration(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.begun++
	return c.beginErr
}
func (c *fakeCore) ConfigurationDone(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completed++
	c.configOrder = append(c.configOrder, "configurationDone")
	return c.doneErr
}
func (c *fakeCore) ResetConsole(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetCalls++
	c.configOrder = append(c.configOrder, "resetConsole")
	return c.resetErr
}
func (c *fakeCore) CancelConfiguration(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled++
	return c.cancelErr
}
func (c *fakeCore) Threads(_ context.Context) ([]actor.Thread, error) {
	if c.threadErr != nil {
		return nil, c.threadErr
	}
	return append([]actor.Thread(nil), c.threads...), nil
}
func (c *fakeCore) SetBreakpoints(_ context.Context, owner session.ControllerID, identity source.Identity, lines []int) (breakpoint.ReplaceResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.breakOwner, c.breakIdentity, c.breakLines = owner, identity, append([]int(nil), lines...)
	return c.breakResult, c.breakErr
}
func (c *fakeCore) Stack(_ context.Context, threadID int, page inspection.Page) (inspection.Stack, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stackThread, c.stackPage = threadID, page
	c.stackCalls = append(c.stackCalls, stackCall{threadID: threadID, page: page})
	if result, ok := c.stackResults[threadID]; ok {
		return result, c.stackErr
	}
	return c.stackResult, c.stackErr
}
func (c *fakeCore) Scopes(_ context.Context, _ int32) ([]inspection.Scope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]inspection.Scope(nil), c.scopesResult...), c.scopesErr
}
func (c *fakeCore) Variables(_ context.Context, reference int32, page inspection.Page, format inspection.Format) (inspection.VariablePage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.varsReference, c.varsPage, c.varsFormat = reference, page, format
	return c.varsResult, c.varsErr
}
func (c *fakeCore) Evaluate(_ context.Context, frameID int32, expression inspection.Expression, format inspection.Format) (inspection.Variable, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evalFrame, c.evalExpression, c.evalFormat = frameID, expression, format
	return c.evalResult, c.evalErr
}
func (c *fakeCore) Execute(_ context.Context, owner session.ControllerID, request actor.ExecutionRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.executions = append(c.executions, executionCall{owner: owner, request: request})
	return c.executeErr
}

func (c *fakeCore) ExecuteResult(_ context.Context, owner session.ControllerID, request actor.ExecutionRequest) (actor.ExecutionResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.executions = append(c.executions, executionCall{owner: owner, request: request})
	return c.executionResult, c.executeErr
}

func (c *fakeCore) emit(owner session.ControllerID, event actor.Event) {
	c.mu.Lock()
	events := c.attachments[owner]
	c.mu.Unlock()
	if events != nil {
		events <- event
	}
}

func (c *fakeCore) closeEvents(owner session.ControllerID) {
	c.mu.Lock()
	events := c.attachments[owner]
	if events != nil {
		close(events)
		delete(c.attachments, owner)
	}
	c.mu.Unlock()
}

type fakePublisher struct {
	mu         sync.Mutex
	states     []dap.TargetState
	outputs    []dap.OutputBody
	terminated int
	accept     bool
}

func (p *fakePublisher) PublishTargetState(state dap.TargetState) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.states = append(p.states, state)
	return p.accept
}
func (p *fakePublisher) PublishOutput(output dap.OutputBody) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, output)
	return p.accept
}
func (p *fakePublisher) Terminate() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminated++
	return p.accept
}
func (p *fakePublisher) stateCount() int       { p.mu.Lock(); defer p.mu.Unlock(); return len(p.states) }
func (p *fakePublisher) terminationCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.terminated }
func (p *fakePublisher) outputSnapshot() []dap.OutputBody {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]dap.OutputBody(nil), p.outputs...)
}

func boundBackend(t *testing.T) (*Backend, *fakeCore, *fakePublisher) {
	t.Helper()
	core := newFakeCore()
	backend, err := NewBackend(core)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{accept: true}
	if err := backend.BindPublisher(publisher); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	return backend, core, publisher
}

func attach(t *testing.T, backend *Backend) dap.ActionResult {
	t.Helper()
	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionAttach})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionConfigurationDone}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAttachRequiresBoundPublisherAndRollsBackLeaseFailure(t *testing.T) {
	core := newFakeCore()
	backend, err := NewBackend(core)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionAttach}); !errors.Is(err, ErrPublisherNotBound) {
		t.Fatalf("unbound attach error = %v", err)
	}
	if err := backend.BindPublisher(&fakePublisher{accept: true}); err != nil {
		t.Fatal(err)
	}
	core.leaseErr = errors.New("held")
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionAttach}); err == nil {
		t.Fatal("attach unexpectedly acquired lease")
	}
	core.mu.Lock()
	owner := core.attachOwners[0]
	detached := core.detached[owner]
	core.mu.Unlock()
	if detached != 1 {
		t.Fatalf("rollback detach calls = %d, want 1", detached)
	}
}

func TestAttachSnapshotThreadMapAndLease(t *testing.T) {
	backend, core, _ := boundBackend(t)
	result := attach(t, backend)
	if len(result.Threads) != 2 || result.Threads[0].ID != 1 || result.Threads[1].ID != 2 {
		t.Fatalf("threads = %#v", result.Threads)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionAttach}); !errors.Is(err, ErrFrontendActive) {
		t.Fatalf("second attach error = %v", err)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionDisconnect}); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	owner := core.attachOwners[0]
	detached := core.detached[owner]
	core.mu.Unlock()
	if detached != 1 {
		t.Fatalf("detach calls = %d", detached)
	}
}

func TestSingleThreadResumeAndHaltReachActor(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.threads = core.threads[:1]
	attach(t, backend)
	threads, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionThreads})
	if err != nil || len(threads.Threads) != 1 || threads.Threads[0].ID != 1 {
		t.Fatalf("threads result = %#v, %v", threads, err)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionContinue, Continue: dap.ContinueArguments{ThreadID: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionPause, Pause: dap.PauseArguments{ThreadID: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionContinue, Continue: dap.ContinueArguments{ThreadID: 99}}); err == nil {
		t.Fatal("unknown thread was accepted")
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.executions) != 2 || core.executions[0].request != (actor.ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue}) || core.executions[1].request != (actor.ExecutionRequest{CoreID: 0, Operation: multi.ExecutionPause}) || core.executions[0].owner != core.executions[1].owner {
		t.Fatalf("control calls = %#v", core.executions)
	}
}

func TestStepActionsUseStableThreadAndKeepStepOutUnsupported(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.threads = core.threads[:1]
	attach(t, backend)
	for _, test := range []struct {
		name   string
		action dap.Action
	}{
		{name: "step in", action: dap.Action{Kind: dap.ActionStepIn, StepIn: dap.StepArguments{ThreadID: 1}}},
		{name: "next", action: dap.Action{Kind: dap.ActionNext, Next: dap.StepArguments{ThreadID: 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := backend.Execute(context.Background(), test.action); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionStepIn, StepIn: dap.StepArguments{ThreadID: 99}}); err == nil {
		t.Fatal("unknown thread was accepted for step in")
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionStepOut, StepOut: dap.StepArguments{ThreadID: 1}}); err == nil {
		t.Fatal("step out was accepted")
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.executions) != 2 || core.executions[0].request != (actor.ExecutionRequest{CoreID: 0, Operation: multi.ExecutionStepIn}) || core.executions[1].request != (actor.ExecutionRequest{CoreID: 0, Operation: multi.ExecutionNext}) || core.executions[0].owner != core.executions[1].owner {
		t.Fatalf("step calls = %#v", core.executions)
	}
}

func TestMulticoreExecutionActionsPreserveConfiguredCoreToActor(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.threads = []actor.Thread{{ID: 1, CoreID: 0, Name: "core0"}, {ID: 5, CoreID: 4, Name: "core4"}}
	core.executeErr = multi.ErrExecutionDomainUnavailable
	attach(t, backend)
	for _, action := range []dap.Action{
		{Kind: dap.ActionContinue, Continue: dap.ContinueArguments{ThreadID: 5}},
		{Kind: dap.ActionPause, Pause: dap.PauseArguments{ThreadID: 1}},
		{Kind: dap.ActionStepIn, StepIn: dap.StepArguments{ThreadID: 5}},
		{Kind: dap.ActionNext, Next: dap.StepArguments{ThreadID: 1}},
	} {
		if _, err := backend.Execute(context.Background(), action); !errors.Is(err, multi.ErrExecutionDomainUnavailable) {
			t.Fatalf("%v error = %v", action.Kind, err)
		}
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	want := []actor.ExecutionRequest{
		{CoreID: 4, Operation: multi.ExecutionContinue},
		{CoreID: 0, Operation: multi.ExecutionPause},
		{CoreID: 4, Operation: multi.ExecutionStepIn},
		{CoreID: 0, Operation: multi.ExecutionNext},
	}
	if len(core.executions) != len(want) {
		t.Fatalf("execution calls = %#v", core.executions)
	}
	for index, request := range want {
		if core.executions[index].request != request {
			t.Fatalf("execution call %d = %#v, want %#v", index, core.executions[index].request, request)
		}
	}
}

func TestExecutionResultPreservesVerifiedTruthWithoutInferringScope(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.threads = []actor.Thread{{ID: 1, CoreID: 0, Name: "core0"}, {ID: 5, CoreID: 4, Name: "core4"}}
	core.executionResult = actor.ExecutionResult{
		Cores: []uint64{0, 4},
		Truth: multi.ExecutionTruth{AllThreadsContinued: true},
	}
	attach(t, backend)

	result, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionContinue, Continue: dap.ContinueArguments{ThreadID: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Execution.AllThreadsContinued || result.Execution.AllThreadsStopped {
		t.Fatalf("execution result = %#v", result.Execution)
	}
}

func TestRejectedMulticoreExecutionPublishesNoSyntheticTargetState(t *testing.T) {
	backend, core, publisher := boundBackend(t)
	core.threads = []actor.Thread{{ID: 1, CoreID: 0, Name: "core0"}, {ID: 5, CoreID: 4, Name: "core4"}}
	core.executeErr = multi.ErrExecutionDomainUnavailable
	attach(t, backend)

	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionContinue, Continue: dap.ContinueArguments{ThreadID: 5}}); !errors.Is(err, multi.ErrExecutionDomainUnavailable) {
		t.Fatalf("continue error = %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if publisher.stateCount() != 0 {
		t.Fatalf("rejected command published target state: %#v", publisher.states)
	}
}

func TestAttachRejectsUnrepresentableActorThreadID(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.threads = []actor.Thread{{ID: int(^uint32(0)), Name: "too-large"}}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionAttach}); err == nil {
		t.Fatal("unrepresentable thread ID was accepted")
	}
	core.mu.Lock()
	owner := core.attachOwners[0]
	detached := core.detached[owner]
	core.mu.Unlock()
	if detached != 1 {
		t.Fatalf("failed attach did not release lease/subscription: %d", detached)
	}
}

func TestActorEventsFollowSnapshotAndMapTargetStates(t *testing.T) {
	backend, core, publisher := boundBackend(t)
	attach(t, backend)
	core.mu.Lock()
	owner := core.attachOwners[0]
	core.mu.Unlock()
	core.emit(owner, actor.Event{Kind: actor.EventResumed, ThreadID: 2, Snapshot: session.Snapshot{State: session.TargetRunning}})
	core.emit(owner, actor.Event{
		Kind: actor.EventStopped, ThreadID: 1, Snapshot: session.Snapshot{State: session.TargetStopped, StopEpoch: 8},
		Reason: stop.ReasonBreakpoint, HitBreakpointIDs: []int{7, 8},
	})
	waitFor(t, func() bool { return publisher.stateCount() == 2 })
	publisher.mu.Lock()
	states := append([]dap.TargetState(nil), publisher.states...)
	publisher.mu.Unlock()
	if states[0].Execution != dap.TargetRunning || states[0].Continued.ThreadID != 2 || states[1].Execution != dap.TargetStopped || states[1].Stopped.ThreadID != 1 || states[1].Stopped.Reason != "breakpoint" || len(states[1].Stopped.HitBreakpointIDs) != 2 || states[1].Stopped.HitBreakpointIDs[0] != 7 || states[1].Stopped.HitBreakpointIDs[1] != 8 {
		t.Fatalf("published states = %#v", states)
	}
}

func TestActorConsoleOutputMapsMULTIPanesToDAPCategories(t *testing.T) {
	backend, core, publisher := boundBackend(t)
	attach(t, backend)
	core.mu.Lock()
	owner := core.attachOwners[0]
	core.mu.Unlock()
	core.emit(owner, actor.Event{Kind: actor.EventOutput, Console: multi.ConsoleOutput{Server: "target\n", IO: "uart\n", RawLossy: true, Truncated: true}})
	waitFor(t, func() bool { return len(publisher.outputSnapshot()) == 4 })
	got := publisher.outputSnapshot()
	want := []dap.OutputBody{
		{Category: "stdout", Output: "target\n"},
		{Category: "stdout", Output: "uart\n"},
		{Category: "stderr", Output: consoleLossyNotice},
		{Category: "stderr", Output: consoleTruncatedNotice},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %#v, want %#v", got, want)
	}
}

func TestConsoleResetRunsBeforeConfigurationDoneOncePerAttach(t *testing.T) {
	backend, core, _ := boundBackend(t)
	attach(t, backend)
	core.mu.Lock()
	if got := append([]string(nil), core.configOrder...); !reflect.DeepEqual(got, []string{"resetConsole", "configurationDone"}) {
		core.mu.Unlock()
		t.Fatalf("first configuration order = %#v", got)
	}
	core.mu.Unlock()
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionConfigurationDone}); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	if core.resetCalls != 1 {
		core.mu.Unlock()
		t.Fatalf("same attach reset calls = %d, want 1", core.resetCalls)
	}
	core.mu.Unlock()
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionDisconnect}); err != nil {
		t.Fatal(err)
	}
	attach(t, backend)
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.resetCalls != 2 {
		t.Fatalf("reattach reset calls = %d, want 2", core.resetCalls)
	}
}

func TestConsoleResetFailureDoesNotBlockConfigurationDone(t *testing.T) {
	backend, core, _ := boundBackend(t)
	core.resetErr = errors.New("bridge unavailable")
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionAttach}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionConfigurationDone}); err != nil {
		t.Fatalf("configurationDone blocked by optional reset: %v", err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.resetCalls != 1 || core.completed != 1 {
		t.Fatalf("reset=%d configured=%d, want 1/1", core.resetCalls, core.completed)
	}
}

func TestStaleFrontendConsoleOutputIsNotPublished(t *testing.T) {
	backend, _, publisher := boundBackend(t)
	attach(t, backend)
	backend.mu.Lock()
	stale := backend.active
	backend.mu.Unlock()
	backend.releaseActive()
	attach(t, backend)
	if got := backend.deliverOutput(stale, actor.Event{Kind: actor.EventOutput, Console: multi.ConsoleOutput{Server: "stale"}}); got != deliveryStale {
		t.Fatalf("stale deliver result = %v", got)
	}
	if got := publisher.outputSnapshot(); len(got) != 0 {
		t.Fatalf("stale frontend published output %#v", got)
	}
}

func TestConsoleCollectionFailurePublishesOneStableNotice(t *testing.T) {
	backend, core, publisher := boundBackend(t)
	attach(t, backend)
	core.mu.Lock()
	owner := core.attachOwners[0]
	core.mu.Unlock()
	core.emit(owner, actor.Event{Kind: actor.EventOutput, ConsoleUnavailable: true})
	core.emit(owner, actor.Event{Kind: actor.EventOutput, ConsoleUnavailable: true})
	waitFor(t, func() bool { return len(publisher.outputSnapshot()) == 2 })
	// The actor owns one-shot suppression; backend preserves every received
	// event so an independently generated warning is never silently dropped.
	got := publisher.outputSnapshot()
	for _, output := range got {
		if output != (dap.OutputBody{Category: "stderr", Output: consoleUnavailableNotice}) {
			t.Fatalf("notice = %#v", output)
		}
	}
}

func TestStoppedStateIsConservativeWithoutActorEvidence(t *testing.T) {
	state := targetState(actor.Event{Kind: actor.EventStopped, Snapshot: session.Snapshot{State: session.TargetStopped}}, 1)
	if state.Stopped.Reason != "unknown" || state.Stopped.ThreadID != 0 || len(state.Stopped.HitBreakpointIDs) != 0 {
		t.Fatalf("conservative stopped state = %#v", state)
	}
}

func TestTargetStateDoesNotClaimAllThreads(t *testing.T) {
	running := targetState(actor.Event{Kind: actor.EventResumed, Snapshot: session.Snapshot{State: session.TargetRunning}, ThreadID: 1}, 1)
	if running.Continued.AllThreadsContinued {
		t.Fatalf("continued state claimed all threads: %#v", running)
	}
	stopped := targetState(actor.Event{Kind: actor.EventStopped, Snapshot: session.Snapshot{State: session.TargetStopped}}, 1)
	if stopped.Stopped.AllThreadsStopped {
		t.Fatalf("stopped state claimed all threads: %#v", stopped)
	}
}

func TestTargetStatePreservesExplicitActorTruth(t *testing.T) {
	running := targetState(actor.Event{
		Kind: actor.EventResumed, Snapshot: session.Snapshot{State: session.TargetRunning}, ThreadID: 1,
		AllThreadsContinued: true,
	}, 1)
	if !running.Continued.AllThreadsContinued {
		t.Fatalf("running state lost actor truth: %#v", running)
	}
	stopped := targetState(actor.Event{
		Kind: actor.EventStopped, Snapshot: session.Snapshot{State: session.TargetStopped},
		AllThreadsStopped: true,
	}, 1)
	if !stopped.Stopped.AllThreadsStopped {
		t.Fatalf("stopped state lost actor truth: %#v", stopped)
	}
}

func TestAbruptDisconnectAndGenerationChurnAreIdempotent(t *testing.T) {
	backend, core, publisher := boundBackend(t)
	attach(t, backend)
	core.mu.Lock()
	first := core.attachOwners[0]
	core.mu.Unlock()
	core.closeEvents(first)
	waitFor(t, func() bool { return publisher.terminationCount() == 1 })
	if _, err := backend.Execute(context.Background(), dap.Action{Kind: dap.ActionDisconnect}); err != nil {
		t.Fatal(err)
	}
	attach(t, backend)
	core.mu.Lock()
	second := core.attachOwners[1]
	core.mu.Unlock()
	// A stale generation must not terminate the newly attached frontend.
	core.closeEvents(first)
	time.Sleep(10 * time.Millisecond)
	if publisher.terminationCount() != 1 {
		t.Fatalf("stale frontend terminated active session: %d", publisher.terminationCount())
	}
	core.emit(second, actor.Event{Kind: actor.EventStopped, ThreadID: 1, Snapshot: session.Snapshot{State: session.TargetStopped}})
	waitFor(t, func() bool { return publisher.stateCount() == 1 })
}

func TestDAPSessionCloseReleasesActorFrontendExactlyOnce(t *testing.T) {
	backend, core, _ := boundBackend(t)
	s := dap.NewSession(backend)
	initialize, _ := json.Marshal(dap.InitializeArguments{AdapterID: "multi-dap"})
	if responses := s.Handle(context.Background(), dap.Envelope{Seq: 1, Type: dap.TypeRequest, Command: "initialize", Arguments: initialize}); len(responses) != 1 || responses[0].Success == nil || !*responses[0].Success {
		t.Fatalf("initialize responses = %#v", responses)
	}
	if responses := s.Handle(context.Background(), dap.Envelope{Seq: 2, Type: dap.TypeRequest, Command: "attach"}); len(responses) != 1 || responses[0].Event != "initialized" {
		t.Fatalf("attach responses = %#v", responses)
	}
	if responses := s.Handle(context.Background(), dap.Envelope{Seq: 3, Type: dap.TypeRequest, Command: "configurationDone"}); len(responses) != 2 {
		t.Fatalf("configurationDone responses = %#v", responses)
	}
	core.mu.Lock()
	owner := core.attachOwners[0]
	core.mu.Unlock()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	detached := core.detached[owner]
	core.mu.Unlock()
	if detached != 1 {
		t.Fatalf("transport close detached %d times, want 1", detached)
	}
}

func TestPublisherOverflowAndReconciliationTerminateAndRelease(t *testing.T) {
	backend, core, publisher := boundBackend(t)
	attach(t, backend)
	core.mu.Lock()
	first := core.attachOwners[0]
	core.mu.Unlock()
	publisher.mu.Lock()
	publisher.accept = false
	publisher.mu.Unlock()
	core.emit(first, actor.Event{Kind: actor.EventResumed, ThreadID: 1, Snapshot: session.Snapshot{State: session.TargetRunning}})
	waitFor(t, func() bool { return publisher.terminationCount() == 1 })
	core.mu.Lock()
	if core.detached[first] != 1 {
		core.mu.Unlock()
		t.Fatalf("overflow detach calls = %d", core.detached[first])
	}
	core.mu.Unlock()

	publisher.mu.Lock()
	publisher.accept = true
	publisher.mu.Unlock()
	attach(t, backend)
	core.mu.Lock()
	second := core.attachOwners[1]
	core.mu.Unlock()
	core.emit(second, actor.Event{Kind: actor.EventReconciliationRequired, Snapshot: session.Snapshot{State: session.TargetFaulted}})
	waitFor(t, func() bool { return publisher.terminationCount() == 2 })
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
