package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/multi"
)

func observation(status multi.Status, stamp uint64) Observation {
	return Observation{Status: status, StopStamp: stamp, HasStopStamp: true}
}

func actionKinds(actions []Action) []ActionKind {
	if len(actions) == 0 {
		return nil
	}
	kinds := make([]ActionKind, len(actions))
	for i, action := range actions {
		kinds[i] = action.Kind
	}
	return kinds
}

func TestObserveTransitions(t *testing.T) {
	tests := []struct {
		name         string
		observations []Observation
		wantState    TargetState
		wantExec     ExecutionEpoch
		wantStop     StopEpoch
		wantEvents   [][]ActionKind
	}{
		{
			name:         "initial stopped is one canonical suspension",
			observations: []Observation{observation(multi.StatusStopped, 12)},
			wantState:    TargetStopped, wantStop: 12,
			wantEvents: [][]ActionKind{{ActionExecutionSuspended}},
		},
		{
			name: "stopped running stopped publishes ordered transitions",
			observations: []Observation{
				observation(multi.StatusStopped, 12),
				observation(multi.StatusRunning, 12),
				observation(multi.StatusStopped, 13),
			},
			wantState: TargetStopped, wantExec: 1, wantStop: 13,
			wantEvents: [][]ActionKind{
				{ActionExecutionSuspended},
				{ActionInvalidateSuspendedReferences, ActionExecutionResumed},
				{ActionExecutionSuspended},
			},
		},
		{
			name: "duplicate observations emit nothing",
			observations: []Observation{
				observation(multi.StatusStopped, 12),
				observation(multi.StatusStopped, 12),
				observation(multi.StatusRunning, 12),
				observation(multi.StatusRunning, 12),
			},
			wantState: TargetRunning, wantExec: 1, wantStop: 12,
			wantEvents: [][]ActionKind{
				{ActionExecutionSuspended},
				nil,
				{ActionInvalidateSuspendedReferences, ActionExecutionResumed},
				nil,
			},
		},
		{
			name: "different stopped stamp proves a missed cycle",
			observations: []Observation{
				observation(multi.StatusStopped, 12),
				observation(multi.StatusStopped, 13),
			},
			wantState: TargetStopped, wantExec: 1, wantStop: 13,
			wantEvents: [][]ActionKind{
				{ActionExecutionSuspended},
				{ActionInvalidateSuspendedReferences, ActionExecutionResumed, ActionExecutionSuspended},
			},
		},
		{
			name: "unknown status is not a guessed transition",
			observations: []Observation{
				observation(multi.StatusStopped, 12),
				observation(multi.StatusDying, 12),
				observation(multi.StatusStopped, 12),
			},
			wantState: TargetStopped, wantStop: 12,
			wantEvents: [][]ActionKind{{ActionExecutionSuspended}, nil, nil},
		},
		{
			name: "unknown interval preserves a later stopped to running transition",
			observations: []Observation{
				observation(multi.StatusStopped, 12),
				observation(multi.StatusDying, 12),
				observation(multi.StatusRunning, 12),
			},
			wantState: TargetRunning, wantExec: 1, wantStop: 12,
			wantEvents: [][]ActionKind{
				{ActionExecutionSuspended},
				nil,
				{ActionInvalidateSuspendedReferences, ActionExecutionResumed},
			},
		},
		{
			name: "unknown interval preserves a later running to stopped transition",
			observations: []Observation{
				observation(multi.StatusRunning, 12),
				observation(multi.StatusDying, 12),
				observation(multi.StatusStopped, 13),
			},
			wantState: TargetStopped, wantStop: 13,
			wantEvents: [][]ActionKind{
				nil,
				nil,
				{ActionExecutionSuspended},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := New(Options{})
			for index, input := range test.observations {
				result, err := r.Observe(input)
				if err != nil {
					t.Fatalf("Observe(%d) error = %v", index, err)
				}
				if got := actionKinds(result.Actions); !reflect.DeepEqual(got, test.wantEvents[index]) {
					t.Fatalf("Observe(%d) action kinds = %v, want %v", index, got, test.wantEvents[index])
				}
			}
			snapshot := r.Snapshot()
			if snapshot.State != test.wantState || snapshot.ExecutionEpoch != test.wantExec || snapshot.StopEpoch != test.wantStop {
				t.Fatalf("Snapshot() = state=%v execution=%d stop=%d, want state=%v execution=%d stop=%d", snapshot.State, snapshot.ExecutionEpoch, snapshot.StopEpoch, test.wantState, test.wantExec, test.wantStop)
			}
		})
	}
}

