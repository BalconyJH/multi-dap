// Package session reduces observations of one target into the session-global
// state used by Debugger Core. It deliberately has no transport, scheduler,
// or frontend dependency: its caller owns serialization and applies the
// returned actions in order.
package session

import (
	"errors"
	"fmt"

	"github.com/Tacrolimus/multi-dap/internal/multi"
)

// TargetState is the execution state established from target observation.
// Unknown is distinct from Faulted: unknown is an ordinary incomplete
// observation, while faulted requires an explicit reconciliation before the
// session can make another transition decision.
type TargetState uint8

const (
	TargetUnknown TargetState = iota
	TargetStopped
	TargetRunning
	TargetFaulted
)

// ExecutionEpoch identifies a confirmed interval of execution. It advances
// only when observation establishes a transition into TargetRunning.
type ExecutionEpoch uint64

// StopEpoch identifies the observed stopped generation. It is assigned from
// the target's stop stamp rather than locally counted.
type StopEpoch uint64

// Observation is the complete state evidence needed by the reducer. A stop
// stamp is required whenever execution state is known; its absence or
// regression is unsafe because it makes missed cycles indistinguishable from
// duplicate observations.
type Observation struct {
	Status       multi.Status
	StopStamp    uint64
	HasStopStamp bool
}

// ActionKind describes work the session actor must apply in order. These are
// frontend-neutral transition facts, not protocol messages.
type ActionKind uint8

const (
	// ActionInvalidateSuspendedReferences must be applied before a transition
	// into running. It is also emitted when a fault invalidates trust in the
	// current suspended world.
	ActionInvalidateSuspendedReferences ActionKind = iota
	ActionExecutionResumed
	ActionExecutionSuspended
	ActionReconciliationRequired
)

// Action contains the epoch values that were current when the action was
// produced. A frontend maps execution actions to its own event vocabulary.
type Action struct {
	Kind           ActionKind
	ExecutionEpoch ExecutionEpoch
	StopEpoch      StopEpoch
}

// Snapshot is the immutable view of the reducer after an operation.
type Snapshot struct {
	State          TargetState
	ExecutionEpoch ExecutionEpoch
	StopEpoch      StopEpoch
	LastStopStamp  uint64
	HasStopStamp   bool
	Configuring    bool
	NeedsReset     bool
	LeaseOwner     ControllerID
}

// Result is the outcome of a reducer operation. Actions are already ordered.
// During the configuration barrier only invalidation actions are returned;
// externally visible transition actions are held until ConfigurationDone.
type Result struct {
	Snapshot Snapshot
	Actions  []Action
}

var (
	// ErrReconciliationRequired means the observation stream can no longer be
	// safely interpreted. Callers must rebuild their view from an explicit
	// reconciliation rather than guessing a transition.
	ErrReconciliationRequired = errors.New("session reconciliation required")
	ErrConfigurationActive    = errors.New("session configuration already active")
	ErrNoConfiguration        = errors.New("session configuration is not active")
	ErrLeaseHeld              = errors.New("control lease is held")
	ErrNotLeaseHolder         = errors.New("control lease is not held by caller")
	ErrResetRequired          = errors.New("reset is required before resuming execution")
)

// ControllerID identifies one frontend or local controller. The empty value
// is never a valid lease owner.
type ControllerID string

// Options contains target-specific policy supplied by validated
// configuration, not hard-coded target behavior.
type Options struct {
	RequireResetAfterDownload bool
}

// Reducer holds only deterministic session state. It is intended to be owned
// by one actor goroutine; it contains no synchronization of its own.
type Reducer struct {
	requireResetAfterDownload bool

	state          TargetState
	executionEpoch ExecutionEpoch
	stopEpoch      StopEpoch
	lastStopStamp  uint64
	hasStopStamp   bool
	hasStop        bool
	configuring    bool
	needsReset     bool
	leaseOwner     ControllerID
}

// New returns a reducer before any target observation has been accepted.
func New(options Options) *Reducer {
	return &Reducer{requireResetAfterDownload: options.RequireResetAfterDownload}
}

// Snapshot returns the current model without changing it.
func (r *Reducer) Snapshot() Snapshot {
	return Snapshot{
		State:          r.state,
		ExecutionEpoch: r.executionEpoch,
		StopEpoch:      r.stopEpoch,
		LastStopStamp:  r.lastStopStamp,
		HasStopStamp:   r.hasStopStamp,
		Configuring:    r.configuring,
		NeedsReset:     r.needsReset,
		LeaseOwner:     r.leaseOwner,
	}
}

