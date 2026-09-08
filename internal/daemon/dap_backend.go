// Package daemon wires the frontend-neutral session actor to the DAP server.
// It deliberately owns no bridge or MULTI capability: every target operation
// is delegated to the actor's public session boundary.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

var (
	ErrPublisherNotBound = errors.New("daemon: DAP publisher is not bound")
	ErrPublisherBound    = errors.New("daemon: DAP publisher is already bound")
	ErrFrontendActive    = errors.New("daemon: DAP frontend is already attached")
)

// Core is the narrow actor contract consumed by the DAP integration. Keeping
// it here makes the boundary directly testable and prevents bridge/MULTI
// capabilities from leaking into daemon code.
type Core interface {
	Attach(context.Context, session.ControllerID) (actor.Attachment, error)
	Detach(context.Context, session.ControllerID) error
	AcquireControl(context.Context, session.ControllerID) error
	BeginConfiguration(context.Context) error
	ConfigurationDone(context.Context) error
	CancelConfiguration(context.Context) error
	Threads(context.Context) ([]actor.Thread, error)
	SetBreakpoints(context.Context, session.ControllerID, source.Identity, []int) (breakpoint.ReplaceResult, error)
	Stack(context.Context, int, inspection.Page) (inspection.Stack, error)
	Scopes(context.Context, int32) ([]inspection.Scope, error)
	Variables(context.Context, int32, inspection.Page, inspection.Format) (inspection.VariablePage, error)
	Evaluate(context.Context, int32, inspection.Expression, inspection.Format) (inspection.Variable, error)
	ExecuteResult(context.Context, session.ControllerID, actor.ExecutionRequest) (actor.ExecutionResult, error)
}

// Publisher is the asynchronous DAP delivery boundary. PublishTargetState
// returns false when the server cannot preserve the canonical event stream.
// In that case Backend drops the frontend lease rather than silently losing a
// transition.
type Publisher interface {
	PublishTargetState(dap.TargetState) bool
	PublishOutput(dap.OutputBody) bool
	Terminate() bool
}

// Backend implements dap.Backend for one DAP server. A DAP server is itself
// single-frontend, but Backend still protects its state because publisher and
// actor event goroutines run concurrently with DAP request handling.
type Backend struct {
	core                  Core
	sourcePaths           SourcePaths
	capabilities          dap.Capabilities
	defaultInspectionCore *uint64

	mu        sync.Mutex
	publisher Publisher
	nextOwner uint64
	active    *frontend
}

var _ dap.Backend = (*Backend)(nil)
var _ dap.CapabilityProvider = (*Backend)(nil)

// BackendOptions declares only capabilities that are wired in the concrete
// actor runtime. Parsing a request is not sufficient reason to advertise it.
type BackendOptions struct {
	SourcePaths           SourcePaths
	Capabilities          dap.Capabilities
	DefaultInspectionCore *uint64
}

// NewBackend constructs an unbound DAP backend. BindPublisher must be called
// exactly once after the DAP server exists and before configurationDone.
func NewBackend(core Core) (*Backend, error) {
	return NewBackendWithOptions(core, BackendOptions{})
}

func NewBackendWithOptions(core Core, options BackendOptions) (*Backend, error) {
	if core == nil {
		return nil, errors.New("daemon: actor core is required")
	}
	if options.SourcePaths == nil {
		var err error
		options.SourcePaths, err = NewSourcePaths(nil)
		if err != nil {
			return nil, err
		}
	}
	backend := &Backend{core: core, sourcePaths: options.SourcePaths, capabilities: options.Capabilities}
	if options.DefaultInspectionCore != nil {
		value := *options.DefaultInspectionCore
		backend.defaultInspectionCore = &value
	}
	return backend, nil
}

func (b *Backend) DAPCapabilities() dap.Capabilities { return b.capabilities }

