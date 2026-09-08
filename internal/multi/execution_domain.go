package multi

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ErrExecutionDomainUnavailable means that the configured topology has not
// yet established a verified, per-core execution-and-observation contract.
// It is deliberately distinct from a target refusal: callers must not retry
// it by falling back to a primary debugger-window command.
var ErrExecutionDomainUnavailable = errors.New("multi: execution domain is unavailable")

// ExecutionOperation is one target-mutating operation. It has no DAP
// vocabulary because a DAP thread request is only one possible caller of an
// execution domain.
type ExecutionOperation uint8

const (
	ExecutionContinue ExecutionOperation = iota + 1
	ExecutionPause
	ExecutionStepIn
	ExecutionNext
)

func (o ExecutionOperation) String() string {
	switch o {
	case ExecutionContinue:
		return "continue"
	case ExecutionPause:
		return "pause"
	case ExecutionStepIn:
		return "step_in"
	case ExecutionNext:
		return "next"
	default:
		return fmt.Sprintf("unknown_execution_operation(%d)", o)
	}
}

// ExecutionScope names an immutable subset of configured cores. It can only
// be created by its ExecutionDomain, so raw debugger component names and
// process slots never cross this boundary.
type ExecutionScope struct {
	domain *ExecutionDomain
	cores  []int
}

// Cores returns the configured core IDs in stable ascending order.
func (s ExecutionScope) Cores() []int { return append([]int(nil), s.cores...) }

// ExecutionRequest separates the requested target scope from the operation.
// A request confirmation is never treated as an execution-state observation.
type ExecutionRequest struct {
	Operation ExecutionOperation
	Scope     ExecutionScope
}

// CoreExecutionObservation is one per-core lifecycle sample. A domain only
// accepts stable MULTI states with an observed stop stamp; its caller must not
// manufacture a sample from the acceptance of a textual command.
type CoreExecutionObservation struct {
	Core         int
	Status       Status
	StopStamp    uint64
	HasStopStamp bool
}

// ExecutionObservation is a complete, verified observation of one scope at
// one point in time. It is intentionally opaque so all-threads DAP claims
// cannot be assembled from a partial collection of per-core samples.
type ExecutionObservation struct {
	domain  *ExecutionDomain
	scope   ExecutionScope
	samples []CoreExecutionObservation
}

// Scope returns the exact observed configured-core scope.
func (o ExecutionObservation) Scope() ExecutionScope {
	return ExecutionScope{domain: o.scope.domain, cores: append([]int(nil), o.scope.cores...)}
}

// Samples returns independent copies in the scope's stable core order.
func (o ExecutionObservation) Samples() []CoreExecutionObservation {
	return append([]CoreExecutionObservation(nil), o.samples...)
}

// AllRunning and AllStopped report a property only of a complete
// observation. They deliberately make no claim about configured cores outside
// the observation scope.
func (o ExecutionObservation) AllRunning() bool {
	if len(o.samples) == 0 {
		return false
	}
	for _, sample := range o.samples {
		if !sample.Status.IsRunning() {
			return false
		}
	}
	return true
}

func (o ExecutionObservation) AllStopped() bool {
	return len(o.samples) != 0 && allObserved(o.samples, StatusStopped)
}

func allObserved(samples []CoreExecutionObservation, status Status) bool {
	for _, sample := range samples {
		if sample.Status != status {
			return false
		}
	}
	return true
}

// ExecutionTransition proves the before/after lifecycle evidence for one
// request scope. The command acceptance itself is deliberately absent: an
// executor must collect both observations after it submits a command.
type ExecutionTransition struct {
	domain *ExecutionDomain
	scope  ExecutionScope
	before ExecutionObservation
	after  ExecutionObservation
}

func (t ExecutionTransition) Scope() ExecutionScope { return t.after.Scope() }
func (t ExecutionTransition) Before() ExecutionObservation {
	return ExecutionObservation{domain: t.before.domain, scope: t.before.Scope(), samples: t.before.Samples()}
}
func (t ExecutionTransition) After() ExecutionObservation {
	return ExecutionObservation{domain: t.after.domain, scope: t.after.Scope(), samples: t.after.Samples()}
}

// ExecutionResult combines one typed request with its proven transition. A
// result only exists after operation-specific transition validation succeeds.
type ExecutionResult struct {
	request    ExecutionRequest
	transition ExecutionTransition
}

