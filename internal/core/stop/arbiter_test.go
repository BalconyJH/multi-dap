package stop

import (
	"errors"
	"reflect"
	"testing"
)

type tokenMap map[uint32]HintTarget

func (m tokenMap) ResolveHint(token uint32) (HintTarget, bool) {
	value, ok := m[token]
	return value, ok
}

func observed(state State, stamp uint64) Observation {
	return Observation{State: state, StopStamp: stamp, HasStopStamp: true}
}

func effectKinds(effects []Effect) []EffectKind {
	if len(effects) == 0 {
		return nil
	}
	got := make([]EffectKind, len(effects))
	for i := range effects {
		got[i] = effects[i].Kind
	}
	return got
}

func TestArbiterReplayTransitions(t *testing.T) {
	tests := []struct {
		name  string
		trace []Observation
		want  [][]EffectKind
	}{
		{"ordinary transition", []Observation{observed(StateStopped, 12), observed(StateRunning, 12), observed(StateStopped, 13)}, [][]EffectKind{{EffectStopped}, {EffectInvalidateSuspended, EffectContinued}, {EffectStopped}}},
		{"duplicate stopped is deduplicated", []Observation{observed(StateStopped, 12), observed(StateStopped, 12)}, [][]EffectKind{{EffectStopped}, nil}},
		{"missed stopped cycle is reported late", []Observation{observed(StateStopped, 12), observed(StateStopped, 13)}, [][]EffectKind{{EffectStopped}, {EffectInvalidateSuspended, EffectContinued, EffectStopped}}},
		{"missed running cycle is reported in target order", []Observation{observed(StateStopped, 12), observed(StateRunning, 13)}, [][]EffectKind{{EffectStopped}, {EffectInvalidateSuspended, EffectStopped, EffectContinued}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := New(nil)
			for index, item := range test.trace {
				effects, err := a.Observe(item)
				if err != nil {
					t.Fatalf("Observe(%d): %v", index, err)
				}
				if got := effectKinds(effects); !reflect.DeepEqual(got, test.want[index]) {
					t.Fatalf("effects %v, want %v", got, test.want[index])
				}
			}
		})
	}
}

func TestHintIsOnlyEvidenceForAConfirmedStop(t *testing.T) {
	a := New(tokenMap{7: {BreakpointID: 42, Core: 3}})
	if _, err := a.Observe(observed(StateStopped, 5)); err != nil {
		t.Fatal(err)
	}
	if !a.NoteHint(7) {
		t.Fatal("stopped hint did not request an authoritative refresh")
	}
	if effects, err := a.Observe(observed(StateStopped, 5)); err != nil || len(effects) != 0 {
		t.Fatalf("duplicate state refresh = %#v, %v", effects, err)
	}
	if _, err := a.Observe(observed(StateRunning, 5)); err != nil {
		t.Fatal(err)
	}
	if !a.NoteHint(7) {
		t.Fatal("running hint did not request a refresh")
	}
	effects, err := a.Observe(Observation{State: StateStopped, StopStamp: 6, HasStopStamp: true, Info: StopInfo{Cause: HaltCauseBreakpoint, ContinuedFromBP: true}})
	if err != nil {
		t.Fatal(err)
	}
	stopped := effects[len(effects)-1]
	if stopped.Reason != ReasonBreakpoint || !reflect.DeepEqual(stopped.HitBreakpointIDs, []int{42}) || !stopped.HasCore || stopped.Core != 3 {
		t.Fatalf("stopped = %#v", stopped)
	}
	if !a.NoteHint(7) {
		t.Fatal("post-stop hint did not request a deduplicating refresh")
	}
	if effects, err := a.Observe(observed(StateStopped, 6)); err != nil || len(effects) != 0 {
		t.Fatalf("post-stop duplicate = %#v, %v", effects, err)
	}
}

func TestReasonIsConservativeWhenHaltCauseIsStale(t *testing.T) {
	tests := []struct {
		name string
		info StopInfo
		want Reason
	}{
		{"exception wins", StopInfo{Cause: HaltCauseBreakpoint, ContinuedFromBP: true, StoppedOnException: true}, ReasonException},
		{"pending halt defeats stale breakpoint", StopInfo{Cause: HaltCauseBreakpoint, ContinuedFromBP: true, PendingHalt: true}, ReasonPause},
		{"unconfirmed textual breakpoint is unknown", StopInfo{Cause: HaltCauseBreakpoint}, ReasonUnknown},
		{"break register corroborates", StopInfo{BreakpointEvidence: true}, ReasonBreakpoint},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classify(test.info, false); got != test.want {
				t.Fatalf("classify = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInvalidStopStampFailsClosed(t *testing.T) {
	a := New(nil)
	if _, err := a.Observe(observed(StateStopped, 7)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Observe(observed(StateStopped, 6)); !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("error = %v", err)
	}
	if _, err := a.Observe(observed(StateStopped, 7)); !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("fault did not persist: %v", err)
	}
}

func FuzzArbiterNeverPublishesHintAlone(f *testing.F) {
	f.Add(uint8(StateStopped), uint64(1), uint8(StateRunning), uint64(1))
	f.Add(uint8(StateStopped), uint64(7), uint8(StateStopped), uint64(8))
	f.Fuzz(func(t *testing.T, firstState uint8, firstStamp uint64, secondState uint8, secondStamp uint64) {
		a := New(tokenMap{1: {BreakpointID: 99, Core: 0}})
		_ = a.NoteHint(1) // ignored before a running observation.
		for _, observation := range []Observation{
			{State: State(firstState % 3), StopStamp: firstStamp, HasStopStamp: true},
			{State: State(secondState % 3), StopStamp: secondStamp, HasStopStamp: true},
		} {
			effects, _ := a.Observe(observation)
			for _, effect := range effects {
				if effect.Kind == EffectStopped && effect.StopEpoch == 0 && observation.StopStamp != 0 {
					t.Fatalf("stopped effect lost its stop stamp: %#v", effect)
				}
			}
		}
	})
}