// BindPublisher resolves the Server <-> Backend construction cycle. Rebinding
// would let an old frontend emit into a different server, so it is forbidden.
func (b *Backend) BindPublisher(publisher Publisher) error {
	if publisher == nil {
		return errors.New("daemon: DAP publisher is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.publisher != nil {
		return ErrPublisherBound
	}
	b.publisher = publisher
	return nil
}

// Execute translates DAP's small action vocabulary into actor operations.
// Commands deliberately only request control; target transitions come from
// actor observations and arrive through the frontend event pump.
func (b *Backend) Execute(ctx context.Context, action dap.Action) (dap.ActionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch action.Kind {
	case dap.ActionAttach:
		return b.attach(ctx, action.Initialize)
	case dap.ActionConfigurationDone:
		return dap.ActionResult{}, b.configurationDone(ctx)
	case dap.ActionThreads:
		return b.threads(ctx)
	case dap.ActionSetBreakpoints:
		return b.setBreakpoints(ctx, action.SetBreakpoints)
	case dap.ActionStackTrace:
		return b.stack(ctx, action.StackTrace)
	case dap.ActionScopes:
		return b.scopes(ctx, action.Scopes)
	case dap.ActionVariables:
		return b.variables(ctx, action.Variables)
	case dap.ActionEvaluate:
		return b.evaluate(ctx, action.Evaluate)
	case dap.ActionReadMemory:
		return b.readMemory(ctx, action.ReadMemory)
	case dap.ActionWriteMemory:
		return dap.ActionResult{}, errors.New("daemon: unsupported DAP action writeMemory")
	case dap.ActionDisassemble:
		return b.disassemble(ctx, action.Disassemble)
	case dap.ActionContinue:
		return b.resume(ctx, action.Continue.ThreadID)
	case dap.ActionPause:
		return b.halt(ctx, action.Pause.ThreadID)
	case dap.ActionStepIn:
		return b.stepIn(ctx, action.StepIn.ThreadID)
	case dap.ActionNext:
		return b.next(ctx, action.Next.ThreadID)
	case dap.ActionStepOut:
		return dap.ActionResult{}, errors.New("daemon: unsupported DAP action stepOut")
	case dap.ActionDisconnect:
		b.releaseActive()
		return dap.ActionResult{}, nil
	default:
		return dap.ActionResult{}, fmt.Errorf("daemon: unsupported DAP action %d", action.Kind)
	}
}

func (b *Backend) attach(ctx context.Context, initialize dap.InitializeArguments) (dap.ActionResult, error) {
	b.mu.Lock()
	if b.publisher == nil {
		b.mu.Unlock()
		return dap.ActionResult{}, ErrPublisherNotBound
	}
	if b.active != nil {
		b.mu.Unlock()
		return dap.ActionResult{}, ErrFrontendActive
	}
	b.nextOwner++
	owner := session.ControllerID(fmt.Sprintf("dap-frontend-%d", b.nextOwner))
	b.mu.Unlock()

	attachment, err := b.core.Attach(ctx, owner)
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: attach actor frontend: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		attachment.Close()
		// Detach is the actor's idempotent owner-scoped subscription and lease
		// release operation. It is deliberately issued even if Acquire failed.
		_ = b.core.Detach(context.Background(), owner)
	}()

	if err := b.core.AcquireControl(ctx, owner); err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: acquire DAP control lease: %w", err)
	}
	threadMap, threads, err := b.threadMap(ctx)
	if err != nil {
		return dap.ActionResult{}, err
	}

	f := &frontend{
		backend: b, owner: owner, attachment: attachment, threads: threadMap, orderedThreads: threads,
		done: make(chan struct{}), configuring: true, initialize: initialize,
	}
	if err := b.core.BeginConfiguration(ctx); err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: begin DAP configuration: %w", err)
	}
	b.mu.Lock()
	if b.active != nil {
		b.mu.Unlock()
		_ = b.core.CancelConfiguration(context.Background())
		return dap.ActionResult{}, ErrFrontendActive
	}
	b.active = f
	b.mu.Unlock()
	committed = true

	// Actor events are held by the configuration barrier until
	// ActionConfigurationDone, then delivered in actor order after DAP has
	// released its held attach response.
	go f.pump()
	return dap.ActionResult{Threads: threads}, nil
}