func (r ExecutionResult) Request() ExecutionRequest       { return r.request }
func (r ExecutionResult) Transition() ExecutionTransition { return r.transition }

// ExecutionTruth is the only form suitable for DAP's allThreads fields.
// Both values remain false unless a proven transition's after-observation
// covered every configured core and confirmed one uniform, stable state.
type ExecutionTruth struct {
	AllThreadsContinued bool
	AllThreadsStopped   bool
}

// ExecutionDomain owns the configured-core execution boundary. It is bound
// once to a resolved topology and starts disabled. Enabling a concrete
// executor requires separate hardware evidence for command effects and
// per-core state observations; there is intentionally no public opt-in.
type ExecutionDomain struct {
	topology *Topology
	cores    []int
	executor executionExecutor
}

// NewExecutionDomain binds the default-disabled execution domain to an
// immutable topology. Its successful construction does not authorize target
// control.
func NewExecutionDomain(topology *Topology) (*ExecutionDomain, error) {
	if topology == nil {
		return nil, errors.New("multi: execution domain requires topology")
	}
	cores := topology.Cores()
	if len(cores) == 0 {
		return nil, errors.New("multi: execution domain requires configured cores")
	}
	ids := make([]int, len(cores))
	for i, core := range cores {
		ids[i] = core.ID
	}
	return &ExecutionDomain{topology: topology, cores: ids}, nil
}

// ScopeForCore returns the single configured-core scope identified by core.
func (d *ExecutionDomain) ScopeForCore(core int) (ExecutionScope, error) {
	if d == nil {
		return ExecutionScope{}, ErrExecutionDomainUnavailable
	}
	if !containsCore(d.cores, core) {
		return ExecutionScope{}, fmt.Errorf("multi: execution domain has no configured core %d", core)
	}
	return ExecutionScope{domain: d, cores: []int{core}}, nil
}

// AllConfiguredScope returns a scope that includes every configured core.
func (d *ExecutionDomain) AllConfiguredScope() (ExecutionScope, error) {
	if d == nil {
		return ExecutionScope{}, ErrExecutionDomainUnavailable
	}
	return ExecutionScope{domain: d, cores: append([]int(nil), d.cores...)}, nil
}

// confirmObservation validates a full per-core lifecycle sample for scope.
// It is intentionally private: callers must prove a transition, not publish
// a standalone post-command state as an execution effect.
func (d *ExecutionDomain) confirmObservation(scope ExecutionScope, samples []CoreExecutionObservation) (ExecutionObservation, error) {
	if err := d.validateScope(scope); err != nil {
		return ExecutionObservation{}, err
	}
	if len(samples) != len(scope.cores) {
		return ExecutionObservation{}, fmt.Errorf("multi: execution observation has %d samples for %d-core scope", len(samples), len(scope.cores))
	}
	byCore := make(map[int]CoreExecutionObservation, len(samples))
	for _, sample := range samples {
		if !containsCore(scope.cores, sample.Core) {
			return ExecutionObservation{}, fmt.Errorf("multi: execution observation includes core %d outside scope", sample.Core)
		}
		if _, duplicate := byCore[sample.Core]; duplicate {
			return ExecutionObservation{}, fmt.Errorf("multi: execution observation duplicates core %d", sample.Core)
		}
		if !sample.HasStopStamp {
			return ExecutionObservation{}, fmt.Errorf("multi: execution observation core %d has no stop stamp", sample.Core)
		}
		if !sample.Status.IsStopped() && !sample.Status.IsRunning() {
			return ExecutionObservation{}, fmt.Errorf("multi: execution observation core %d has unstable status %s", sample.Core, sample.Status)
		}
		byCore[sample.Core] = sample
	}
	ordered := make([]CoreExecutionObservation, len(scope.cores))
	for i, core := range scope.cores {
		sample, ok := byCore[core]
		if !ok {
			return ExecutionObservation{}, fmt.Errorf("multi: execution observation misses core %d", core)
		}
		ordered[i] = sample
	}
	return ExecutionObservation{domain: d, scope: scope, samples: ordered}, nil
}

// confirmTransition proves the operation-specific lifecycle transition. A
// successful routed command by itself cannot manufacture this result.
func (d *ExecutionDomain) confirmTransition(request ExecutionRequest, samples executionSamples) (ExecutionResult, error) {
	if err := d.validateRequest(request); err != nil {
		return ExecutionResult{}, err
	}
	before, err := d.confirmObservation(request.Scope, samples.before)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("multi: execution before observation: %w", err)
	}
	after, err := d.confirmObservation(request.Scope, samples.after)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("multi: execution after observation: %w", err)
	}
	transition := ExecutionTransition{domain: d, scope: request.Scope, before: before, after: after}
	if err := validateTransition(request.Operation, transition); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{request: request, transition: transition}, nil
}

