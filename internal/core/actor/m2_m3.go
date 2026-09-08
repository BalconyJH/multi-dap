package actor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/core/stop"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

var (
	ErrBreakpointsUnavailable = errors.New("actor: breakpoint placement is unavailable")
	ErrSourceUnavailable      = errors.New("actor: source is unavailable on every core")
	ErrSourceUncertain        = errors.New("actor: source resolution is uncertain")
	ErrInspectionUnavailable  = errors.New("actor: inspection is unavailable")
)

// SourceIndex is the core-owned, per-configured-core source coverage
// boundary. Its values must originate from authoritative MULTI observations
// or configured ELF metadata; Actor never infers a core from a source path.
type SourceIndex interface {
	Coverage(key string) []source.Observation
}

// SourceResolver is the primary source-to-core lookup boundary for targets
// whose ELF has no DWARF. Resolve executes under Actor's executor ownership,
// so probing cannot open a competing bridge transport. A result that leaves
// any configured core unknown must be rejected by Actor rather than treated
// as an absent source.
type SourceResolver interface {
	Resolve(context.Context, *bridge.Client, source.Identity) ([]source.Observation, error)
}

// BreakpointRunner is the only side-effect seam used by M2 placement. The
// executor supplies its bridge client at call time, preventing a placement
// backend from opening a competing transport or bypassing generation fences.
type BreakpointRunner interface {
	Set(context.Context, *bridge.Client, breakpoint.PlacementRequest) (breakpoint.Physical, error)
	Clear(context.Context, *bridge.Client, breakpoint.Physical) error
}

// MULTIBreakpointRunner binds M2 placement to the same immutable topology as
// M3. BindMULTITopology runs during Actor.Open on the bridge generation which
// resolved that topology; later operations cannot re-enumerate components.
type MULTIBreakpointRunner struct {
	mu     sync.Mutex
	placer *multi.BreakpointPlacer
	bound  bool
}

