package dap

import "fmt"

type LifecyclePhase uint8

const (
	AwaitInitialize LifecyclePhase = iota
	AwaitAttach
	Configuring
	Attached
	Disconnected
)

type Lifecycle struct {
	Phase            LifecyclePhase
	PendingAttachSeq int64
}

func NewLifecycle() Lifecycle { return Lifecycle{Phase: AwaitInitialize} }

type LifecycleEventKind uint8

const (
	LifecycleInitialize LifecycleEventKind = iota
	LifecycleAttach
	LifecycleConfigurationDone
	LifecycleTargetChanged
	LifecycleDisconnect
)

type LifecycleEvent struct {
	Kind       LifecycleEventKind
	RequestSeq int64
}

type LifecycleEffectKind uint8

const (
	EffectInitializeResponse LifecycleEffectKind = iota
	EffectInitialized
	EffectConfigurationDoneResponse
	EffectAttachResponse
	EffectPublishTargetState
	EffectDisconnectResponse
)

type LifecycleEffect struct {
	Kind       LifecycleEffectKind
	RequestSeq int64
}

// ReduceLifecycle enforces the attach ordering in architecture.md §9.2. It is
// intentionally unaware of target state: churn during Configuring produces no
// effect and the final canonical state is read only at the attach barrier.
func ReduceLifecycle(state Lifecycle, event LifecycleEvent) (Lifecycle, []LifecycleEffect, error) {
	switch event.Kind {
	case LifecycleInitialize:
		if state.Phase != AwaitInitialize {
			return state, nil, fmt.Errorf("initialize is only valid before attach")
		}
		state.Phase = AwaitAttach
		return state, []LifecycleEffect{{Kind: EffectInitializeResponse, RequestSeq: event.RequestSeq}}, nil
	case LifecycleAttach:
		if state.Phase != AwaitAttach {
			return state, nil, fmt.Errorf("attach requires initialize response")
		}
		state.Phase = Configuring
		state.PendingAttachSeq = event.RequestSeq
		return state, []LifecycleEffect{{Kind: EffectInitialized}}, nil
	case LifecycleConfigurationDone:
		if state.Phase != Configuring {
			return state, nil, fmt.Errorf("configurationDone requires an attach configuration window")
		}
		state.Phase = Attached
		return state, []LifecycleEffect{
			{Kind: EffectConfigurationDoneResponse, RequestSeq: event.RequestSeq},
			{Kind: EffectAttachResponse, RequestSeq: state.PendingAttachSeq},
		}, nil
	case LifecycleTargetChanged:
		if state.Phase == Configuring {
			return state, nil, nil
		}
		if state.Phase != Attached {
			return state, nil, fmt.Errorf("target state cannot be published before attach")
		}
		return state, []LifecycleEffect{{Kind: EffectPublishTargetState}}, nil
	case LifecycleDisconnect:
		if state.Phase == Disconnected {
			return state, nil, fmt.Errorf("client is already disconnected")
		}
		state.Phase = Disconnected
		return state, []LifecycleEffect{{Kind: EffectDisconnectResponse, RequestSeq: event.RequestSeq}}, nil
	default:
		return state, nil, fmt.Errorf("unknown lifecycle event")
	}
}