// Truth converts only a proven all-configured-core transition into DAP
// allThreads facts. A single-core result is useful to its caller but cannot
// assert the state of other configured threads.
func (d *ExecutionDomain) Truth(result ExecutionResult) ExecutionTruth {
	transition := result.transition
	if d == nil || transition.domain != d || transition.scope.domain != d || !sameCores(transition.scope.cores, d.cores) {
		return ExecutionTruth{}
	}
	return ExecutionTruth{
		AllThreadsContinued: transition.after.AllRunning(),
		AllThreadsStopped:   transition.after.AllStopped(),
	}
}

// Execute is the future side-effect seam. The shipped domain has no executor,
// so every request reaches ErrExecutionDomainUnavailable before it can reach
// a bridge client or MULTI command.
func (d *ExecutionDomain) Execute(ctx context.Context, driver *Driver, request ExecutionRequest) (ExecutionResult, error) {
	if d == nil || d.executor == nil {
		return ExecutionResult{}, ErrExecutionDomainUnavailable
	}
	if driver == nil {
		return ExecutionResult{}, errors.New("multi: execution domain requires driver")
	}
	if err := d.validateRequest(request); err != nil {
		return ExecutionResult{}, err
	}
	samples, err := d.executor.Execute(ctx, d.topology, driver, request)
	if err != nil {
		return ExecutionResult{}, err
	}
	return d.confirmTransition(request, samples)
}

type executionExecutor interface {
	Execute(context.Context, *Topology, *Driver, ExecutionRequest) (executionSamples, error)
}

type executionSamples struct {
	before []CoreExecutionObservation
	after  []CoreExecutionObservation
}

func validateTransition(operation ExecutionOperation, transition ExecutionTransition) error {
	before, after := transition.before.samples, transition.after.samples
	switch operation {
	case ExecutionContinue:
		if !transition.before.AllStopped() || !transition.after.AllRunning() {
			return errors.New("multi: continue did not prove stopped-to-running transition")
		}
	case ExecutionPause:
		if !transition.before.AllRunning() || !transition.after.AllStopped() {
			return errors.New("multi: pause did not prove running-to-stopped transition")
		}
		for index := range before {
			if after[index].StopStamp <= before[index].StopStamp {
				return fmt.Errorf("multi: pause core %d did not advance stop stamp", after[index].Core)
			}
		}
	case ExecutionStepIn, ExecutionNext:
		if !transition.before.AllStopped() || !transition.after.AllStopped() {
			return fmt.Errorf("multi: %s did not prove stopped-to-stopped step transition", operation)
		}
		for index := range before {
			if after[index].StopStamp <= before[index].StopStamp {
				return fmt.Errorf("multi: %s core %d did not advance stop stamp", operation, after[index].Core)
			}
		}
	default:
		return fmt.Errorf("multi: unsupported execution operation %s", operation)
	}
	return nil
}

func (d *ExecutionDomain) validateRequest(request ExecutionRequest) error {
	if err := d.validateScope(request.Scope); err != nil {
		return err
	}
	if request.Operation < ExecutionContinue || request.Operation > ExecutionNext {
		return fmt.Errorf("multi: unsupported execution operation %s", request.Operation)
	}
	return nil
}

func (d *ExecutionDomain) validateScope(scope ExecutionScope) error {
	if d == nil || scope.domain != d || len(scope.cores) == 0 {
		return ErrExecutionDomainUnavailable
	}
	if !sort.IntsAreSorted(scope.cores) {
		return errors.New("multi: execution scope is not in stable core order")
	}
	if len(scope.cores) > len(d.cores) {
		return errors.New("multi: execution scope exceeds configured cores")
	}
	for i, core := range scope.cores {
		if !containsCore(d.cores, core) {
			return fmt.Errorf("multi: execution scope has unconfigured core %d", core)
		}
		if i > 0 && scope.cores[i-1] == core {
			return fmt.Errorf("multi: execution scope duplicates core %d", core)
		}
	}
	return nil
}

func containsCore(cores []int, core int) bool {
	index := sort.SearchInts(cores, core)
	return index < len(cores) && cores[index] == core
}

func sameCores(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