func (r *MULTIBreakpointRunner) BindMULTITopology(topology *multi.Topology) error {
	if r == nil {
		return errors.New("actor: nil MULTI breakpoint runner")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bound {
		return errors.New("actor: MULTI breakpoint runner topology is already bound")
	}
	placer, err := multi.NewBreakpointPlacer(topology)
	if err != nil {
		return err
	}
	r.placer = placer
	r.bound = true
	return nil
}

func (r *MULTIBreakpointRunner) Set(ctx context.Context, client *bridge.Client, request breakpoint.PlacementRequest) (breakpoint.Physical, error) {
	if r == nil {
		return breakpoint.Physical{}, ErrBreakpointsUnavailable
	}
	r.mu.Lock()
	placer := r.placer
	r.mu.Unlock()
	if placer == nil {
		return breakpoint.Physical{}, ErrBreakpointsUnavailable
	}
	driver, err := multi.NewDriver(client)
	if err != nil {
		return breakpoint.Physical{}, err
	}
	return placer.Set(ctx, driver, request)
}

func (r *MULTIBreakpointRunner) Clear(ctx context.Context, client *bridge.Client, physical breakpoint.Physical) error {
	if r == nil {
		return ErrBreakpointsUnavailable
	}
	r.mu.Lock()
	placer := r.placer
	r.mu.Unlock()
	if placer == nil {
		return ErrBreakpointsUnavailable
	}
	driver, err := multi.NewDriver(client)
	if err != nil {
		return err
	}
	return placer.Clear(ctx, driver, physical)
}

// InspectionRunner is M3's bridge-bound backend seam. It contains typed
// inspection operands only, never MULTI command text. It is intentionally
// separate from inspection.Backend because only Actor may decide when an RPC
// starts and which executor generation owns it.
type InspectionRunner interface {
	Stack(context.Context, *bridge.Client, inspection.CoreID) ([]inspection.RawFrame, error)
	Scopes(context.Context, *bridge.Client, inspection.CoreID, uint64) ([]inspection.RawScope, error)
	Variables(context.Context, *bridge.Client, inspection.CoreID, inspection.ScopeLocator, inspection.Page, inspection.Format) (inspection.RawValuePage, error)
	Children(context.Context, *bridge.Client, inspection.CoreID, inspection.ValueLocator, inspection.Page, inspection.Format) (inspection.RawValuePage, error)
	Evaluate(context.Context, *bridge.Client, inspection.CoreID, uint64, inspection.Expression, inspection.Format) (inspection.RawValue, error)
}

// SourceMapper is the one M3 path boundary. It aliases inspection's contract
// so Options reads at the actor layer without exposing its implementation.
type SourceMapper = inspection.SourceMapper

// SetBreakpoints applies DAP replacement semantics for one canonical source.
// It may only be called by the current control-lease owner. The full physical
// transaction runs as one executor operation, so Store.Replace cannot
// interleave with a state observation or another target mutation.
func (a *Actor) SetBreakpoints(ctx context.Context, owner session.ControllerID, identity source.Identity, lines []int) (breakpoint.ReplaceResult, error) {
	ch := make(chan reply[breakpoint.ReplaceResult], 1)
	if err := a.send(ctx, setBreakpointsCommand{owner: owner, identity: identity, lines: append([]int(nil), lines...), reply: ch}); err != nil {
		return breakpoint.ReplaceResult{}, err
	}
	return await(ctx, a.done, ch, breakpoint.ReplaceResult{})
}

// NoteHint accepts an already authenticated UDP token. Unknown and retired
// tokens are intentionally ignored. A valid token only queues an immediate
// state sample; it never manufactures a stopped event on its own.
func (a *Actor) NoteHint(ctx context.Context, token uint32) error {
	ch := make(chan reply[struct{}], 1)
	if err := a.send(ctx, noteHintCommand{token: token, reply: ch}); err != nil {
		return err
	}
	_, err := await(ctx, a.done, ch, struct{}{})
	return err
}

// Stack returns a stop-bound page for one stable frontend thread id.
func (a *Actor) Stack(ctx context.Context, threadID int, page inspection.Page) (inspection.Stack, error) {
	ch := make(chan reply[inspection.Stack], 1)
	if err := a.send(ctx, stackCommand{threadID: threadID, page: page, reply: ch}); err != nil {
		return inspection.Stack{}, err
	}
	return await(ctx, a.done, ch, inspection.Stack{})
}

// Scopes returns lazy variable roots for a frame handle from the current stop.
func (a *Actor) Scopes(ctx context.Context, frameID int32) ([]inspection.Scope, error) {
	ch := make(chan reply[[]inspection.Scope], 1)
	if err := a.send(ctx, scopesCommand{frameID: frameID, reply: ch}); err != nil {
		return nil, err
	}
	return await(ctx, a.done, ch, []inspection.Scope(nil))
}

// Variables resolves one scope or variable reference in the current stop.
func (a *Actor) Variables(ctx context.Context, reference int32, page inspection.Page, format inspection.Format) (inspection.VariablePage, error) {
	ch := make(chan reply[inspection.VariablePage], 1)
	if err := a.send(ctx, variablesCommand{reference: reference, page: page, format: format, reply: ch}); err != nil {
		return inspection.VariablePage{}, err
	}
	return await(ctx, a.done, ch, inspection.VariablePage{})
}

// Evaluate evaluates a validated expression in a stop-bound frame.
func (a *Actor) Evaluate(ctx context.Context, frameID int32, expression inspection.Expression, format inspection.Format) (inspection.Variable, error) {
	ch := make(chan reply[inspection.Variable], 1)
	if err := a.send(ctx, evaluateCommand{frameID: frameID, expression: expression, format: format, reply: ch}); err != nil {
		return inspection.Variable{}, err
	}
	return await(ctx, a.done, ch, inspection.Variable{})
}

type setBreakpointsCommand struct {
	owner    session.ControllerID
	identity source.Identity
	lines    []int
	reply    chan reply[breakpoint.ReplaceResult]
}

func (c setBreakpointsCommand) apply(r *runtime) {
	if !r.opened {
		respond(c.reply, breakpoint.ReplaceResult{}, ErrNotOpen)
		return
	}
	if r.faulted {
		respond(c.reply, breakpoint.ReplaceResult{}, ErrReconciliationNeeded)
		return
	}
	if err := r.reducer.RequireControl(c.owner); err != nil {
		respond(c.reply, breakpoint.ReplaceResult{}, err)
		return
	}
	if r.opts.BreakpointRunner == nil {
		respond(c.reply, breakpoint.ReplaceResult{}, ErrBreakpointsUnavailable)
		return
	}
	lines := append([]int(nil), c.lines...)
	// An empty DAP replacement only removes already owned physical breakpoints.
	// Their core identities are retained in the Store, so resolving the source
	// again would make a safe clear depend on a query that is unnecessary here.
	if len(lines) == 0 {
		r.enqueue(r.breakpointWork(c.owner, c.identity, lines, nil, c.reply))
		return
	}
	if r.opts.SourceIndex == nil {
		respond(c.reply, breakpoint.ReplaceResult{}, ErrSourceUncertain)
		return
	}
	coverage := r.opts.SourceIndex.Coverage(c.identity.Key)
	expected, err := coverageSet(coverage)
	if err != nil {
		respond(c.reply, breakpoint.ReplaceResult{}, err)
		return
	}
	if coverageHasUnknown(coverage) {
		if r.opts.SourceResolver == nil {
			respond(c.reply, breakpoint.ReplaceResult{}, ErrSourceUncertain)
			return
		}
		r.enqueue(r.sourceResolutionWork(c.owner, c.identity, lines, coverage, expected, c.reply))
		return
	}
	cores, err := resolvedCores(coverage)
	if err != nil {
		respond(c.reply, breakpoint.ReplaceResult{}, err)
		return
	}
	r.enqueue(r.breakpointWork(c.owner, c.identity, lines, cores, c.reply))
}

func (r *runtime) sourceResolutionWork(owner session.ControllerID, identity source.Identity, lines []int, coverage []source.Observation, expected map[int]struct{}, reply chan reply[breakpoint.ReplaceResult]) rpcWork {
	return rpcWork{kind: opBreakpoints, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		observations, err := r.opts.SourceResolver.Resolve(ctx, client, identity)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSourceUncertain, err)
		}
		if err := matchesCoverage(expected, observations); err != nil {
			return nil, err
		}
		cores, err := resolvedCores(mergeCoverage(coverage, observations))
		if err != nil {
			return nil, err
		}
		placer := actorPlacer{runner: r.opts.BreakpointRunner, client: client}
		return r.breakpoints.ReplaceForOwner(ctx, placer, breakpoint.Owner(owner), identity, lines, cores)
	}, done: func(_ *runtime, value any, err error) {
		if err != nil {
			respond(reply, breakpoint.ReplaceResult{}, err)
			return
		}
		result, ok := value.(breakpoint.ReplaceResult)
		if !ok {
			respond(reply, breakpoint.ReplaceResult{}, errors.New("actor: breakpoint result has wrong type"))
			return
		}
		respond(reply, result, nil)
	}}
}

