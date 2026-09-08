// Package stop turns observed target state into canonical execution events.
//
// It deliberately has no bridge, DAP, or MULTI dependency. Hints can make a
// later observation happen sooner, but only an Observation can produce an
// event. In particular, StopEpoch is the observed stop stamp, never a local
// counter: this is what makes a complete unobserved run/stop cycle visible.
package stop

import (
	"errors"
	"fmt"
	"sort"
)

// State is the execution state reported by one coherent state/stop-info read.
type State uint8

const (
	StateUnknown State = iota
	StateStopped
	StateRunning
)

// Reason is a frontend-neutral stop classification. Unknown is intentional:
// a frontend may map it to its conservative protocol representation, but the
// core never turns incomplete evidence into a confidently wrong cause.
type Reason string

const (
	ReasonUnknown    Reason = "unknown"
	ReasonBreakpoint Reason = "breakpoint"
	ReasonStep       Reason = "step"
	ReasonException  Reason = "exception"
	ReasonPause      Reason = "pause"
)

// HaltCause is the normalized, textual halt-cause observation. It is only
// supporting evidence: M0 established it may describe an earlier stop.
type HaltCause uint8

const (
	HaltCauseUnknown HaltCause = iota
	HaltCauseBreakpoint
	HaltCauseUser
	HaltCauseNotRunning
)

// StopInfo contains the evidence needed to conservatively classify a stop.
// BreakpointEvidence represents a corroborating $_BREAK-style observation.
type StopInfo struct {
	Cause              HaltCause
	ContinuedFromBP    bool
	InStepMode         bool
	PendingHalt        bool
	StoppedOnException bool
	BreakpointEvidence bool
	Core               int
	HasCore            bool
}

// Observation is one coherent target sample. StopStamp must be available for
// every known state and be monotonic for the lifetime of an arbiter.
type Observation struct {
	State        State
	StopStamp    uint64
	HasStopStamp bool
	Info         StopInfo
}

// HintTarget is the local identity associated with one live hint token.
// BreakpointID is zero for an unverified orphan: it can still accelerate a
// state refresh, but must never be reported as a verified DAP breakpoint hit.
type HintTarget struct {
	BreakpointID int
	Core         int
}

// TokenResolver resolves only currently live tokens. It is deliberately a
// small interface so the breakpoint store, a replay fake, or a future token
// registry can provide it without coupling this package to placement details.
type TokenResolver interface {
	ResolveHint(token uint32) (HintTarget, bool)
}

// TokenResolverFunc adapts a function into a TokenResolver. It keeps the
// arbiter independent of the breakpoint store while making the normal store
// lookup wiring one line at the actor boundary.
type TokenResolverFunc func(token uint32) (HintTarget, bool)

func (f TokenResolverFunc) ResolveHint(token uint32) (HintTarget, bool) { return f(token) }

// EffectKind names an ordered consequence of an observation.
type EffectKind uint8

const (
	EffectInvalidateSuspended EffectKind = iota
	EffectContinued
	EffectStopped
)

// Effect is emitted in target order. StopEpoch always equals the stop stamp
// of the suspended world referred to by a stopped effect.
type Effect struct {
	Kind             EffectKind
	ExecutionEpoch   uint64
	StopEpoch        uint64
	Reason           Reason
	Core             int
	HasCore          bool
	HitBreakpointIDs []int
}

var (
	ErrReconciliationRequired = errors.New("stop: reconciliation required")
	ErrInvalidObservation     = errors.New("stop: invalid observation")
)

// Arbiter reduces observations and accepts hints while it knows execution is
// running. It is actor-owned; callers must serialize calls to it.
type Arbiter struct {
	resolver  TokenResolver
	state     State
	hasStop   bool
	lastStamp uint64
	stopEpoch uint64
	exec      uint64
	pending   []pendingHint
	faulted   bool
}

type pendingHint struct {
	target HintTarget
	base   uint64
}

// Snapshot is the current observed state of an Arbiter. It is intended for
// the owning actor's diagnostics and must not be used to manufacture events.
type Snapshot struct {
	State          State
	ExecutionEpoch uint64
	StopEpoch      uint64
	Faulted        bool
}

// New returns an arbiter using resolver for live-token lookups. resolver may
// be nil when the hint channel is disabled.
func New(resolver TokenResolver) *Arbiter { return &Arbiter{resolver: resolver} }

// Snapshot returns the current derived epochs and state without changing the
// arbiter.
func (a *Arbiter) Snapshot() Snapshot {
	return Snapshot{State: a.state, ExecutionEpoch: a.exec, StopEpoch: a.currentStopEpoch(), Faulted: a.faulted}
}

// NoteHint records one valid hint after an initial target observation. A hint
// received while stopped triggers an immediate duplicate state check: if that
// check has the same stop stamp it is discarded, while a newer stamp proves a
// missed cycle and may use the hint. This covers GUI runs that start and end
// between polling ticks.
//
// The return value tells the caller whether it should schedule an immediate
// state observation. It does not mean a stop event was produced.
func (a *Arbiter) NoteHint(token uint32) bool {
	if a.faulted || a.state == StateUnknown || a.resolver == nil {
		return false
	}
	target, ok := a.resolver.ResolveHint(token)
	if !ok {
		return false
	}
	for _, pending := range a.pending {
		if pending.target == target && pending.base == a.currentStopEpoch() {
			return true
		}
	}
	a.pending = append(a.pending, pendingHint{target: target, base: a.currentStopEpoch()})
	return true
}