func (b *Backend) configurationDone(ctx context.Context) error {
	b.mu.Lock()
	f := b.active
	b.mu.Unlock()
	if f == nil {
		return errors.New("daemon: no attached DAP frontend")
	}
	// A fresh frontend must restart the bridge cursor before the actor releases
	// its configuration barrier. This closes the detach/reattach window where a
	// prior generation consumed a pane read but its DAP event was discarded.
	// Console forwarding is optional: reset failure must not block debugging.
	b.resetConsole(ctx, f)
	if err := b.core.ConfigurationDone(ctx); err != nil {
		return fmt.Errorf("daemon: finish DAP configuration: %w", err)
	}
	f.markConfigured()
	return nil
}

func (b *Backend) threads(ctx context.Context) (dap.ActionResult, error) {
	b.mu.Lock()
	f := b.active
	b.mu.Unlock()
	if f == nil {
		return dap.ActionResult{}, errors.New("daemon: no attached DAP frontend")
	}
	return dap.ActionResult{Threads: f.dapThreads()}, nil
}

func (b *Backend) setBreakpoints(ctx context.Context, arguments dap.SetBreakpointsArguments) (dap.ActionResult, error) {
	f, err := b.frontend()
	if err != nil {
		return dap.ActionResult{}, err
	}
	identity, err := b.sourcePaths.FromClient(f.initialize, arguments.Source)
	if err != nil {
		return dap.ActionResult{}, err
	}
	lines := make([]int, len(arguments.Breakpoints))
	for index, candidate := range arguments.Breakpoints {
		line, err := f.canonicalLine(candidate.Line)
		if err != nil {
			return dap.ActionResult{}, fmt.Errorf("daemon: breakpoint %d: %w", index, err)
		}
		lines[index] = line
	}
	result, err := b.core.SetBreakpoints(ctx, f.owner, identity, lines)
	if err != nil {
		if errors.Is(err, actor.ErrSourceUnavailable) {
			breakpoints, mapErr := f.unverifiedBreakpoints(identity, lines, err.Error())
			if mapErr != nil {
				return dap.ActionResult{}, mapErr
			}
			return dap.ActionResult{Breakpoints: breakpoints}, nil
		}
		return dap.ActionResult{}, fmt.Errorf("daemon: set breakpoints: %w", err)
	}
	breakpoints, err := f.dapBreakpoints(result.Breakpoints, lines)
	if err != nil {
		return dap.ActionResult{}, err
	}
	return dap.ActionResult{Breakpoints: breakpoints}, nil
}

func (b *Backend) stack(ctx context.Context, arguments dap.StackTraceArguments) (dap.ActionResult, error) {
	f, _, err := b.frontendForThread(arguments.ThreadID)
	if err != nil {
		return dap.ActionResult{}, err
	}
	page, err := dapPage(arguments.StartFrame, arguments.Levels)
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: stackTrace: %w", err)
	}
	result, err := b.core.Stack(ctx, int(arguments.ThreadID), page)
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: stack trace: %w", err)
	}
	frames, err := f.dapFrames(result.Frames)
	if err != nil {
		return dap.ActionResult{}, err
	}
	return dap.ActionResult{StackFrames: frames, TotalFrames: result.Total}, nil
}

func (b *Backend) scopes(ctx context.Context, arguments dap.ScopesArguments) (dap.ActionResult, error) {
	f, err := b.frontend()
	if err != nil {
		return dap.ActionResult{}, err
	}
	result, err := b.core.Scopes(ctx, arguments.FrameID)
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: scopes: %w", err)
	}
	return dap.ActionResult{Scopes: f.dapScopes(result)}, nil
}

func (b *Backend) variables(ctx context.Context, arguments dap.VariablesArguments) (dap.ActionResult, error) {
	_, err := b.frontend()
	if err != nil {
		return dap.ActionResult{}, err
	}
	page, err := dapPage(arguments.Start, arguments.Count)
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: variables: %w", err)
	}
	requestPage := page
	if arguments.Filter != "" {
		// DAP paging is scoped to the selected named or indexed child set. The
		// inspection service owns the raw snapshot, so obtain its complete view
		// before filtering instead of paging the mixed set and guessing later.
		requestPage = inspection.Page{Count: math.MaxInt}
	}
	result, err := b.core.Variables(ctx, arguments.VariablesReference, requestPage, dapFormat(arguments.Format))
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: variables: %w", err)
	}
	values, err := filteredVariables(result.Variables, arguments.Filter)
	if err != nil {
		return dap.ActionResult{}, err
	}
	if arguments.Filter != "" {
		values = variablePage(values, page)
	}
	return dap.ActionResult{Variables: dapVariables(values)}, nil
}