func coverageSet(observations []source.Observation) (map[int]struct{}, error) {
	if len(observations) == 0 {
		return nil, ErrSourceUncertain
	}
	expected := make(map[int]struct{}, len(observations))
	for _, observation := range observations {
		if observation.Core < 0 {
			return nil, fmt.Errorf("%w: negative core %d", ErrSourceUncertain, observation.Core)
		}
		if _, duplicate := expected[observation.Core]; duplicate {
			return nil, fmt.Errorf("%w: duplicate core %d", ErrSourceUncertain, observation.Core)
		}
		expected[observation.Core] = struct{}{}
	}
	return expected, nil
}

func coverageHasUnknown(observations []source.Observation) bool {
	for _, observation := range observations {
		if observation.Presence == source.PresenceUnknown {
			return true
		}
	}
	return false
}

// matchesCoverage rejects a resolver result unless it has exactly one
// observation for every configured core. An omitted or unexpected core would
// make a logical multi-core breakpoint non-atomic.
func matchesCoverage(expected map[int]struct{}, observations []source.Observation) error {
	if len(observations) != len(expected) {
		return fmt.Errorf("%w: resolver returned %d core observations for %d configured cores", ErrSourceUncertain, len(observations), len(expected))
	}
	seen := make(map[int]struct{}, len(observations))
	for _, observation := range observations {
		if _, configured := expected[observation.Core]; !configured {
			return fmt.Errorf("%w: resolver returned unconfigured core %d", ErrSourceUncertain, observation.Core)
		}
		if _, duplicate := seen[observation.Core]; duplicate {
			return fmt.Errorf("%w: resolver returned duplicate core %d", ErrSourceUncertain, observation.Core)
		}
		seen[observation.Core] = struct{}{}
	}
	return nil
}