// BeginConfiguration starts a frontend configuration barrier. Observations
// continue to update epochs and invalidate suspended references, but their
// public transition actions are suppressed until ConfigurationDone.
func (r *Reducer) BeginConfiguration() (Result, error) {
	if r.state == TargetFaulted {
		return r.result(nil), ErrReconciliationRequired
	}
	if r.configuring {
		return r.result(nil), ErrConfigurationActive
	}
	r.configuring = true
	return r.result(nil), nil
}

// ConfigurationDone closes the frontend configuration barrier and publishes
// exactly one action for the final canonical execution state, irrespective of
// churn folded into the barrier. The frontend uses that action as its initial
// state after its held attach response has been released.
func (r *Reducer) ConfigurationDone() (Result, error) {
	if r.state == TargetFaulted {
		return r.result(nil), ErrReconciliationRequired
	}
	if !r.configuring {
		return r.result(nil), ErrNoConfiguration
	}
	r.configuring = false
	switch r.state {
	case TargetStopped:
		return r.result([]Action{r.suspendedAction()}), nil
	case TargetRunning:
		return r.result([]Action{r.resumedAction()}), nil
	default:
		return r.result(nil), nil
	}
}

// CancelConfiguration drops a frontend configuration barrier without
// publishing an initial state. It is used when a client disconnects before
// configurationDone; leaving the barrier active would suppress events for the
// next frontend.
func (r *Reducer) CancelConfiguration() (Result, error) {
	if !r.configuring {
		return r.result(nil), ErrNoConfiguration
	}
	r.configuring = false
	return r.result(nil), nil
}

// Observe reduces a target observation. It is the only operation that changes
// execution state or epochs. Commands and accepted requests never do so.
func (r *Reducer) Observe(observation Observation) (Result, error) {
	if r.state == TargetFaulted {
		return r.result(nil), ErrReconciliationRequired
	}

	next := observedState(observation.Status)
	if err := r.acceptStopStamp(next, observation); err != nil {
		return r.fault(err)
	}

	switch next {
	case TargetUnknown:
		// An incomplete lifecycle observation does not erase the last
		// canonical execution state. Keeping it is what lets a later known
		// sample recover the transition across the unknown interval.
		return r.result(nil), nil
	case TargetStopped:
		return r.observeStopped(observation.StopStamp)
	case TargetRunning:
		return r.observeRunning(observation.StopStamp)
	default:
		return r.fault(fmt.Errorf("unrepresentable observed state %d", next))
	}
}

func observedState(status multi.Status) TargetState {
	switch {
	case status.IsStopped():
		return TargetStopped
	case status.IsRunning():
		return TargetRunning
	default:
		return TargetUnknown
	}
}

func (r *Reducer) acceptStopStamp(next TargetState, observation Observation) error {
	if !observation.HasStopStamp {
		if r.hasStopStamp || next != TargetUnknown {
			return errors.New("stop stamp is absent")
		}
		return nil
	}
	if r.hasStopStamp && observation.StopStamp < r.lastStopStamp {
		return fmt.Errorf("stop stamp regressed from %d to %d", r.lastStopStamp, observation.StopStamp)
	}
	r.lastStopStamp = observation.StopStamp
	r.hasStopStamp = true
	return nil
}

func (r *Reducer) observeStopped(stamp uint64) (Result, error) {
	previous := r.state
	r.state = TargetStopped

	if !r.hasStop {
		r.hasStop = true
		r.stopEpoch = StopEpoch(stamp)
		return r.publish([]Action{r.suspendedAction()}), nil
	}

	switch previous {
	case TargetStopped:
		if stamp == uint64(r.stopEpoch) {
			return r.result(nil), nil
		}
		// A different generation while still stopped proves an unobserved
		// run/stop cycle. Publish the late ordered pair and advance both
		// session epochs at detection time.
		r.executionEpoch++
		r.stopEpoch = StopEpoch(stamp)
		return r.publish([]Action{
			r.invalidateAction(),
			r.resumedAction(),
			r.suspendedAction(),
		}), nil
	case TargetRunning:
		if stamp == uint64(r.stopEpoch) {
			return r.fault(errors.New("stopped observation did not advance stop stamp"))
		}
		r.stopEpoch = StopEpoch(stamp)
		return r.publish([]Action{r.suspendedAction()}), nil
	default:
		// An unknown interval has no observable execution transition. If the
		// stamp is unchanged, this is merely renewed evidence of the previous
		// stop and must not duplicate it. A new stamp proves a missed cycle.
		if stamp == uint64(r.stopEpoch) {
			return r.result(nil), nil
		}
		r.executionEpoch++
		r.stopEpoch = StopEpoch(stamp)
		return r.publish([]Action{
			r.invalidateAction(),
			r.resumedAction(),
			r.suspendedAction(),
		}), nil
	}
}