func TestStoppedToRunningInvalidatesBeforeResumed(t *testing.T) {
	r := New(Options{})
	if _, err := r.Observe(observation(multi.StatusStopped, 4)); err != nil {
		t.Fatal(err)
	}
	result, err := r.Observe(observation(multi.StatusRunning, 4))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := actionKinds(result.Actions), []ActionKind{ActionInvalidateSuspendedReferences, ActionExecutionResumed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("action order = %v, want %v", got, want)
	}
	if result.Actions[0].ExecutionEpoch != 1 || result.Actions[1].ExecutionEpoch != 1 {
		t.Fatalf("resumption actions carry execution epochs %d and %d, want 1", result.Actions[0].ExecutionEpoch, result.Actions[1].ExecutionEpoch)
	}
}

func TestObservationFailuresFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		inputs []Observation
	}{
		{
			name: "missing stamp after a stopped observation",
			inputs: []Observation{
				observation(multi.StatusStopped, 8),
				{Status: multi.StatusRunning},
			},
		},
		{
			name: "stamp rollback",
			inputs: []Observation{
				observation(multi.StatusStopped, 8),
				observation(multi.StatusStopped, 7),
			},
		},
		{
			name: "running to stopped must advance the generation",
			inputs: []Observation{
				observation(multi.StatusStopped, 8),
				observation(multi.StatusRunning, 8),
				observation(multi.StatusStopped, 8),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := New(Options{})
			for index, input := range test.inputs {
				result, err := r.Observe(input)
				if index != len(test.inputs)-1 {
					if err != nil {
						t.Fatalf("Observe(%d) error = %v", index, err)
					}
					continue
				}
				if !errors.Is(err, ErrReconciliationRequired) {
					t.Fatalf("Observe(%d) error = %v, want ErrReconciliationRequired", index, err)
				}
				if result.Snapshot.State != TargetFaulted {
					t.Fatalf("state = %v, want TargetFaulted", result.Snapshot.State)
				}
				if got, want := actionKinds(result.Actions), []ActionKind{ActionInvalidateSuspendedReferences, ActionReconciliationRequired}; !reflect.DeepEqual(got, want) {
					t.Fatalf("actions = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestConfigurationBarrierFoldsChurn(t *testing.T) {
	r := New(Options{})
	if _, err := r.BeginConfiguration(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		input Observation
		want  []ActionKind
	}{
		{observation(multi.StatusStopped, 20), nil},
		{observation(multi.StatusRunning, 20), []ActionKind{ActionInvalidateSuspendedReferences}},
		{observation(multi.StatusStopped, 21), nil},
	}
	for _, test := range tests {
		result, err := r.Observe(test.input)
		if err != nil {
			t.Fatal(err)
		}
		if got := actionKinds(result.Actions); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("barrier actions = %v, want %v", got, test.want)
		}
	}

	result, err := r.ConfigurationDone()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := actionKinds(result.Actions), []ActionKind{ActionExecutionSuspended}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigurationDone() actions = %v, want %v", got, want)
	}
	if result.Snapshot.ExecutionEpoch != 1 || result.Snapshot.StopEpoch != 21 {
		t.Fatalf("ConfigurationDone() snapshot = execution=%d stop=%d, want execution=1 stop=21", result.Snapshot.ExecutionEpoch, result.Snapshot.StopEpoch)
	}
}

func TestControlLeaseAndLifecycleGate(t *testing.T) {
	r := New(Options{RequireResetAfterDownload: true})
	owner := ControllerID("frontend-a")
	other := ControllerID("frontend-b")

	if err := r.AcquireControl(owner); err != nil {
		t.Fatalf("AcquireControl() error = %v", err)
	}
	if err := r.AcquireControl(other); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second AcquireControl() error = %v, want ErrLeaseHeld", err)
	}
	if err := r.RequireControl(other); !errors.Is(err, ErrNotLeaseHolder) {
		t.Fatalf("RequireControl(other) error = %v, want ErrNotLeaseHolder", err)
	}

	r.NoteDownload()
	if err := r.RequireResume(owner); !errors.Is(err, ErrResetRequired) {
		t.Fatalf("RequireResume() after download error = %v, want ErrResetRequired", err)
	}
	r.NoteReset()
	if err := r.RequireResume(owner); err != nil {
		t.Fatalf("RequireResume() after reset error = %v", err)
	}
	if err := r.ReleaseControl(other); !errors.Is(err, ErrNotLeaseHolder) {
		t.Fatalf("ReleaseControl(other) error = %v, want ErrNotLeaseHolder", err)
	}
	if err := r.ReleaseControl(owner); err != nil {
		t.Fatalf("ReleaseControl(owner) error = %v", err)
	}
	if err := r.AcquireControl(other); err != nil {
		t.Fatalf("AcquireControl(other) after release error = %v", err)
	}
}

func TestLifecycleGateCanBeDisabled(t *testing.T) {
	r := New(Options{})
	owner := ControllerID("frontend")
	if err := r.AcquireControl(owner); err != nil {
		t.Fatal(err)
	}
	r.NoteDownload()
	if err := r.RequireResume(owner); err != nil {
		t.Fatalf("RequireResume() with disabled gate error = %v", err)
	}
}