// Observe applies one state/stop-info sample. Invalid stamp evidence fails
// closed rather than risking stale-handle validity or duplicate stop events.
func (a *Arbiter) Observe(ob Observation) ([]Effect, error) {
	if a.faulted {
		return nil, ErrReconciliationRequired
	}
	if ob.State == StateUnknown {
		return nil, nil
	}
	if !ob.HasStopStamp {
		return a.fault("missing stop stamp")
	}
	if a.hasStop && ob.StopStamp < a.lastStamp {
		return a.fault("stop stamp regressed from %d to %d", a.lastStamp, ob.StopStamp)
	}
	a.lastStamp = ob.StopStamp

	switch ob.State {
	case StateRunning:
		return a.observeRunning(ob)
	case StateStopped:
		return a.observeStopped(ob)
	default:
		return a.fault("unknown execution state %d", ob.State)
	}
}

func (a *Arbiter) observeRunning(ob Observation) ([]Effect, error) {
	previous := a.state
	a.state = StateRunning
	// A hint that did not lead to a stopped observation before execution was
	// observed running again belongs to an old stop and cannot be carried into
	// a future one.
	a.pending = nil
	if !a.hasStop {
		return nil, nil
	}

	if previous == StateStopped {
		if ob.StopStamp == a.currentStopEpoch() {
			a.exec++
			return []Effect{a.invalidate(), a.continued()}, nil
		}
		// A complete cycle occurred between samples and is now running again.
		a.exec++
		stop := a.stopped(ob)
		a.exec++
		return []Effect{a.invalidate(), stop, a.continued()}, nil
	}
	if previous == StateRunning && ob.StopStamp != a.currentStopEpoch() {
		// A complete externally initiated cycle occurred between running polls.
		a.exec++
		stop := a.stopped(ob)
		a.exec++
		return []Effect{a.invalidate(), stop, a.continued()}, nil
	}
	return nil, nil
}

func (a *Arbiter) observeStopped(ob Observation) ([]Effect, error) {
	previous := a.state
	a.state = StateStopped
	if !a.hasStop {
		a.hasStop = true
		return []Effect{a.stopped(ob)}, nil
	}

	if previous == StateStopped {
		if ob.StopStamp == a.currentStopEpoch() {
			a.discardHintsAt(ob.StopStamp)
			return nil, nil
		}
		a.exec++
		return []Effect{a.invalidate(), a.continued(), a.stopped(ob)}, nil
	}
	if previous == StateRunning {
		if ob.StopStamp == a.currentStopEpoch() {
			return a.fault("running-to-stopped observation did not advance stop stamp")
		}
		return []Effect{a.stopped(ob)}, nil
	}

	if ob.StopStamp == a.currentStopEpoch() {
		return nil, nil
	}
	a.exec++
	return []Effect{a.invalidate(), a.continued(), a.stopped(ob)}, nil
}

func (a *Arbiter) currentStopEpoch() uint64 {
	if !a.hasStop {
		return 0
	}
	return a.stopEpoch
}

func (a *Arbiter) invalidate() Effect {
	return Effect{Kind: EffectInvalidateSuspended, ExecutionEpoch: a.exec, StopEpoch: a.currentStopEpoch()}
}

func (a *Arbiter) continued() Effect {
	return Effect{Kind: EffectContinued, ExecutionEpoch: a.exec, StopEpoch: a.currentStopEpoch()}
}

func (a *Arbiter) stopped(ob Observation) Effect {
	a.hasStop = true
	a.stopEpoch = ob.StopStamp
	targets := a.pendingFor(ob.StopStamp)
	a.pending = nil
	hits := make([]int, 0, len(targets))
	core, hasCore := ob.Info.Core, ob.Info.HasCore
	for _, candidate := range targets {
		target := candidate.target
		if target.BreakpointID > 0 {
			hits = append(hits, target.BreakpointID)
		}
		if !hasCore {
			core, hasCore = target.Core, true
		}
	}
	sort.Ints(hits)
	hits = compactInts(hits)
	return Effect{
		Kind: EffectStopped, ExecutionEpoch: a.exec, StopEpoch: ob.StopStamp,
		Reason: classify(ob.Info, len(hits) != 0), Core: core, HasCore: hasCore,
		HitBreakpointIDs: hits,
	}
}

func (a *Arbiter) pendingFor(stamp uint64) []pendingHint {
	accepted := make([]pendingHint, 0, len(a.pending))
	for _, candidate := range a.pending {
		if candidate.base < stamp {
			accepted = append(accepted, candidate)
		}
	}
	return accepted
}

func (a *Arbiter) discardHintsAt(stamp uint64) {
	kept := a.pending[:0]
	for _, candidate := range a.pending {
		if candidate.base != stamp {
			kept = append(kept, candidate)
		}
	}
	a.pending = kept
}

func classify(info StopInfo, hasHintHit bool) Reason {
	switch {
	case info.StoppedOnException:
		return ReasonException
	case info.PendingHalt || info.Cause == HaltCauseUser:
		return ReasonPause
	case info.InStepMode && !info.ContinuedFromBP:
		return ReasonStep
	case info.BreakpointEvidence:
		return ReasonBreakpoint
	case info.Cause == HaltCauseBreakpoint && (info.ContinuedFromBP || hasHintHit):
		return ReasonBreakpoint
	default:
		return ReasonUnknown
	}
}

func compactInts(values []int) []int {
	if len(values) < 2 {
		return values
	}
	n := 1
	for _, value := range values[1:] {
		if value != values[n-1] {
			values[n] = value
			n++
		}
	}
	return values[:n]
}

func (a *Arbiter) fault(format string, args ...any) ([]Effect, error) {
	a.faulted = true
	cause := fmt.Errorf(format, args...)
	return nil, fmt.Errorf("%w: %w", ErrReconciliationRequired, fmt.Errorf("%w: %v", ErrInvalidObservation, cause))
}