// mergeCoverage fills only uncertain entries from the resolver. DWARF hits
// are positive evidence and must survive a later partial scan or resolver
// disagreement; a complete DWARF non-match remains authoritative as absent.
func mergeCoverage(index, resolver []source.Observation) []source.Observation {
	resolved := make(map[int]source.Presence, len(resolver))
	for _, observation := range resolver {
		resolved[observation.Core] = observation.Presence
	}
	merged := append([]source.Observation(nil), index...)
	for i := range merged {
		if merged[i].Presence == source.PresenceUnknown {
			merged[i].Presence = resolved[merged[i].Core]
		}
	}
	return merged
}

func resolvedCores(observations []source.Observation) ([]int, error) {
	if len(observations) == 0 {
		return nil, ErrSourceUncertain
	}
	cores := make([]int, 0, len(observations))
	seen := make(map[int]struct{}, len(observations))
	for _, observation := range observations {
		if observation.Core < 0 {
			return nil, fmt.Errorf("%w: negative core %d", ErrSourceUncertain, observation.Core)
		}
		if _, duplicate := seen[observation.Core]; duplicate {
			return nil, fmt.Errorf("%w: duplicate core %d", ErrSourceUncertain, observation.Core)
		}
		seen[observation.Core] = struct{}{}
		switch observation.Presence {
		case source.PresencePresent:
			cores = append(cores, observation.Core)
		case source.PresenceAbsent:
		case source.PresenceUnknown:
			return nil, fmt.Errorf("%w: core %d", ErrSourceUncertain, observation.Core)
		default:
			return nil, fmt.Errorf("%w: core %d returned %s", ErrSourceUncertain, observation.Core, observation.Presence)
		}
	}
	if len(cores) == 0 {
		return nil, ErrSourceUnavailable
	}
	sort.Ints(cores)
	return cores, nil
}

func (r *runtime) breakpointWork(owner session.ControllerID, identity source.Identity, lines, cores []int, reply chan reply[breakpoint.ReplaceResult]) rpcWork {
	return rpcWork{kind: opBreakpoints, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		placer := actorPlacer{runner: r.opts.BreakpointRunner, client: client}
		return r.breakpoints.ReplaceForOwner(ctx, placer, breakpoint.Owner(owner), identity, lines, cores)
	}, done: func(_ *runtime, value any, err error) {
		if err != nil {
			respond(reply, breakpoint.ReplaceResult{}, err)
			return
		}
		result, ok := value.(breakpoint.ReplaceResult)
		if !ok {
			respond(reply, breakpoint.ReplaceResult{}, errors.New("actor: breakpoint result has wrong type"))
			return
		}
		respond(reply, result, nil)
	}}
}