func filteredVariables(values []inspection.Variable, filter string) ([]inspection.Variable, error) {
	if filter == "" {
		return values, nil
	}
	want := inspection.ChildNamed
	switch filter {
	case "named":
	case "indexed":
		want = inspection.ChildIndexed
	default:
		return nil, fmt.Errorf("daemon: variables filter %q is unsupported", filter)
	}
	result := make([]inspection.Variable, 0, len(values))
	for _, value := range values {
		if value.ChildKind != inspection.ChildNamed && value.ChildKind != inspection.ChildIndexed {
			return nil, fmt.Errorf("daemon: variables contain unknown child kind %d", value.ChildKind)
		}
		if value.ChildKind == want {
			result = append(result, value)
		}
	}
	return result, nil
}

func variablePage(values []inspection.Variable, page inspection.Page) []inspection.Variable {
	if page.Start >= len(values) {
		return nil
	}
	end := page.Start + page.Count
	if end > len(values) {
		end = len(values)
	}
	return values[page.Start:end]
}

func (b *Backend) evaluate(ctx context.Context, arguments dap.EvaluateArguments) (dap.ActionResult, error) {
	f, err := b.frontend()
	if err != nil {
		return dap.ActionResult{}, err
	}
	expression, err := inspection.NewExpression(arguments.Expression)
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: evaluate: %w", err)
	}
	frameID := arguments.FrameID
	if frameID == 0 {
		frameID, err = b.defaultEvaluationFrame(ctx, f)
		if err != nil {
			return dap.ActionResult{}, err
		}
	}
	result, err := b.core.Evaluate(ctx, frameID, expression, dapFormat(arguments.Format))
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: evaluate: %w", err)
	}
	return dap.ActionResult{Evaluation: dapEvaluate(result)}, nil
}

// defaultEvaluationFrame resolves DAP's optional evaluate frameId to the top
// frame of the frontend's deterministic primary configured thread. Stack
// returns a stop-bound handle, so the actor continues to reject evaluations
// after a resume or against a stale suspended world.
func (b *Backend) defaultEvaluationFrame(ctx context.Context, f *frontend) (int32, error) {
	if len(f.orderedThreads) == 0 {
		return 0, errors.New("daemon: no configured DAP threads for evaluation")
	}
	stack, err := b.core.Stack(ctx, int(f.orderedThreads[0].ID), inspection.Page{Count: 1})
	if err != nil {
		return 0, fmt.Errorf("daemon: resolve default evaluation frame: %w", err)
	}
	if len(stack.Frames) == 0 || stack.Frames[0].ID <= 0 {
		return 0, errors.New("daemon: default evaluation thread has no top frame")
	}
	return stack.Frames[0].ID, nil
}

func (b *Backend) resume(ctx context.Context, threadID int32) (dap.ActionResult, error) {
	f, thread, err := b.frontendForThread(threadID)
	if err != nil {
		return dap.ActionResult{}, err
	}
	result, err := b.core.ExecuteResult(ctx, f.owner, actor.ExecutionRequest{CoreID: thread.CoreID, Operation: multi.ExecutionContinue})
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: resume target: %w", err)
	}
	return dap.ActionResult{Execution: dap.ExecutionResult{AllThreadsContinued: result.Truth.AllThreadsContinued, AllThreadsStopped: result.Truth.AllThreadsStopped}}, nil
}

func (b *Backend) halt(ctx context.Context, threadID int32) (dap.ActionResult, error) {
	f, thread, err := b.frontendForThread(threadID)
	if err != nil {
		return dap.ActionResult{}, err
	}
	result, err := b.core.ExecuteResult(ctx, f.owner, actor.ExecutionRequest{CoreID: thread.CoreID, Operation: multi.ExecutionPause})
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: halt target: %w", err)
	}
	return dap.ActionResult{Execution: dap.ExecutionResult{AllThreadsContinued: result.Truth.AllThreadsContinued, AllThreadsStopped: result.Truth.AllThreadsStopped}}, nil
}

