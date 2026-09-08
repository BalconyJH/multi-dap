// Package actor owns the asynchronous M1 debugger session.  It keeps target
// observation, handle invalidation, control ownership, and frontend events on
// one goroutine while bridge I/O is performed by bridge.Executor's worker.
package actor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/handle"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/core/stop"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

var (
	ErrClosed                = errors.New("actor: session is closed")
	ErrNotOpen               = errors.New("actor: session is not open")
	ErrAlreadyOpen           = errors.New("actor: session is already open")
	ErrBridgeStaleCompletion = errors.New("actor: bridge completion is stale")
	ErrReconciliationNeeded  = errors.New("actor: reconciliation is required")
)

const defaultBootstrapDeadline = 30 * time.Second

// Options supplies all target-dependent scheduling policy.  The executor is
// owned by Actor for its complete lifetime and must not be replaced directly
// by callers after construction.
type Options struct {
	Executor *bridge.Executor
	Open     multi.OpenRequest
	// CoreSpecs are the configured identities which Topology must bind to
	// MULTI program components before the initial state sample.
	CoreSpecs    []multi.CoreSpec
	Session      session.Options
	PollInterval time.Duration
	// ConsoleInterval limits optional console pane snapshots separately from
	// state polling. When console is enabled a zero value uses one second.
	ConsoleInterval time.Duration
	RPCDeadline     time.Duration
	// BootstrapDeadline bounds one Open -> Cores -> State acquisition. Open
	// may need longer than an ordinary bridge call, while Cores and State keep
	// their ordinary RPCDeadline within this parent budget.
	BootstrapDeadline time.Duration
	EventBuffer       int
	Handles           *handle.Store

	// Breakpoints, BreakpointRunner, SourceIndex, and SourceResolver are the M2 ownership
	// boundary. They are deliberately optional so an M1-only deployment does
	// not advertise breakpoint support by accident.
	Breakpoints      *breakpoint.Store
	BreakpointRunner BreakpointRunner
	SourceIndex      SourceIndex
	SourceResolver   SourceResolver
	Hints            <-chan uint32

	// Inspection is the M3 backend factory. Each invocation receives the
	// executor-owned bridge client, so an inspection request cannot bypass the
	// actor's single-flight transport boundary.
	Inspection InspectionRunner
	SourceMap  SourceMapper
	// M5 is the read-only Memory View and Disassembly backend. Like the
	// inspection runner, it receives only executor-owned bridge clients.
	M5 M5Runner

	// ConsoleEnabled permits bounded incremental MULTI console collection for
	// attached frontends. It defaults to false so embedders which do not expose
	// a console do not acquire extra target traffic.
	ConsoleEnabled bool
}

// Thread is a stable M1 thread projection.  Thread IDs are one-based core
// IDs, so they remain stable across observations and are never zero.
type Thread struct {
	ID     int
	CoreID uint64
	Name   string
}

// EventKind is frontend-neutral target state evidence.
type EventKind uint8

const (
	EventInvalidated EventKind = iota
	EventResumed
	EventStopped
	EventReconciliationRequired
	// EventOutput is debugger console text. Its source remains MULTI-neutral
	// here; the DAP integration maps it to frontend categories.
	EventOutput
)

// Event is sent in canonical reducer order.  An event is never silently
// discarded: a full subscription is closed and must attach again.
type Event struct {
	Kind                EventKind
	Snapshot            session.Snapshot
	ThreadID            int
	AllThreadsContinued bool
	AllThreadsStopped   bool
	Reason              stop.Reason
	HitBreakpointIDs    []int
	Cause               error
	Generation          uint64
	Console             multi.ConsoleOutput
	ConsoleUnavailable  bool
}

// Attachment is an event subscription scoped to one frontend generation.
// The channel closes on Detach, overflow, or actor shutdown.
type Attachment struct {
	Snapshot   session.Snapshot
	Generation uint64
	Events     <-chan Event
	close      func()
}

// Close detaches this frontend.  It is idempotent and never waits for bridge
// traffic, so a cancelled frontend cannot poison the shared debugger session.
func (a Attachment) Close() {
	if a.close != nil {
		a.close()
	}
}

// Actor serializes all session decisions. Public methods wait only for a
// buffered actor reply. Ordinary submitted bridge work uses Actor's RPC
// deadline; Open retains its caller's bounded bootstrap context through the
// required acquisition sequence.
type Actor struct {
	commands chan command
	done     chan struct{}
	faults   chan Fault
	once     sync.Once

	// submissions serializes admission to commands with shutdown. The command
	// channel deliberately remains open: closing it would race callers and
	// turn a rejected submission into a panic. Instead Close closes this gate
	// before it places its close command after every already admitted command.
	submissions sync.Mutex
	closing     bool
}

// Operation identifies the actor work category that made the session
// terminal. It is intentionally independent of MULTI command text so callers
// can record a stable failure classification without exposing target details.
type Operation uint8

const (
	OperationUnknown Operation = iota
	OperationOpen
	OperationCores
	OperationState
	OperationExecution
	OperationBreakpoints
	OperationBreakpointCleanup
	OperationInspection
	OperationRollbackClose
	OperationConsole
)

func (operation Operation) String() string {
	switch operation {
	case OperationOpen:
		return "open"
	case OperationCores:
		return "cores"
	case OperationState:
		return "state"
	case OperationExecution:
		return "execution"
	case OperationBreakpoints:
		return "breakpoints"
	case OperationBreakpointCleanup:
		return "breakpoint-cleanup"
	case OperationInspection:
		return "inspection"
	case OperationRollbackClose:
		return "rollback-close"
	case OperationConsole:
		return "console"
	default:
		return "unknown"
	}
}

// Fault is the single terminal fail-closed transition. Error text contains
// only the stable operation classification. Use errors.Is to inspect the
// preserved reconciliation sentinel or underlying failure category.
type Fault struct {
	Operation Operation
	cause     error
}

func (fault Fault) Error() string {
	return fmt.Sprintf("%s during %s", ErrReconciliationNeeded, fault.Operation)
}

// Unwrap preserves both the terminal reconciliation sentinel and the original
// cause without requiring consumers to parse fault text.
func (fault Fault) Unwrap() []error {
	if fault.cause == nil {
		return []error{ErrReconciliationNeeded}
	}
	return []error{ErrReconciliationNeeded, fault.cause}
}

type command interface{ apply(*runtime) }

type reply[T any] struct {
	value T
	err   error
}