func (r *Reducer) observeRunning(stamp uint64) (Result, error) {
	previous := r.state
	r.state = TargetRunning

	if previous == TargetStopped {
		if stamp == uint64(r.stopEpoch) {
			r.executionEpoch++
			return r.publish([]Action{r.invalidateAction(), r.resumedAction()}), nil
		}
		// The target stopped and resumed between observations. The stop stamp
		// makes that cycle observable even though its final state is running.
		r.executionEpoch += 2
		r.stopEpoch = StopEpoch(stamp)
		return r.publish([]Action{
			r.invalidateAction(),
			r.resumedAction(),
			r.suspendedAction(),
			r.resumedAction(),
		}), nil
	}

	if previous == TargetRunning && stamp != uint64(r.stopEpoch) && r.hasStop {
		// A changed stamp is proof of a complete, externally initiated cycle
		// between two running samples. Report it in observed order.
		r.executionEpoch++
		r.stopEpoch = StopEpoch(stamp)
		return r.publish([]Action{r.suspendedAction(), r.resumedAction()}), nil
	}

	return r.result(nil), nil
}

func (r *Reducer) publish(actions []Action) Result {
	if !r.configuring {
		return r.result(actions)
	}
	internal := make([]Action, 0, len(actions))
	for _, action := range actions {
		if action.Kind == ActionInvalidateSuspendedReferences {
			internal = append(internal, action)
		}
	}
	return r.result(internal)
}

func (r *Reducer) fault(cause error) (Result, error) {
	r.state = TargetFaulted
	actions := []Action{r.invalidateAction(), {
		Kind:           ActionReconciliationRequired,
		ExecutionEpoch: r.executionEpoch,
		StopEpoch:      r.stopEpoch,
	}}
	return r.result(actions), fmt.Errorf("%w: %v", ErrReconciliationRequired, cause)
}

func (r *Reducer) invalidateAction() Action {
	return Action{Kind: ActionInvalidateSuspendedReferences, ExecutionEpoch: r.executionEpoch, StopEpoch: r.stopEpoch}
}

func (r *Reducer) resumedAction() Action {
	return Action{Kind: ActionExecutionResumed, ExecutionEpoch: r.executionEpoch, StopEpoch: r.stopEpoch}
}

func (r *Reducer) suspendedAction() Action {
	return Action{Kind: ActionExecutionSuspended, ExecutionEpoch: r.executionEpoch, StopEpoch: r.stopEpoch}
}

func (r *Reducer) result(actions []Action) Result {
	return Result{Snapshot: r.Snapshot(), Actions: actions}
}

// AcquireControl grants the only mutating-controller lease. Read-only
// observers do not use this method and can coexist freely.
func (r *Reducer) AcquireControl(owner ControllerID) error {
	if owner == "" {
		return errors.New("control lease owner is empty")
	}
	if r.leaseOwner != "" && r.leaseOwner != owner {
		return fmt.Errorf("owner %q: %w by %q", owner, ErrLeaseHeld, r.leaseOwner)
	}
	r.leaseOwner = owner
	return nil
}

// ReleaseControl releases owner’s lease. A different controller cannot
// release a lease it does not hold.
func (r *Reducer) ReleaseControl(owner ControllerID) error {
	if r.leaseOwner != owner || owner == "" {
		return fmt.Errorf("owner %q: %w", owner, ErrNotLeaseHolder)
	}
	r.leaseOwner = ""
	return nil
}

// RequireControl authorizes a mutating operation other than resume.
func (r *Reducer) RequireControl(owner ControllerID) error {
	if r.leaseOwner != owner || owner == "" {
		return fmt.Errorf("owner %q: %w", owner, ErrNotLeaseHolder)
	}
	return nil
}

// RequireResume authorizes a resume request without changing target state.
// The later observation is solely responsible for the state transition.
func (r *Reducer) RequireResume(owner ControllerID) error {
	if err := r.RequireControl(owner); err != nil {
		return err
	}
	if r.needsReset {
		return ErrResetRequired
	}
	return nil
}

// NoteDownload records a confirmed download. It is intentionally separate
// from command acceptance; callers invoke it only after confirmed completion.
func (r *Reducer) NoteDownload() {
	if r.requireResetAfterDownload {
		r.needsReset = true
	}
}

// NoteReset records a confirmed reset and clears the post-download resume
// gate. It does not itself assert any execution transition.
func (r *Reducer) NoteReset() {
	r.needsReset = false
}