func (r *runtime) breakpointCleanupWork() rpcWork {
	return rpcWork{kind: opBreakpointCleanup, run: func(ctx context.Context, client *bridge.Client) (any, error) {
		placer := actorPlacer{runner: r.opts.BreakpointRunner, client: client}
		return r.breakpoints.CleanupPending(ctx, placer)
	}, done: func(_ *runtime, _ any, _ error) {
		// CleanupPending retains every failed physical record as an orphan.
		// A later stopped observation retries it; target cleanup failures are
		// evidence, not a reason to fault the whole observed session.
	}}
}

type actorPlacer struct {
	runner BreakpointRunner
	client *bridge.Client
}

func (p actorPlacer) Set(ctx context.Context, request breakpoint.PlacementRequest) (breakpoint.Physical, error) {
	return p.runner.Set(ctx, p.client, request)
}

func (p actorPlacer) Clear(ctx context.Context, physical breakpoint.Physical) error {
	return p.runner.Clear(ctx, p.client, physical)
}

type noteHintCommand struct {
	token uint32
	reply chan reply[struct{}]
}

func (c noteHintCommand) apply(r *runtime) {
	if r.opened && !r.faulted && r.stopper.NoteHint(c.token) && !r.hasStateWork() {
		r.enqueue(r.stateWork(nil, nil))
	}
	respond(c.reply, struct{}{}, nil)
}

func (r *runtime) watchHints(hints <-chan uint32) {
	for {
		select {
		case <-r.done:
			return
		case token, ok := <-hints:
			if !ok {
				return
			}
			r.detachHint(token)
		}
	}
}

func (r *runtime) detachHint(token uint32) {
	select {
	case r.commands <- noteHintCommand{token: token}:
	case <-r.done:
	}
}

type stackCommand struct {
	threadID int
	page     inspection.Page
	reply    chan reply[inspection.Stack]
}

func (c stackCommand) apply(r *runtime) {
	core, stopContext, err := r.inspectionCore(c.threadID)
	if err != nil {
		respond(c.reply, inspection.Stack{}, err)
		return
	}
	r.enqueue(r.inspectionWork(stopContext, func(ctx context.Context, service *inspection.Service) (any, error) {
		return service.Stack(ctx, stopContext, core, c.page)
	}, c.reply))
}

type scopesCommand struct {
	frameID int32
	reply   chan reply[[]inspection.Scope]
}

func (c scopesCommand) apply(r *runtime) {
	stopContext, err := r.inspectionStop()
	if err != nil {
		respond(c.reply, nil, err)
		return
	}
	r.enqueue(r.inspectionWork(stopContext, func(ctx context.Context, service *inspection.Service) (any, error) {
		return service.Scopes(ctx, stopContext, c.frameID)
	}, c.reply))
}

type variablesCommand struct {
	reference int32
	page      inspection.Page
	format    inspection.Format
	reply     chan reply[inspection.VariablePage]
}

func (c variablesCommand) apply(r *runtime) {
	stopContext, err := r.inspectionStop()
	if err != nil {
		respond(c.reply, inspection.VariablePage{}, err)
		return
	}
	r.enqueue(r.inspectionWork(stopContext, func(ctx context.Context, service *inspection.Service) (any, error) {
		return service.Variables(ctx, stopContext, c.reference, c.page, c.format)
	}, c.reply))
}

type evaluateCommand struct {
	frameID    int32
	expression inspection.Expression
	format     inspection.Format
	reply      chan reply[inspection.Variable]
}

func (c evaluateCommand) apply(r *runtime) {
	stopContext, err := r.inspectionStop()
	if err != nil {
		respond(c.reply, inspection.Variable{}, err)
		return
	}
	r.enqueue(r.inspectionWork(stopContext, func(ctx context.Context, service *inspection.Service) (any, error) {
		return service.Evaluate(ctx, stopContext, c.frameID, c.expression, c.format)
	}, c.reply))
}