type runtime struct {
	opts        Options
	executor    *bridge.Executor
	reducer     *session.Reducer
	handles     *handle.Store
	breakpoints *breakpoint.Store
	stopper     *stop.Arbiter
	commands    chan command
	done        chan struct{}
	faults      chan<- Fault
	complete    chan completion
	pending     []rpcWork
	inflight    *inflight
	nextToken   uint64

	opened  bool
	opening bool
	// openSucceeded becomes true only after MBP open returned its explicit
	// affirmative confirmation. It is the ownership proof required before a
	// failed cold bootstrap may issue MULTI close; a timeout or an open error
	// is deliberately too ambiguous to disconnect anything.
	openSucceeded bool
	// openGeneration is the executor generation that returned Open's explicit
	// affirmative confirmation. A cold rollback may target only this bridge.
	openGeneration bridge.BridgeGeneration
	faulted        bool
	closing        bool
	threads        []Thread
	subs           map[uint64]*subscriber
	nextSub        uint64
	lastStop       *stop.Effect
	topology       *multi.Topology
	execution      *multi.ExecutionDomain
	bootstrap      context.Context
	bootstrapStop  context.CancelFunc

	// Cleanup retries are budgeted by reducer StopEpoch. Repeated 250ms state
	// samples while a target remains stopped must not repeatedly clear an
	// orphan, but a detach that arrives in an already-stopped epoch is allowed
	// one additional immediate attempt.
	cleanupNaturalEpoch    uint64
	cleanupImmediateEpoch  uint64
	consoleDisabled        bool
	consoleFailureReported bool
	consoleFailurePending  bool
	consoleFailureTargets  map[uint64]uint64
}

type subscriber struct {
	owner      session.ControllerID
	generation uint64
	events     chan Event
}

type opKind uint8

const (
	opOpen opKind = iota
	opCores
	opState
	opExecution
	opBreakpoints
	opBreakpointCleanup
	opInspection
	opRollbackClose
	opConsole
)

func (kind opKind) operation() Operation {
	switch kind {
	case opOpen:
		return OperationOpen
	case opCores:
		return OperationCores
	case opState:
		return OperationState
	case opExecution:
		return OperationExecution
	case opBreakpoints:
		return OperationBreakpoints
	case opBreakpointCleanup:
		return OperationBreakpointCleanup
	case opInspection:
		return OperationInspection
	case opRollbackClose:
		return OperationRollbackClose
	case opConsole:
		return OperationConsole
	default:
		return OperationUnknown
	}
}

type deadlinePolicy uint8

const (
	deadlineRPC deadlinePolicy = iota
	deadlineBootstrap
	deadlineRollback
)

type rpcWork struct {
	kind opKind
	run  bridge.Operation
	// validate runs only on the actor goroutine immediately before the work is
	// submitted to the bridge. It must not read reducer state from run: run is
	// dispatched on a worker goroutine and therefore cannot safely decide
	// whether a previously queued observation is still current.
	validate           func(*runtime) error
	done               func(*runtime, any, error)
	parent             context.Context
	deadline           deadlinePolicy
	expectedGeneration bridge.BridgeGeneration
}

type inflight struct {
	token      uint64
	work       rpcWork
	submitted  bool
	generation bridge.BridgeGeneration
	operation  bridge.OperationID
}

type completion struct {
	token      uint64
	generation bridge.BridgeGeneration
	operation  bridge.OperationID
	accepted   bool
	result     any
	err        error
}

// stateEvidence keeps an authoritative State sample and the optional
// supporting H observation together. H is deliberately best-effort: a
// malformed or unavailable halt string must not erase a valid state sample;
// the stop arbiter then emits the conservative unknown reason.
type stateEvidence struct {
	state   multi.State
	halt    multi.HaltInfo
	hasHalt bool
}

// New starts an actor immediately.  It performs no bridge I/O until Open.
func New(options Options) (*Actor, error) {
	if options.Executor == nil {
		return nil, errors.New("actor: executor is required")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 250 * time.Millisecond
	}
	if options.ConsoleInterval <= 0 {
		options.ConsoleInterval = time.Second
	}
	if options.RPCDeadline <= 0 {
		options.RPCDeadline = 3 * time.Second
	}
	if options.BootstrapDeadline <= 0 {
		options.BootstrapDeadline = defaultBootstrapDeadline
	}
	if options.EventBuffer <= 0 {
		options.EventBuffer = 32
	}
	if options.Handles == nil {
		options.Handles = handle.NewStore()
	}
	if options.Breakpoints == nil {
		options.Breakpoints = breakpoint.NewStore()
	}

	a := &Actor{commands: make(chan command, 64), done: make(chan struct{}), faults: make(chan Fault, 1)}
	r := &runtime{
		opts: options, executor: options.Executor, reducer: session.New(options.Session),
		handles: options.Handles, breakpoints: options.Breakpoints, commands: a.commands, done: a.done, faults: a.faults,
		complete: make(chan completion, 8), subs: make(map[uint64]*subscriber),
	}
	r.stopper = stop.New(stop.TokenResolverFunc(func(token uint32) (stop.HintTarget, bool) {
		target, ok := r.breakpoints.LookupHint(token)
		return stop.HintTarget{BreakpointID: target.DAPID, Core: target.Core}, ok
	}))
	go r.loop()
	if options.Hints != nil {
		go r.watchHints(options.Hints)
	}
	return a, nil
}

// Faults reports the actor's single terminal fail-closed transition. The
// channel is buffered and deliberately remains open: Actor.Close is ordinary
// ownership cleanup, not a debugger fault.
func (a *Actor) Faults() <-chan Fault {
	if a == nil {
		return nil
	}
	return a.faults
}