func (b *Backend) stepIn(ctx context.Context, threadID int32) (dap.ActionResult, error) {
	f, thread, err := b.frontendForThread(threadID)
	if err != nil {
		return dap.ActionResult{}, err
	}
	result, err := b.core.ExecuteResult(ctx, f.owner, actor.ExecutionRequest{CoreID: thread.CoreID, Operation: multi.ExecutionStepIn})
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: step into target: %w", err)
	}
	return dap.ActionResult{Execution: dap.ExecutionResult{AllThreadsContinued: result.Truth.AllThreadsContinued, AllThreadsStopped: result.Truth.AllThreadsStopped}}, nil
}

func (b *Backend) next(ctx context.Context, threadID int32) (dap.ActionResult, error) {
	f, thread, err := b.frontendForThread(threadID)
	if err != nil {
		return dap.ActionResult{}, err
	}
	result, err := b.core.ExecuteResult(ctx, f.owner, actor.ExecutionRequest{CoreID: thread.CoreID, Operation: multi.ExecutionNext})
	if err != nil {
		return dap.ActionResult{}, fmt.Errorf("daemon: step over target: %w", err)
	}
	return dap.ActionResult{Execution: dap.ExecutionResult{AllThreadsContinued: result.Truth.AllThreadsContinued, AllThreadsStopped: result.Truth.AllThreadsStopped}}, nil
}

func (b *Backend) frontendForThread(threadID int32) (*frontend, actor.Thread, error) {
	b.mu.Lock()
	f := b.active
	b.mu.Unlock()
	if f == nil {
		return nil, actor.Thread{}, errors.New("daemon: no attached DAP frontend")
	}
	thread, ok := f.threads[threadID]
	if !ok {
		return nil, actor.Thread{}, fmt.Errorf("daemon: unknown DAP thread %d", threadID)
	}
	return f, thread, nil
}

func (b *Backend) threadMap(ctx context.Context) (map[int32]actor.Thread, []dap.Thread, error) {
	actorThreads, err := b.core.Threads(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: read actor threads: %w", err)
	}
	if len(actorThreads) == 0 {
		return nil, nil, errors.New("daemon: actor has no debug threads")
	}
	result := make(map[int32]actor.Thread, len(actorThreads))
	threads := make([]dap.Thread, 0, len(actorThreads))
	for _, thread := range actorThreads {
		if thread.ID <= 0 || int64(thread.ID) > math.MaxInt32 {
			return nil, nil, fmt.Errorf("daemon: actor thread ID %d cannot be represented by DAP", thread.ID)
		}
		id := int32(thread.ID)
		if _, exists := result[id]; exists {
			return nil, nil, fmt.Errorf("daemon: duplicate actor thread ID %d", thread.ID)
		}
		result[id] = thread
		threads = append(threads, dap.Thread{ID: id, Name: thread.Name})
	}
	return result, threads, nil
}

// Close releases the active frontend without issuing a target command. It is
// intended for daemon shutdown and is safe to race a DAP disconnect.
func (b *Backend) Close() { b.releaseActive() }

func (b *Backend) releaseActive() {
	b.mu.Lock()
	f := b.active
	if f != nil {
		b.active = nil
	}
	b.mu.Unlock()
	if f != nil {
		f.release()
	}
}

type deliveryResult uint8

const (
	deliveryStale deliveryResult = iota
	deliveryAccepted
	deliveryFailed
)

// deliver fences publication and frontend identity under one lock. Publisher
// methods are required to be non-blocking; dap.Server only offers into a
// bounded queue. Holding the lock prevents a late old-generation pump from
// publishing into a newly attached client.
func (b *Backend) deliver(f *frontend, state dap.TargetState) deliveryResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != f {
		return deliveryStale
	}
	if b.publisher.PublishTargetState(state) {
		return deliveryAccepted
	}
	return deliveryFailed
}

const (
	consoleLossyNotice       = "[MULTI console output contained invalid UTF-8; replacement characters were used]\n"
	consoleTruncatedNotice   = "[MULTI console output was truncated]\n"
	consoleUnavailableNotice = "[MULTI console forwarding disabled for this session]\n"
)