type inspectionCall func(context.Context, *inspection.Service) (any, error)

func (r *runtime) inspectionWork(stopContext inspection.StopContext, call inspectionCall, destination any) rpcWork {
	return rpcWork{kind: opInspection, validate: r.stoppedEpochGuard(stopContext.StopEpoch), run: func(ctx context.Context, client *bridge.Client) (any, error) {
		service, err := r.newInspectionService(client)
		if err != nil {
			return nil, err
		}
		return call(ctx, service)
	}, done: func(r *runtime, value any, err error) {
		if !r.sameStoppedEpoch(stopContext.StopEpoch) {
			// Service methods may have allocated frame/scope/value handles before
			// the state sample completed. They are bound to the old stop and must
			// not survive a stale completion.
			r.handles.DropStopBound()
			err = ErrInspectionStale
			value = nil
		}
		switch ch := destination.(type) {
		case chan reply[inspection.Stack]:
			result, ok := value.(inspection.Stack)
			if !ok && err == nil {
				err = errors.New("actor: stack result has wrong type")
			}
			respond(ch, result, err)
		case chan reply[[]inspection.Scope]:
			result, ok := value.([]inspection.Scope)
			if !ok && err == nil {
				err = errors.New("actor: scopes result has wrong type")
			}
			respond(ch, result, err)
		case chan reply[inspection.VariablePage]:
			result, ok := value.(inspection.VariablePage)
			if !ok && err == nil {
				err = errors.New("actor: variables result has wrong type")
			}
			respond(ch, result, err)
		case chan reply[inspection.Variable]:
			result, ok := value.(inspection.Variable)
			if !ok && err == nil {
				err = errors.New("actor: evaluate result has wrong type")
			}
			respond(ch, result, err)
		default:
			panic("actor: unsupported inspection reply")
		}
	}}
}

var ErrInspectionStale = errors.New("actor: inspection request is stale")

// stoppedEpochGuard is deliberately evaluated by startNext on the actor
// goroutine. A state RPC can be ahead of inspection in the serialized bridge
// queue; accepting an inspection command based on the old snapshot after that
// state completion would issue target I/O while the target is running.
func (r *runtime) stoppedEpochGuard(stopEpoch uint64) func(*runtime) error {
	return func(current *runtime) error {
		if current.sameStoppedEpoch(stopEpoch) {
			return nil
		}
		return ErrInspectionStale
	}
}

func (r *runtime) inspectionStop() (inspection.StopContext, error) {
	if !r.opened {
		return inspection.StopContext{}, ErrNotOpen
	}
	if r.faulted {
		return inspection.StopContext{}, ErrReconciliationNeeded
	}
	if r.opts.Inspection == nil || r.opts.SourceMap == nil {
		return inspection.StopContext{}, ErrInspectionUnavailable
	}
	snapshot := r.reducer.Snapshot()
	return inspection.StopContext{Stopped: snapshot.State == session.TargetStopped, StopEpoch: uint64(snapshot.StopEpoch)}, nil
}

func (r *runtime) inspectionCore(threadID int) (inspection.CoreID, inspection.StopContext, error) {
	stopContext, err := r.inspectionStop()
	if err != nil {
		return 0, inspection.StopContext{}, err
	}
	for _, thread := range r.threads {
		if thread.ID == threadID {
			return inspection.CoreID(thread.CoreID), stopContext, nil
		}
	}
	return 0, inspection.StopContext{}, fmt.Errorf("actor: unknown thread %d", threadID)
}

func (r *runtime) newInspectionService(client *bridge.Client) (*inspection.Service, error) {
	return inspection.New(inspectionAdapter{runner: r.opts.Inspection, client: client}, r.handles, r.opts.SourceMap)
}