// Open bootstraps MULTI in the required order: Open, Cores, then State.
func (a *Actor) Open(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, openCommand{ctx: ctx, reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

// Attach subscribes one frontend to future canonical transitions.  Attach is
// read-only and does not acquire the controller lease.
func (a *Actor) Attach(ctx context.Context, owner session.ControllerID) (Attachment, error) {
	ch := make(chan reply[Attachment], 1)
	if err := a.send(ctx, attachCommand{owner: owner, reply: ch}); err != nil {
		return Attachment{}, err
	}
	return await(ctx, a.done, ch, Attachment{})
}

// Detach removes all subscriptions for owner and releases its control lease.
func (a *Actor) Detach(ctx context.Context, owner session.ControllerID) error {
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, detachOwnerCommand{owner: owner, reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

func (a *Actor) AcquireControl(ctx context.Context, owner session.ControllerID) error {
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, acquireCommand{owner: owner, reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

func (a *Actor) ReleaseControl(ctx context.Context, owner session.ControllerID) error {
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, releaseCommand{owner: owner, reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

// Threads returns the immutable core projection established during Open.
func (a *Actor) Threads(ctx context.Context) ([]Thread, error) {
	ch := make(chan reply[[]Thread], 1)
	if err := a.send(ctx, threadsCommand{reply: ch}); err != nil {
		return nil, err
	}
	return await(ctx, a.done, ch, []Thread(nil))
}

// Snapshot returns the current observation-derived state only.
func (a *Actor) Snapshot(ctx context.Context) (session.Snapshot, error) {
	ch := make(chan reply[session.Snapshot], 1)
	if err := a.send(ctx, snapshotCommand{reply: ch}); err != nil {
		return session.Snapshot{}, err
	}
	return await(ctx, a.done, ch, session.Snapshot{})
}

// BreakpointCounts returns only aggregate ownership evidence. The actor keeps
// individual physical handles private to Debugger Core and the MULTI driver.
func (a *Actor) BreakpointCounts(ctx context.Context) (BreakpointCounts, error) {
	ch := make(chan reply[BreakpointCounts], 1)
	if err := a.send(ctx, breakpointCountsCommand{reply: ch}); err != nil {
		return BreakpointCounts{}, err
	}
	return await(ctx, a.done, ch, BreakpointCounts{})
}

type BreakpointCounts struct {
	DAPOwned int
	Pending  int
	Orphaned int
}

// Poll requests a state observation.  It is mainly useful to non-DAP callers
// and tests; normal operation uses PollInterval.
func (a *Actor) Poll(ctx context.Context) error {
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, pollCommand{reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

// ExecutionRequest preserves the configured target identity selected by the
// frontend. A command acknowledgement never changes canonical state; only a
// later validated observation may do that.
type ExecutionRequest struct {
	CoreID    uint64
	Operation multi.ExecutionOperation
}

// ExecutionResult is the execution-domain evidence available to a frontend
// after a successful command. Cores is the exact confirmed scope, while Truth
// is non-zero only when that scope proved an all-configured-core transition.
// It does not replace the actor's canonical observation event stream.
type ExecutionResult struct {
	Cores []uint64
	Truth multi.ExecutionTruth
}

// Execute submits an operation for one configured core. A multicore request
// enters the topology-bound execution domain on the actor executor; the
// current default-disabled domain rejects it before bridge I/O.
func (a *Actor) Execute(ctx context.Context, owner session.ControllerID, request ExecutionRequest) error {
	_, err := a.ExecuteResult(ctx, owner, request)
	return err
}

// ExecuteResult submits an operation and returns only its verified execution
// evidence. Existing callers that only need command completion may use
// Execute. Neither method publishes a state transition; that remains the
// canonical observation path.
func (a *Actor) ExecuteResult(ctx context.Context, owner session.ControllerID, request ExecutionRequest) (ExecutionResult, error) {
	ch := make(chan reply[ExecutionResult], 1)
	if err := a.send(ctx, executionCommand{owner: owner, request: request, reply: ch}); err != nil {
		return ExecutionResult{}, err
	}
	return await(ctx, a.done, ch, ExecutionResult{})
}

// BeginConfiguration and ConfigurationDone expose the reducer's frontend
// configuration barrier without exposing any DAP vocabulary.
func (a *Actor) BeginConfiguration(ctx context.Context) error {
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, beginConfigurationCommand{reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

func (a *Actor) ConfigurationDone(ctx context.Context) error {
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, configurationDoneCommand{reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

// CancelConfiguration removes an unfinished frontend barrier without
// publishing an initial state. It is safe only for the frontend that is
// abandoning configuration before configurationDone.
func (a *Actor) CancelConfiguration(ctx context.Context) error {
	reply := make(chan reply[struct{}], 1)
	if err := a.send(ctx, cancelConfigurationCommand{reply: reply}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, reply, struct{}{})
	return err
}

// AllocateHandle mints a suspended-state reference only while the observed
// target is stopped.  The handle store is otherwise not exposed outside Actor.
func (a *Actor) AllocateHandle(ctx context.Context, kind handle.Kind, core handle.CoreID, value any) (int32, error) {
	ch := make(chan reply[int32], 1)
	if err := a.send(ctx, allocateHandleCommand{kind: kind, core: core, value: value, reply: ch}); err != nil {
		return 0, err
	}
	return await(ctx, a.done, ch, int32(0))
}

// Close stops the actor and interrupts outstanding bridge I/O.  It does not
// issue MULTI's close command, preserving the warm service-router session.
func (a *Actor) Close() {
	a.once.Do(func() {
		ch := make(chan reply[struct{}], 1)
		a.submissions.Lock()
		a.closing = true
		select {
		case a.commands <- closeCommand{reply: ch}:
			a.submissions.Unlock()
			<-ch
		case <-a.done:
			a.submissions.Unlock()
		}
		// The close acknowledgement is produced by the loop, not after it has
		// exited. Wait for done so Close never returns while the actor can still
		// consume commands or publish events.
		<-a.done
	})
}

func (a *Actor) send(ctx context.Context, c command) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.submissions.Lock()
	defer a.submissions.Unlock()
	if a.closing {
		return ErrClosed
	}
	select {
	case <-a.done:
		return ErrClosed
	case a.commands <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// await binds an accepted public request to the actor's lifetime. A buffered
// reply wins even if shutdown has started; otherwise loop termination releases
// callers which were queued or inflight when Close stopped consuming commands.
func await[T any](ctx context.Context, done <-chan struct{}, ch <-chan reply[T], zero T) (T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result := <-ch:
		return result.value, result.err
	default:
	}
	select {
	case result := <-ch:
		return result.value, result.err
	case <-done:
		// A response and done can become ready together. Preserve the response
		// rather than spuriously converting completed work into ErrClosed.
		select {
		case result := <-ch:
			return result.value, result.err
		default:
			return zero, ErrClosed
		}
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (r *runtime) loop() {
	stateTicker := time.NewTicker(r.opts.PollInterval)
	consoleTicker := time.NewTicker(r.opts.ConsoleInterval)
	defer stateTicker.Stop()
	defer consoleTicker.Stop()
	defer close(r.done)
	for {
		r.startNext()
		select {
		case c := <-r.commands:
			c.apply(r)
		case c := <-r.complete:
			r.completeWork(c)
		case <-stateTicker.C:
			if r.opened && !r.faulted && !r.hasStateWork() {
				r.enqueue(r.stateWork(nil, nil))
			}
		case <-consoleTicker.C:
			if r.consoleFailurePending && r.consoleFrontendActive() {
				r.consoleFailurePending = false
				r.consoleFailureReported = true
				r.publishTo(Event{Kind: EventOutput, ConsoleUnavailable: true}, r.consoleFailureTargets)
				r.consoleFailureTargets = nil
			}
			if r.consoleActive() && !r.hasConsoleWork() {
				r.enqueue(r.consoleWork(r.consoleSubscribers()))
			}
		}
		if r.closed() {
			return
		}
	}
}

func (r *runtime) closed() bool { return r.closing }

func (r *runtime) startNext() {
	// A guard rejection completes synchronously without creating an inflight
	// operation. Keep draining such work so the next queued request cannot wait
	// indefinitely for an unrelated ticker or command to wake the loop.
	for r.inflight == nil && len(r.pending) != 0 && !r.closing && !r.faulted {
		work := r.pending[0]
		r.pending = r.pending[1:]
		if work.expectedGeneration != 0 && r.executor.Generation() != work.expectedGeneration {
			work.done(r, nil, ErrBridgeStaleCompletion)
			continue
		}
		if work.validate != nil {
			if err := work.validate(r); err != nil {
				work.done(r, nil, err)
				continue
			}
		}
		r.nextToken++
		in := &inflight{token: r.nextToken, work: work}
		r.inflight = in
		go r.dispatch(in.token, work)
	}
}

func (r *runtime) dispatch(token uint64, work rpcWork) {
	parent := work.parent
	if parent == nil {
		parent = context.Background()
	}
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	switch work.deadline {
	case deadlineBootstrap:
		ctx, cancel = context.WithCancel(parent)
	case deadlineRollback:
		ctx, cancel = context.WithTimeout(context.Background(), r.opts.RPCDeadline)
	default:
		ctx, cancel = context.WithTimeout(parent, r.opts.RPCDeadline)
	}
	defer cancel()
	executor := r.executor
	generation := executor.Generation()
	if work.expectedGeneration != 0 && generation != work.expectedGeneration {
		r.deliver(completion{token: token, generation: generation, err: ErrBridgeStaleCompletion})
		return
	}
	operation, completed, err := executor.Submit(ctx, work.run)
	if err != nil {
		r.deliver(completion{token: token, generation: generation, err: err})
		return
	}
	r.deliver(completion{token: token, generation: generation, operation: operation, accepted: true})
	select {
	case result := <-completed:
		r.deliver(completion{
			token:      token,
			generation: result.Generation,
			operation:  result.Operation,
			result:     result.Result,
			err:        result.Err,
		})
	case <-ctx.Done():
		// Executor will still finish and retain the completion, but this actor
		// must converge on its own deadline instead of waiting forever.
		r.deliver(completion{token: token, generation: generation, operation: operation, err: ctx.Err()})
	}
}

func (r *runtime) deliver(c completion) {
	select {
	case r.complete <- c:
	case <-r.done:
	}
}

func (r *runtime) completeWork(c completion) {
	in := r.inflight
	if in == nil || in.token != c.token {
		return
	}
	if c.accepted {
		if in.submitted || c.operation == 0 || c.generation == 0 || r.executor.Generation() != c.generation || (in.work.expectedGeneration != 0 && c.generation != in.work.expectedGeneration) {
			r.inflight = nil
			r.failClosed(in.work.kind.operation(), ErrBridgeStaleCompletion)
			in.work.done(r, nil, ErrBridgeStaleCompletion)
			return
		}
		in.submitted = true
		in.operation = c.operation
		in.generation = c.generation
		return
	}
	r.inflight = nil
	if !in.submitted {
		in.work.done(r, nil, c.err)
		return
	}
	if c.operation == 0 || c.operation != in.operation {
		r.failClosed(in.work.kind.operation(), ErrBridgeStaleCompletion)
		in.work.done(r, nil, ErrBridgeStaleCompletion)
		return
	}
	if c.generation == 0 || c.generation != in.generation || r.executor.Generation() != in.generation || (in.work.expectedGeneration != 0 && c.generation != in.work.expectedGeneration) {
		r.failClosed(in.work.kind.operation(), ErrBridgeStaleCompletion)
		in.work.done(r, nil, ErrBridgeStaleCompletion)
		return
	}
	if in.work.kind == opOpen && c.err == nil {
		r.openGeneration = in.generation
	}
	if completionFailureIsTerminal(c.err) && !r.defersBootstrapFault(in.work.kind) {
		r.failClosed(in.work.kind.operation(), c.err)
	}
	in.work.done(r, c.result, c.err)
}

// completionFailureIsTerminal covers errors that leave the actor unable to
// trust bridge ordering or its observed session model. Inspection text-format
// errors use multi.ErrInspectionFormat instead: their command completed on a
// healthy, non-lossy bridge but the individual response has no safe DAP view.
func completionFailureIsTerminal(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, bridge.ErrPoisoned) || errors.Is(err, multi.ErrProtocol)
}

// A cold bootstrap must get the chance to attempt its one ownership-proven
// rollback close before a fatal cores/state completion freezes the executor
// queue. The close will normally report the same poisoned/protocol failure;
// rollbackCloseWork then preserves both causes and makes the actor terminal.
func (r *runtime) defersBootstrapFault(kind opKind) bool {
	return r.opening && r.openSucceeded && resolvedOpenMode(r.opts.Open) == multi.OpenModeCold && (kind == opCores || kind == opState)
}

func (r *runtime) enqueue(work rpcWork) { r.pending = append(r.pending, work) }

func (r *runtime) hasStateWork() bool {
	if r.inflight != nil && r.inflight.work.kind == opState {
		return true
	}
	for _, work := range r.pending {
		if work.kind == opState {
			return true
		}
	}
	return false
}

func (r *runtime) hasConsoleWork() bool {
	if r.inflight != nil && r.inflight.work.kind == opConsole {
		return true
	}
	for _, work := range r.pending {
		if work.kind == opConsole {
			return true
		}
	}
	return false
}

// consoleActive deliberately observes the reducer state on the actor
// goroutine. Console traffic is optional and must not run before Open, while
// a frontend is absent, or while DAP configuration suppresses events.
func (r *runtime) consoleActive() bool {
	return r.consoleFrontendActive() && !r.consoleDisabled
}

func (r *runtime) consoleFrontendActive() bool {
	return r.opts.ConsoleEnabled && r.opened && !r.faulted && len(r.subs) != 0 && !r.reducer.Snapshot().Configuring
}

// consoleSubscribers snapshots the subscribers eligible when this console
// read starts. Its generation fence prevents delayed pane text from crossing
// a detach/reattach boundary into a different frontend.
func (r *runtime) consoleSubscribers() map[uint64]uint64 {
	targets := make(map[uint64]uint64, len(r.subs))
	for id, sub := range r.subs {
		targets[id] = sub.generation
	}
	return targets
}

func (r *runtime) consoleWork(targets map[uint64]uint64) rpcWork {
	return rpcWork{kind: opConsole, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		d, err := multi.NewDriver(client)
		if err != nil {
			return nil, err
		}
		return d.Console(ctx)
	}, done: func(r *runtime, value any, err error) {
		if err != nil {
			// A completed remote error leaves the shared transport ordered, so
			// this optional pane can be disabled without affecting target
			// control. Timeout, cancellation, poisoning, and protocol errors
			// have already fail-closed the actor in completeWork.
			r.consoleDisabled = true
			r.noteConsoleFailure(targets)
			return
		}
		output, ok := value.(multi.ConsoleOutput)
		if !ok {
			r.consoleDisabled = true
			r.noteConsoleFailure(targets)
			return
		}
		if r.consoleActive() && (output.Server != "" || output.IO != "" || output.RawLossy) {
			r.publishTo(Event{Kind: EventOutput, Console: output}, targets)
		}
	}}
}

func (r *runtime) noteConsoleFailure(targets map[uint64]uint64) {
	if r.consoleFailureReported {
		return
	}
	if r.consoleFrontendActive() {
		r.consoleFailureReported = true
		r.publishTo(Event{Kind: EventOutput, ConsoleUnavailable: true}, targets)
		return
	}
	r.consoleFailurePending = true
	r.consoleFailureTargets = targets
}

func (r *runtime) openWork(reply chan reply[struct{}], bootstrap context.Context) rpcWork {
	request := r.opts.Open
	return rpcWork{kind: opOpen, parent: bootstrap, deadline: deadlineBootstrap, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		d, err := multi.NewDriver(client)
		if err != nil {
			return nil, err
		}
		return nil, d.Open(ctx, request)
	}, done: func(r *runtime, _ any, err error) {
		if err != nil {
			r.opening = false
			r.finishBootstrap()
			respond(reply, struct{}{}, err)
			return
		}
		r.openSucceeded = true
		if err := bootstrap.Err(); err != nil {
			r.bootstrapFailed(OperationOpen, reply, err)
			return
		}
		r.enqueue(r.coresWork(reply, bootstrap))
	}}
}

type resolvedTopology struct{ topology *multi.Topology }

func (r *runtime) coresWork(reply chan reply[struct{}], bootstrap context.Context) rpcWork {
	return rpcWork{kind: opCores, parent: bootstrap, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		d, err := multi.NewDriver(client)
		if err != nil {
			return nil, err
		}
		topology, err := multi.ResolveTopology(ctx, d, r.opts.CoreSpecs)
		if err != nil {
			return nil, err
		}
		return resolvedTopology{topology: topology}, nil
	}, done: func(r *runtime, value any, err error) {
		if err != nil {
			r.bootstrapFailed(OperationCores, reply, err)
			return
		}
		resolved, ok := value.(resolvedTopology)
		if !ok {
			r.bootstrapFailed(OperationCores, reply, errors.New("actor: topology result has wrong type"))
			return
		}
		threads, err := makeThreads(resolved.topology.Cores())
		if err != nil {
			r.bootstrapFailed(OperationCores, reply, err)
			return
		}
		if err := r.bindTopology(resolved.topology); err != nil {
			r.bootstrapFailed(OperationCores, reply, err)
			return
		}
		execution, err := multi.NewExecutionDomain(resolved.topology)
		if err != nil {
			r.bootstrapFailed(OperationCores, reply, err)
			return
		}
		r.topology = resolved.topology
		r.execution = execution
		r.threads = threads
		r.enqueue(r.stateWork(reply, bootstrap))
	}}
}

func (r *runtime) stateWork(reply chan reply[struct{}], bootstrap context.Context) rpcWork {
	return rpcWork{kind: opState, parent: bootstrap, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		d, err := multi.NewDriver(client)
		if err != nil {
			return nil, err
		}
		state, err := d.State(ctx)
		if err != nil {
			return nil, err
		}
		evidence := stateEvidence{state: state}
		if state.Status.IsStopped() {
			if halt, haltErr := d.HaltInfo(ctx); haltErr == nil {
				evidence.halt, evidence.hasHalt = halt, true
			} else if errors.Is(haltErr, multi.ErrProtocol) || errors.Is(haltErr, bridge.ErrPoisoned) {
				// H is supporting evidence, but a bridge contract/transport
				// failure is not a benign "unknown reason". Accepting later
				// results after such a failure would violate generation fencing.
				return nil, haltErr
			}
		}
		return evidence, nil
	}, done: func(r *runtime, value any, err error) {
		if err == nil {
			evidence, ok := value.(stateEvidence)
			if !ok {
				err = errors.New("actor: state result has wrong type")
			} else {
				err = r.observe(evidence)
				if errors.Is(err, session.ErrReconciliationRequired) && !r.opening {
					r.failClosed(OperationState, err)
				}
			}
		}
		if r.opening {
			if err != nil {
				r.bootstrapFailed(OperationState, reply, err)
				return
			}
			r.opening = false
			r.opened = true
			r.finishBootstrap()
		}
		if reply != nil {
			respond(reply, struct{}{}, err)
		}
	}}
}

// bootstrapFailed converges an actor whose open RPC succeeded but whose
// required cores/state bootstrap did not. A cold session is bridge-owned and
// therefore gets one actor-owned, short typed close on the same executor
// generation before the failure becomes terminal. This close intentionally
// does not inherit the cancelled bootstrap context. Warm sessions bind an
// operator-owned window and must never be disconnected here.
func (r *runtime) bootstrapFailed(operation Operation, reply chan reply[struct{}], bootstrapErr error) {
	// A generation mismatch has already made the transport identity unsafe.
	// Never enqueue close against a replacement client: returning the stale
	// completion is safer than guessing which session it would affect.
	if !r.opening || r.faulted {
		r.opening = false
		r.finishBootstrap()
		respond(reply, struct{}{}, bootstrapErr)
		return
	}
	if r.openSucceeded && resolvedOpenMode(r.opts.Open) == multi.OpenModeCold {
		if r.openGeneration == 0 || r.executor.Generation() != r.openGeneration {
			r.opening = false
			r.finishBootstrap()
			r.failClosed(operation, ErrBridgeStaleCompletion)
			respond(reply, struct{}{}, ErrBridgeStaleCompletion)
			return
		}
		r.enqueue(r.rollbackCloseWork(operation, reply, bootstrapErr))
		return
	}
	r.opening = false
	r.finishBootstrap()
	r.failClosed(operation, bootstrapErr)
	respond(reply, struct{}{}, bootstrapErr)
}

func resolvedOpenMode(request multi.OpenRequest) multi.OpenMode {
	if request.Mode == "" {
		return multi.OpenModeCold
	}
	return request.Mode
}

func (r *runtime) rollbackCloseWork(bootstrapOperation Operation, reply chan reply[struct{}], bootstrapErr error) rpcWork {
	return rpcWork{kind: opRollbackClose, deadline: deadlineRollback, expectedGeneration: r.openGeneration, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		d, err := multi.NewDriver(client)
		if err != nil {
			return nil, err
		}
		return nil, d.Close(ctx)
	}, done: func(r *runtime, _ any, rollbackErr error) {
		r.opening = false
		r.openSucceeded = false
		r.openGeneration = 0
		r.finishBootstrap()
		if rollbackErr != nil {
			combined := errors.Join(bootstrapErr, fmt.Errorf("actor: cold startup rollback close: %w", rollbackErr))
			r.failClosed(OperationRollbackClose, combined)
			respond(reply, struct{}{}, combined)
			return
		}
		r.failClosed(bootstrapOperation, bootstrapErr)
		respond(reply, struct{}{}, bootstrapErr)
	}}
}

func (r *runtime) finishBootstrap() {
	if r.bootstrapStop != nil {
		r.bootstrapStop()
		r.bootstrapStop = nil
		r.bootstrap = nil
	}
}

func (r *runtime) observe(evidence stateEvidence) error {
	state := evidence.state
	// H reports the exact command list of a fired DAP-owned breakpoint. Feed
	// its strictly parsed token through the same arbiter path as the optional
	// UDP accelerator before applying the authoritative state sample. This is
	// what keeps polling independently correct when the hint channel is absent
	// or lossy.
	if evidence.hasHalt {
		if token, ok := evidence.halt.HintToken(); ok {
			r.stopper.NoteHint(token)
		}
	}
	effects, err := r.stopper.Observe(stopObservation(state, evidence.halt, evidence.hasHalt))
	if err != nil {
		if !r.opening {
			r.failClosed(OperationState, err)
		}
		return err
	}
	stamp, hasStamp := state.ProcessInfo.StopStamp()
	result, err := r.reducer.Observe(session.Observation{Status: state.Status, StopStamp: stamp, HasStopStamp: hasStamp})
	if err != nil {
		r.applyReducerActions(result.Actions, result.Snapshot, err)
		return err
	}
	if err := r.verifyArbiterSnapshot(result.Snapshot); err != nil {
		if !r.opening {
			r.failClosed(OperationState, err)
		}
		return err
	}
	if result.Snapshot.Configuring {
		// During configuration the reducer intentionally suppresses public
		// state transitions. Invalidations remain observable because stale
		// suspended references must never survive the barrier.
		for _, effect := range effects {
			if effect.Kind == stop.EffectInvalidateSuspended {
				r.applyStopEffect(effect, result.Snapshot, nil)
			}
		}
		r.maybeScheduleBreakpointCleanup(result.Snapshot, false)
		return nil
	}
	for _, effect := range effects {
		r.applyStopEffect(effect, result.Snapshot, nil)
	}
	r.maybeScheduleBreakpointCleanup(result.Snapshot, false)
	return nil
}

func (r *runtime) maybeScheduleBreakpointCleanup(snapshot session.Snapshot, immediate bool) {
	if snapshot.State != session.TargetStopped || r.opts.BreakpointRunner == nil {
		return
	}
	epoch := uint64(snapshot.StopEpoch)
	if immediate {
		if r.cleanupImmediateEpoch == epoch {
			return
		}
		r.cleanupImmediateEpoch = epoch
	} else {
		if r.cleanupNaturalEpoch == epoch || r.breakpoints.PendingCount()+r.breakpoints.OrphanCount() == 0 {
			return
		}
		r.cleanupNaturalEpoch = epoch
	}
	r.enqueue(r.breakpointCleanupWork())
}

func (r *runtime) applyReducerActions(actions []session.Action, snapshot session.Snapshot, cause error) {
	for _, action := range actions {
		switch action.Kind {
		case session.ActionInvalidateSuspendedReferences:
			r.handles.DropStopBound()
			r.publish(Event{Kind: EventInvalidated, Snapshot: snapshot, Cause: cause})
		case session.ActionExecutionResumed:
			// The freeze group resumes every core. DAP requires one valid
			// threadId for a continued event, so use a stable member of the
			// resumed group without claiming that it triggered the transition.
			r.publish(Event{Kind: EventResumed, Snapshot: snapshot, ThreadID: r.representativeThread(), Cause: cause})
		case session.ActionExecutionSuspended:
			// M1 state evidence has no core identity. A stopped event permits
			// omitting threadId; M2's stop arbiter will populate it only after
			// the halt cause identifies a core.
			r.publish(Event{Kind: EventStopped, Snapshot: snapshot, Cause: cause})
		case session.ActionReconciliationRequired:
			r.publish(Event{Kind: EventReconciliationRequired, Snapshot: snapshot, Cause: cause})
		}
	}
}

func (r *runtime) applyStopEffect(effect stop.Effect, snapshot session.Snapshot, cause error) {
	switch effect.Kind {
	case stop.EffectInvalidateSuspended:
		r.handles.DropStopBound()
		r.publish(Event{Kind: EventInvalidated, Snapshot: snapshot, Cause: cause})
	case stop.EffectContinued:
		r.publish(Event{Kind: EventResumed, Snapshot: snapshot, ThreadID: r.representativeThread(), Cause: cause})
	case stop.EffectStopped:
		effect := effect
		r.lastStop = &effect
		threadID := 0
		if effect.HasCore {
			threadID = r.threadIDForCore(uint64(effect.Core))
		}
		r.publish(Event{
			Kind: EventStopped, Snapshot: snapshot, ThreadID: threadID, Cause: cause,
			Reason: effect.Reason, HitBreakpointIDs: append([]int(nil), effect.HitBreakpointIDs...),
		})
	}
}

func (r *runtime) verifyArbiterSnapshot(snapshot session.Snapshot) error {
	derived := r.stopper.Snapshot()
	if uint64(snapshot.StopEpoch) != derived.StopEpoch || uint64(snapshot.ExecutionEpoch) != derived.ExecutionEpoch {
		return fmt.Errorf("actor: reducer/stop-arbiter epoch disagreement: reducer=(%d,%d) arbiter=(%d,%d)", snapshot.ExecutionEpoch, snapshot.StopEpoch, derived.ExecutionEpoch, derived.StopEpoch)
	}
	return nil
}

func (r *runtime) publish(event Event) {
	for id, sub := range r.subs {
		event.Generation = sub.generation
		select {
		case sub.events <- event:
		default:
			close(sub.events)
			delete(r.subs, id)
		}
	}
}

// publishTo applies a captured subscriber-generation fence. Unlike canonical
// state transitions, console pane text is best-effort and must never be
// replayed to a frontend which attached after the read began.
func (r *runtime) publishTo(event Event, targets map[uint64]uint64) {
	for id, generation := range targets {
		sub, ok := r.subs[id]
		if !ok || sub.generation != generation {
			continue
		}
		event.Generation = sub.generation
		select {
		case sub.events <- event:
		default:
			close(sub.events)
			delete(r.subs, id)
		}
	}
}

func (r *runtime) failClosed(operation Operation, cause error) {
	if r.faulted {
		return
	}
	r.faulted = true
	fault := Fault{Operation: operation, cause: cause}
	select {
	case r.faults <- fault:
	default:
	}
	r.handles.DropStopBound()
	pending := r.pending
	r.pending = nil
	for _, work := range pending {
		work.done(r, nil, ErrReconciliationNeeded)
	}
	if r.reducer.Snapshot().State == session.TargetFaulted {
		return
	}
	r.publish(Event{Kind: EventReconciliationRequired, Snapshot: r.reducer.Snapshot(), Cause: cause})
}

func (r *runtime) representativeThread() int {
	if len(r.threads) == 0 {
		return 0
	}
	return r.threads[0].ID
}

func makeThreads(cores []multi.TopologyCore) ([]Thread, error) {
	threads := make([]Thread, 0, len(cores))
	seen := make(map[uint64]struct{})
	for _, core := range cores {
		if core.ID < 0 || uint64(core.ID) >= uint64(^uint(0)>>1) {
			return nil, fmt.Errorf("actor: core id %d exceeds thread id range", core.ID)
		}
		id := uint64(core.ID)
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("actor: duplicate core id %d", core.ID)
		}
		seen[id] = struct{}{}
		threads = append(threads, Thread{ID: core.ID + 1, CoreID: id, Name: fmt.Sprintf("core%d", core.ID)})
	}
	if len(threads) == 0 {
		return nil, errors.New("actor: topology has no configured cores")
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].CoreID < threads[j].CoreID })
	return threads, nil
}

type topologyBinder interface {
	BindMULTITopology(*multi.Topology) error
}

func (r *runtime) bindTopology(topology *multi.Topology) error {
	for _, candidate := range []any{r.opts.BreakpointRunner, r.opts.Inspection, r.opts.SourceResolver, r.opts.M5} {
		if candidate == nil {
			continue
		}
		if binder, ok := candidate.(topologyBinder); ok {
			if err := binder.BindMULTITopology(topology); err != nil {
				return err
			}
		}
	}
	return nil
}

func respond[T any](ch chan reply[T], value T, err error) {
	if ch != nil {
		ch <- reply[T]{value: value, err: err}
	}
}

type openCommand struct {
	ctx   context.Context
	reply chan reply[struct{}]
}

func (c openCommand) apply(r *runtime) {
	if r.faulted {
		respond(c.reply, struct{}{}, ErrReconciliationNeeded)
		return
	}
	if r.opened || r.opening {
		respond(c.reply, struct{}{}, ErrAlreadyOpen)
		return
	}
	bootstrap, cancel := context.WithTimeout(c.ctx, r.opts.BootstrapDeadline)
	r.bootstrap = bootstrap
	r.bootstrapStop = cancel
	r.opening = true
	r.enqueue(r.openWork(c.reply, bootstrap))
}

type attachCommand struct {
	owner session.ControllerID
	reply chan reply[Attachment]
}

func (c attachCommand) apply(r *runtime) {
	if r.faulted {
		respond(c.reply, Attachment{}, ErrReconciliationNeeded)
		return
	}
	r.nextSub++
	id := r.nextSub
	events := make(chan Event, r.opts.EventBuffer)
	sub := &subscriber{owner: c.owner, generation: id, events: events}
	r.subs[id] = sub
	var once sync.Once
	attachment := Attachment{Snapshot: r.reducer.Snapshot(), Generation: id, Events: events, close: func() { once.Do(func() { r.detachAsync(id) }) }}
	respond(c.reply, attachment, nil)
}

type detachOwnerCommand struct {
	owner session.ControllerID
	reply chan reply[struct{}]
}

func (c detachOwnerCommand) apply(r *runtime) {
	r.detachOwner(c.owner)
	respond(c.reply, struct{}{}, nil)
}

type acquireCommand struct {
	owner session.ControllerID
	reply chan reply[struct{}]
}

func (c acquireCommand) apply(r *runtime) {
	if r.faulted {
		respond(c.reply, struct{}{}, ErrReconciliationNeeded)
		return
	}
	respond(c.reply, struct{}{}, r.reducer.AcquireControl(c.owner))
}

type releaseCommand struct {
	owner session.ControllerID
	reply chan reply[struct{}]
}

func (c releaseCommand) apply(r *runtime) {
	respond(c.reply, struct{}{}, r.reducer.ReleaseControl(c.owner))
}

type threadsCommand struct{ reply chan reply[[]Thread] }

func (c threadsCommand) apply(r *runtime) {
	if !r.opened {
		respond(c.reply, nil, ErrNotOpen)
		return
	}
	threads := append([]Thread(nil), r.threads...)
	respond(c.reply, threads, nil)
}

type snapshotCommand struct{ reply chan reply[session.Snapshot] }

func (c snapshotCommand) apply(r *runtime) {
	if r.faulted {
		respond(c.reply, r.reducer.Snapshot(), ErrReconciliationNeeded)
		return
	}
	respond(c.reply, r.reducer.Snapshot(), nil)
}

type breakpointCountsCommand struct{ reply chan reply[BreakpointCounts] }

func (c breakpointCountsCommand) apply(r *runtime) {
	respond(c.reply, BreakpointCounts{
		DAPOwned: r.breakpoints.DAPOwnedCount(),
		Pending:  r.breakpoints.PendingCount(),
		Orphaned: r.breakpoints.OrphanCount(),
	}, nil)
}

type pollCommand struct{ reply chan reply[struct{}] }

func (c pollCommand) apply(r *runtime) {
	if !r.opened {
		respond(c.reply, struct{}{}, ErrNotOpen)
		return
	}
	if r.faulted {
		respond(c.reply, struct{}{}, ErrReconciliationNeeded)
		return
	}
	r.enqueue(r.stateWork(c.reply, nil))
}

type executionCommand struct {
	owner   session.ControllerID
	request ExecutionRequest
	reply   chan reply[ExecutionResult]
}

func (c executionCommand) apply(r *runtime) {
	if !r.opened {
		respond(c.reply, ExecutionResult{}, ErrNotOpen)
		return
	}
	if r.faulted {
		respond(c.reply, ExecutionResult{}, ErrReconciliationNeeded)
		return
	}
	if !r.hasConfiguredCore(c.request.CoreID) {
		respond(c.reply, ExecutionResult{}, fmt.Errorf("actor: unknown configured core %d", c.request.CoreID))
		return
	}
	if c.request.Operation < multi.ExecutionContinue || c.request.Operation > multi.ExecutionNext {
		respond(c.reply, ExecutionResult{}, fmt.Errorf("actor: invalid execution operation %s", c.request.Operation))
		return
	}
	if c.request.Operation == multi.ExecutionPause {
		if err := r.reducer.RequireControl(c.owner); err != nil {
			respond(c.reply, ExecutionResult{}, err)
			return
		}
	} else if err := r.reducer.RequireResume(c.owner); err != nil {
		respond(c.reply, ExecutionResult{}, err)
		return
	}
	if len(r.threads) == 1 {
		r.enqueue(r.singleCoreExecutionWork(c.request, c.reply))
		return
	}
	if r.execution == nil {
		respond(c.reply, ExecutionResult{}, multi.ErrExecutionDomainUnavailable)
		return
	}
	if c.request.CoreID > uint64(maxInt()) {
		respond(c.reply, ExecutionResult{}, fmt.Errorf("actor: configured core %d is outside platform range", c.request.CoreID))
		return
	}
	scope, err := r.execution.ScopeForCore(int(c.request.CoreID))
	if err != nil {
		respond(c.reply, ExecutionResult{}, err)
		return
	}
	r.enqueue(r.executionDomainWork(multi.ExecutionRequest{Operation: c.request.Operation, Scope: scope}, c.reply))
}

func (r *runtime) hasConfiguredCore(coreID uint64) bool {
	for _, thread := range r.threads {
		if thread.CoreID == coreID {
			return true
		}
	}
	return false
}

type beginConfigurationCommand struct{ reply chan reply[struct{}] }

func (c beginConfigurationCommand) apply(r *runtime) {
	result, err := r.reducer.BeginConfiguration()
	r.applyReducerActions(result.Actions, result.Snapshot, err)
	respond(c.reply, struct{}{}, err)
}

type configurationDoneCommand struct{ reply chan reply[struct{}] }

func (c configurationDoneCommand) apply(r *runtime) {
	result, err := r.reducer.ConfigurationDone()
	if err == nil {
		r.applyConfigurationActions(result.Actions, result.Snapshot)
	} else {
		r.applyReducerActions(result.Actions, result.Snapshot, err)
	}
	respond(c.reply, struct{}{}, err)
}

type cancelConfigurationCommand struct{ reply chan reply[struct{}] }

func (c cancelConfigurationCommand) apply(r *runtime) {
	result, err := r.reducer.CancelConfiguration()
	r.applyReducerActions(result.Actions, result.Snapshot, err)
	respond(c.reply, struct{}{}, err)
}

type allocateHandleCommand struct {
	kind  handle.Kind
	core  handle.CoreID
	value any
	reply chan reply[int32]
}

func (c allocateHandleCommand) apply(r *runtime) {
	s := r.reducer.Snapshot()
	if s.State != session.TargetStopped {
		respond(c.reply, 0, handle.ErrStale)
		return
	}
	id, err := r.handles.Alloc(c.kind, c.core, uint64(s.StopEpoch), c.value)
	respond(c.reply, id, err)
}

type closeCommand struct{ reply chan reply[struct{}] }

func (c closeCommand) apply(r *runtime) {
	for id, sub := range r.subs {
		close(sub.events)
		delete(r.subs, id)
	}
	r.handles.DropStopBound()
	r.finishBootstrap()
	r.executor.Close()
	r.closing = true
	respond(c.reply, struct{}{}, nil)
}

type detachSubscriptionCommand struct{ id uint64 }

func (c detachSubscriptionCommand) apply(r *runtime) { r.detach(c.id) }

func (r *runtime) singleCoreExecutionWork(request ExecutionRequest, reply chan reply[ExecutionResult]) rpcWork {
	return rpcWork{kind: opExecution, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		d, err := multi.NewDriver(client)
		if err != nil {
			return nil, err
		}
		switch request.Operation {
		case multi.ExecutionContinue:
			return nil, d.Resume(ctx)
		case multi.ExecutionPause:
			return nil, d.Halt(ctx)
		case multi.ExecutionStepIn:
			return nil, d.StepIn(ctx)
		case multi.ExecutionNext:
			return nil, d.Next(ctx)
		default:
			return nil, errors.New("actor: invalid single-core execution operation")
		}
	}, done: func(_ *runtime, _ any, err error) {
		if err != nil {
			respond(reply, ExecutionResult{}, err)
			return
		}
		respond(reply, ExecutionResult{Cores: []uint64{request.CoreID}}, nil)
	}}
}

func (r *runtime) executionDomainWork(request multi.ExecutionRequest, reply chan reply[ExecutionResult]) rpcWork {
	return rpcWork{kind: opExecution, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		d, err := multi.NewDriver(client)
		if err != nil {
			return nil, err
		}
		return r.execution.Execute(ctx, d, request)
	}, done: func(r *runtime, value any, err error) {
		if err != nil {
			respond(reply, ExecutionResult{}, err)
			return
		}
		result, ok := value.(multi.ExecutionResult)
		if !ok {
			respond(reply, ExecutionResult{}, errors.New("actor: execution domain returned an invalid result"))
			return
		}
		cores := result.Request().Scope.Cores()
		outcome := ExecutionResult{Cores: make([]uint64, len(cores)), Truth: r.execution.Truth(result)}
		for index, core := range cores {
			if core < 0 {
				respond(reply, ExecutionResult{}, fmt.Errorf("actor: execution domain returned negative core %d", core))
				return
			}
			outcome.Cores[index] = uint64(core)
		}
		respond(reply, outcome, nil)
	}}
}

func maxInt() int { return int(^uint(0) >> 1) }

func (r *runtime) detachAsync(id uint64) {
	select {
	case r.commands <- detachSubscriptionCommand{id: id}:
	case <-r.done:
	}
}
func (r *runtime) detach(id uint64) {
	if sub, ok := r.subs[id]; ok {
		close(sub.events)
		delete(r.subs, id)
	}
}
func (r *runtime) detachOwner(owner session.ControllerID) {
	for id, sub := range r.subs {
		if sub.owner == owner {
			r.detach(id)
		}
	}
	// Transfer physical ownership into the stopped-bound cleanup queue before
	// releasing the lease. The actor is serialized, but this ordering makes the
	// hand-off explicit: a replacement frontend can never observe an available
	// lease while the detached frontend's records still look live.
	_, cleanupRequested := r.breakpoints.RequestCleanupSignal(breakpoint.Owner(owner))
	if r.reducer.Snapshot().LeaseOwner == owner {
		_ = r.reducer.ReleaseControl(owner)
	}
	if cleanupRequested {
		r.maybeScheduleBreakpointCleanup(r.reducer.Snapshot(), true)
	}
}