// deliverOutput shares target-state delivery's frontend identity fence and
// bounded publication semantics. An old actor generation therefore cannot
// write console text into a replacement DAP session.
func (b *Backend) deliverOutput(f *frontend, event actor.Event) deliveryResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != f {
		return deliveryStale
	}
	outputs := make([]dap.OutputBody, 0, 4)
	if event.ConsoleUnavailable {
		outputs = append(outputs, dap.OutputBody{Category: "stderr", Output: consoleUnavailableNotice})
	} else {
		if event.Console.Server != "" {
			// CLion's Cidr DAP frontend hides category=console from its process
			// Console.  stdout keeps GHS target-server diagnostics visible there.
			outputs = append(outputs, dap.OutputBody{Category: "stdout", Output: event.Console.Server})
		}
		if event.Console.IO != "" {
			outputs = append(outputs, dap.OutputBody{Category: "stdout", Output: event.Console.IO})
		}
		if event.Console.RawLossy {
			outputs = append(outputs, dap.OutputBody{Category: "stderr", Output: consoleLossyNotice})
		}
		if event.Console.Truncated {
			outputs = append(outputs, dap.OutputBody{Category: "stderr", Output: consoleTruncatedNotice})
		}
	}
	for _, output := range outputs {
		if !b.publisher.PublishOutput(output) {
			return deliveryFailed
		}
	}
	return deliveryAccepted
}

type consoleResetter interface {
	ResetConsole(context.Context) error
}

// resetConsole is deliberately best-effort. The lock protects only the
// active-frontend check and once flag; ResetConsole crosses the actor/bridge
// boundary and must not block detach or frontend replacement on b.mu.
func (b *Backend) resetConsole(ctx context.Context, f *frontend) {
	resetter, ok := b.core.(consoleResetter)
	if !ok {
		return
	}
	b.mu.Lock()
	if b.active != f || f.consoleReset {
		b.mu.Unlock()
		return
	}
	f.consoleReset = true
	b.mu.Unlock()
	_ = resetter.ResetConsole(ctx)
}

func (b *Backend) fail(f *frontend) {
	b.mu.Lock()
	if b.active != f {
		b.mu.Unlock()
		f.release()
		return
	}
	if b.publisher != nil {
		_ = b.publisher.Terminate()
	}
	b.active = nil
	b.mu.Unlock()
	f.release()
}

type frontend struct {
	backend         *Backend
	owner           session.ControllerID
	attachment      actor.Attachment
	threads         map[int32]actor.Thread
	orderedThreads  []dap.Thread
	initialize      dap.InitializeArguments
	done            chan struct{}
	once            sync.Once
	configurationMu sync.Mutex
	configuring     bool
	consoleReset    bool
}

func (f *frontend) dapThreads() []dap.Thread {
	return append([]dap.Thread(nil), f.orderedThreads...)
}

func (b *Backend) frontend() (*frontend, error) {
	b.mu.Lock()
	f := b.active
	b.mu.Unlock()
	if f == nil {
		return nil, errors.New("daemon: no attached DAP frontend")
	}
	return f, nil
}

func (f *frontend) canonicalLine(line int64) (int, error) {
	if !f.initialize.LinesStartAt1 {
		if line == math.MaxInt64 {
			return 0, errors.New("line overflows canonical numbering")
		}
		line++
	}
	if line <= 0 || line > int64(math.MaxInt) {
		return 0, fmt.Errorf("line %d is outside the canonical range", line)
	}
	return int(line), nil
}

func (f *frontend) dapLine(line int64) int64 {
	if !f.initialize.LinesStartAt1 && line > 0 {
		return line - 1
	}
	return line
}

func (f *frontend) dapColumn(column int64) int64 {
	if !f.initialize.ColumnsStartAt1 && column > 0 {
		return column - 1
	}
	return column
}

func (f *frontend) dapSource(identity source.Identity) (*dap.Source, error) {
	return f.backend.sourcePaths.ToClient(f.initialize, identity)
}