type inspectionAdapter struct {
	runner InspectionRunner
	client *bridge.Client
}

func (b inspectionAdapter) Stack(ctx context.Context, core inspection.CoreID) ([]inspection.RawFrame, error) {
	return b.runner.Stack(ctx, b.client, core)
}
func (b inspectionAdapter) Scopes(ctx context.Context, core inspection.CoreID, frame uint64) ([]inspection.RawScope, error) {
	return b.runner.Scopes(ctx, b.client, core, frame)
}
func (b inspectionAdapter) Variables(ctx context.Context, core inspection.CoreID, locator inspection.ScopeLocator, page inspection.Page, format inspection.Format) (inspection.RawValuePage, error) {
	return b.runner.Variables(ctx, b.client, core, locator, page, format)
}
func (b inspectionAdapter) Children(ctx context.Context, core inspection.CoreID, locator inspection.ValueLocator, page inspection.Page, format inspection.Format) (inspection.RawValuePage, error) {
	return b.runner.Children(ctx, b.client, core, locator, page, format)
}
func (b inspectionAdapter) Evaluate(ctx context.Context, core inspection.CoreID, frame uint64, expression inspection.Expression, format inspection.Format) (inspection.RawValue, error) {
	return b.runner.Evaluate(ctx, b.client, core, frame, expression, format)
}

func (r *runtime) applyConfigurationActions(actions []session.Action, snapshot session.Snapshot) {
	for _, action := range actions {
		switch action.Kind {
		case session.ActionExecutionSuspended:
			if r.lastStop != nil && r.lastStop.StopEpoch == uint64(snapshot.StopEpoch) {
				r.applyStopEffect(*r.lastStop, snapshot, nil)
			} else {
				r.publish(Event{Kind: EventStopped, Snapshot: snapshot})
			}
		case session.ActionExecutionResumed:
			r.publish(Event{Kind: EventResumed, Snapshot: snapshot, ThreadID: r.representativeThread()})
		case session.ActionInvalidateSuspendedReferences:
			r.handles.DropStopBound()
			r.publish(Event{Kind: EventInvalidated, Snapshot: snapshot})
		case session.ActionReconciliationRequired:
			r.publish(Event{Kind: EventReconciliationRequired, Snapshot: snapshot})
		}
	}
}

func (r *runtime) threadIDForCore(core uint64) int {
	for _, thread := range r.threads {
		if thread.CoreID == core {
			return thread.ID
		}
	}
	return 0
}

func stopObservation(state multi.State, halt multi.HaltInfo, hasHalt bool) stop.Observation {
	observation := stop.Observation{StopStamp: 0, HasStopStamp: false}
	switch {
	case state.Status.IsStopped():
		observation.State = stop.StateStopped
	case state.Status.IsRunning():
		observation.State = stop.StateRunning
	default:
		observation.State = stop.StateUnknown
	}
	observation.StopStamp, observation.HasStopStamp = state.ProcessInfo.StopStamp()
	info := stop.StopInfo{
		ContinuedFromBP:    processFlag(state, "fContFromBp"),
		InStepMode:         processFlag(state, "fInStepMode"),
		PendingHalt:        processFlag(state, "fPendingHalt"),
		StoppedOnException: state.ProcessInfo.StoppedOnException(),
	}
	if hasHalt {
		switch halt.Reason {
		case multi.HaltReasonBreakpoint:
			info.Cause = stop.HaltCauseBreakpoint
		case multi.HaltReasonUserRequest:
			info.Cause = stop.HaltCauseUser
		case multi.HaltReasonNotRunning:
			info.Cause = stop.HaltCauseNotRunning
		}
	}
	observation.Info = info
	return observation
}

func processFlag(state multi.State, key string) bool {
	value, ok := state.ProcessInfo.Field(key)
	return ok && strings.TrimSpace(value) == "1"
}