func (f *frontend) dapBreakpoints(logical []breakpoint.Logical, requested []int) ([]dap.Breakpoint, error) {
	byLine := make(map[int]dap.Breakpoint, len(logical))
	for _, candidate := range logical {
		if candidate.DAPID <= 0 || int64(candidate.DAPID) > math.MaxInt32 {
			return nil, fmt.Errorf("daemon: breakpoint ID %d cannot be represented by DAP", candidate.DAPID)
		}
		if _, exists := byLine[candidate.Line]; exists {
			return nil, fmt.Errorf("daemon: actor returned duplicate result for source line %d", candidate.Line)
		}
		mappedSource, err := f.dapSource(candidate.Source)
		if err != nil {
			return nil, fmt.Errorf("daemon: map breakpoint source: %w", err)
		}
		byLine[candidate.Line] = dap.Breakpoint{
			ID: int32(candidate.DAPID), Verified: candidate.Verified, Message: candidate.Message,
			Source: mappedSource, Line: f.dapLine(int64(candidate.Line)),
		}
	}
	result := make([]dap.Breakpoint, 0, len(requested))
	for _, line := range requested {
		candidate, ok := byLine[line]
		if !ok {
			return nil, fmt.Errorf("daemon: actor did not return a breakpoint result for source line %d", line)
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (f *frontend) unverifiedBreakpoints(identity source.Identity, requested []int, message string) ([]dap.Breakpoint, error) {
	mappedSource, err := f.dapSource(identity)
	if err != nil {
		return nil, fmt.Errorf("daemon: map unverified breakpoint source: %w", err)
	}
	result := make([]dap.Breakpoint, 0, len(requested))
	for _, line := range requested {
		result = append(result, dap.Breakpoint{
			Verified: false, Message: message, Source: mappedSource, Line: f.dapLine(int64(line)),
		})
	}
	return result, nil
}

func (f *frontend) dapFrames(frames []inspection.Frame) ([]dap.StackFrame, error) {
	result := make([]dap.StackFrame, 0, len(frames))
	for _, frame := range frames {
		if frame.ID <= 0 {
			return nil, fmt.Errorf("daemon: frame ID %d is invalid", frame.ID)
		}
		value := dap.StackFrame{ID: frame.ID, Name: frame.Name}
		if frame.HasInstructionAddress {
			if frame.InstructionAddress.Core < 0 {
				return nil, fmt.Errorf("daemon: frame %d has negative instruction core %d", frame.ID, frame.InstructionAddress.Core)
			}
			value.InstructionPointerReference = formatMemoryReference(uint64(frame.InstructionAddress.Core), frame.InstructionAddress.StopEpoch, frame.InstructionAddress.Address)
		}
		if frame.Source != nil {
			mappedSource, err := f.dapSource(frame.Source.Identity)
			if err != nil {
				return nil, fmt.Errorf("daemon: map stack source: %w", err)
			}
			value.Source = mappedSource
			value.Line = f.dapLine(frame.Source.Line)
			value.Column = f.dapColumn(frame.Source.Column)
		}
		result = append(result, value)
	}
	return result, nil
}

func (f *frontend) dapScopes(scopes []inspection.Scope) []dap.Scope {
	result := make([]dap.Scope, 0, len(scopes))
	for _, scope := range scopes {
		result = append(result, dap.Scope{
			Name: scope.Name, PresentationHint: dapScopeHint(scope.Kind), VariablesReference: scope.VariablesReference, Expensive: scope.Expensive,
		})
	}
	return result
}

func dapScopeHint(kind inspection.ScopeKind) string {
	switch kind {
	case inspection.ScopeLocals:
		return "locals"
	case inspection.ScopeGlobals:
		return "globals"
	case inspection.ScopeRegisters:
		return "registers"
	default:
		return ""
	}
}

func dapVariables(values []inspection.Variable) []dap.Variable {
	result := make([]dap.Variable, 0, len(values))
	for _, value := range values {
		item := dap.Variable{
			Name: value.Name, EvaluateName: value.EvaluateName, Value: value.Value, Type: value.Type, VariablesReference: value.VariablesReference,
		}
		if value.NamedChildren >= 0 {
			item.NamedVariables = value.NamedChildren
		}
		if value.IndexedChildren >= 0 {
			item.IndexedVariables = value.IndexedChildren
		}
		result = append(result, item)
	}
	return result
}

func dapEvaluate(value inspection.Variable) dap.EvaluateBody {
	result := dap.EvaluateBody{Result: value.Value, Type: value.Type, VariablesReference: value.VariablesReference}
	if value.NamedChildren >= 0 {
		result.NamedVariables = value.NamedChildren
	}
	if value.IndexedChildren >= 0 {
		result.IndexedVariables = value.IndexedChildren
	}
	return result
}

func dapFormat(format dap.ValueFormat) inspection.Format {
	if format.Hex {
		return inspection.FormatHexadecimal
	}
	return inspection.FormatDefault
}

func dapPage(start, count uint32) (inspection.Page, error) {
	if uint64(start) > uint64(math.MaxInt) || uint64(count) > uint64(math.MaxInt) {
		return inspection.Page{}, errors.New("page is outside the platform range")
	}
	page := inspection.Page{Start: int(start), Count: int(count)}
	if page.Count == 0 {
		page.Count = math.MaxInt - page.Start
	}
	if page.Count < 0 || page.Count > math.MaxInt-page.Start {
		return inspection.Page{}, errors.New("page overflows")
	}
	return page, nil
}

func (f *frontend) release() {
	f.once.Do(func() {
		close(f.done)
		f.configurationMu.Lock()
		configuring := f.configuring
		f.configuring = false
		f.configurationMu.Unlock()
		if configuring {
			_ = f.backend.core.CancelConfiguration(context.Background())
		}
		f.attachment.Close()
		_ = f.backend.core.Detach(context.Background(), f.owner)
	})
}

func (f *frontend) markConfigured() {
	f.configurationMu.Lock()
	f.configuring = false
	f.configurationMu.Unlock()
}

func (f *frontend) pump() {
	for {
		select {
		case <-f.done:
			return
		case event, ok := <-f.attachment.Events:
			if !ok {
				f.fail()
				return
			}
			switch event.Kind {
			case actor.EventInvalidated:
				// DAP M1 owns no suspended-reference requests yet.
			case actor.EventResumed, actor.EventStopped:
				switch f.backend.deliver(f, f.targetState(event)) {
				case deliveryAccepted:
					continue
				case deliveryStale:
					return
				case deliveryFailed:
					f.fail()
					return
				default:
					f.fail()
					return
				}
			case actor.EventReconciliationRequired:
				f.fail()
				return
			case actor.EventOutput:
				switch f.backend.deliverOutput(f, event) {
				case deliveryAccepted:
					continue
				case deliveryStale:
					return
				default:
					f.fail()
					return
				}
			default:
				f.fail()
				return
			}
		}
	}
}

func (f *frontend) targetState(event actor.Event) dap.TargetState {
	fallback := f.orderedThreads[0].ID
	if event.ThreadID > 0 && int64(event.ThreadID) <= math.MaxInt32 {
		id := int32(event.ThreadID)
		if _, exists := f.threads[id]; exists {
			fallback = id
		}
	}
	return targetState(event, fallback)
}

func (f *frontend) fail() {
	f.backend.fail(f)
}

func targetState(event actor.Event, fallback int32) dap.TargetState {
	switch event.Snapshot.State {
	case session.TargetRunning:
		threadID := event.ThreadID
		if threadID <= 0 || int64(threadID) > math.MaxInt32 {
			threadID = int(fallback)
		}
		id := int32(threadID)
		return dap.TargetState{Execution: dap.TargetRunning, Continued: dap.ContinuedBody{ThreadID: id, AllThreadsContinued: event.AllThreadsContinued}}
	case session.TargetStopped:
		var id int32
		if event.ThreadID > 0 && int64(event.ThreadID) <= math.MaxInt32 {
			id = int32(event.ThreadID)
		}
		reason := string(event.Reason)
		if reason == "" {
			reason = "unknown"
		}
		hits := make([]int32, 0, len(event.HitBreakpointIDs))
		for _, breakpointID := range event.HitBreakpointIDs {
			if breakpointID > 0 && int64(breakpointID) <= math.MaxInt32 {
				hits = append(hits, int32(breakpointID))
			}
		}
		return dap.TargetState{Execution: dap.TargetStopped, Stopped: dap.StoppedBody{
			Reason: reason, ThreadID: id, HitBreakpointIDs: hits, AllThreadsStopped: event.AllThreadsStopped,
		}}
	default:
		return dap.TargetState{Execution: dap.TargetUnknown}
	}
}
