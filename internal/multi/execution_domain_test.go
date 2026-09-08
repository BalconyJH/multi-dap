package multi

import (
	"context"
	"errors"
	"testing"
)

type fakeExecutionExecutor struct {
	requests []ExecutionRequest
	samples  executionSamples
}

func (f *fakeExecutionExecutor) Execute(_ context.Context, _ *Topology, _ *Driver, request ExecutionRequest) (executionSamples, error) {
	f.requests = append(f.requests, request)
	return executionSamples{
		before: append([]CoreExecutionObservation(nil), f.samples.before...),
		after:  append([]CoreExecutionObservation(nil), f.samples.after...),
	}, nil
}

func fixtureExecutionTopology() *Topology {
	return &Topology{
		cores: []TopologyCore{{ID: 0, ELF: `X:\fixture\core0.elf`}, {ID: 4, ELF: `X:\fixture\core4.elf`}},
		routes: map[int]topologyRoute{
			0: {component: "fixture.component.0", elf: `x:\fixture\core0.elf`},
			4: {component: "fixture.component.4", elf: `x:\fixture\core4.elf`},
		},
	}
}

func TestExecutionDomainDefaultIsUnavailableBeforeDriverIO(t *testing.T) {
	domain, err := NewExecutionDomain(fixtureExecutionTopology())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domain.ScopeForCore(4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.Execute(context.Background(), nil, ExecutionRequest{Operation: ExecutionNext, Scope: scope}); !errors.Is(err, ErrExecutionDomainUnavailable) {
		t.Fatalf("Execute() error = %v, want ErrExecutionDomainUnavailable", err)
	}
}

func TestExecutionDomainTransitionNeedsExactScopeAndAllThreadsTruth(t *testing.T) {
	domain, err := NewExecutionDomain(fixtureExecutionTopology())
	if err != nil {
		t.Fatal(err)
	}
	one, err := domain.ScopeForCore(4)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeExecutionExecutor{samples: executionSamples{
		before: []CoreExecutionObservation{{Core: 4, Status: StatusStopped, StopStamp: 7, HasStopStamp: true}},
		after:  []CoreExecutionObservation{{Core: 4, Status: StatusRunning, StopStamp: 7, HasStopStamp: true}},
	}}
	domain.executor = fake
	partial, err := domain.Execute(context.Background(), &Driver{}, ExecutionRequest{Operation: ExecutionContinue, Scope: one})
	if err != nil {
		t.Fatal(err)
	}
	if truth := domain.Truth(partial); truth.AllThreadsContinued || truth.AllThreadsStopped {
		t.Fatalf("single-core truth = %#v", truth)
	}

	all, err := domain.AllConfiguredScope()
	if err != nil {
		t.Fatal(err)
	}
	fake.samples = executionSamples{
		before: []CoreExecutionObservation{
			{Core: 4, Status: StatusStopped, StopStamp: 7, HasStopStamp: true},
			{Core: 0, Status: StatusStopped, StopStamp: 8, HasStopStamp: true},
		},
		after: []CoreExecutionObservation{
			{Core: 4, Status: StatusRunning, StopStamp: 7, HasStopStamp: true},
			{Core: 0, Status: StatusRunning, StopStamp: 8, HasStopStamp: true},
		},
	}
	confirmed, err := domain.Execute(context.Background(), &Driver{}, ExecutionRequest{Operation: ExecutionContinue, Scope: all})
	if err != nil {
		t.Fatal(err)
	}
	if got := confirmed.Transition().After().Samples(); got[0].Core != 0 || got[1].Core != 4 {
		t.Fatalf("After().Samples() = %#v, want stable scope order", got)
	}
	if truth := domain.Truth(confirmed); !truth.AllThreadsContinued || truth.AllThreadsStopped {
		t.Fatalf("all-core truth = %#v", truth)
	}
}

func TestExecutionDomainRejectsPartialUnstableAndNonTransitioningEvidence(t *testing.T) {
	domain, err := NewExecutionDomain(fixtureExecutionTopology())
	if err != nil {
		t.Fatal(err)
	}
	all, err := domain.AllConfiguredScope()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeExecutionExecutor{}
	domain.executor = fake
	for _, samples := range []executionSamples{
		{before: []CoreExecutionObservation{{Core: 0, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}}, after: []CoreExecutionObservation{{Core: 0, Status: StatusRunning, StopStamp: 1, HasStopStamp: true}, {Core: 4, Status: StatusRunning, StopStamp: 1, HasStopStamp: true}}},
		{before: []CoreExecutionObservation{{Core: 0, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}, {Core: 0, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}}, after: []CoreExecutionObservation{{Core: 0, Status: StatusRunning, StopStamp: 1, HasStopStamp: true}, {Core: 4, Status: StatusRunning, StopStamp: 1, HasStopStamp: true}}},
		{before: []CoreExecutionObservation{{Core: 0, Status: StatusStopped, StopStamp: 1, HasStopStamp: false}, {Core: 4, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}}, after: []CoreExecutionObservation{{Core: 0, Status: StatusRunning, StopStamp: 1, HasStopStamp: true}, {Core: 4, Status: StatusRunning, StopStamp: 1, HasStopStamp: true}}},
		{before: []CoreExecutionObservation{{Core: 0, Status: StatusNil, StopStamp: 1, HasStopStamp: true}, {Core: 4, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}}, after: []CoreExecutionObservation{{Core: 0, Status: StatusRunning, StopStamp: 1, HasStopStamp: true}, {Core: 4, Status: StatusRunning, StopStamp: 1, HasStopStamp: true}}},
		{before: []CoreExecutionObservation{{Core: 0, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}, {Core: 4, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}}, after: []CoreExecutionObservation{{Core: 0, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}, {Core: 4, Status: StatusStopped, StopStamp: 1, HasStopStamp: true}}},
	} {
		fake.samples = samples
		if _, err := domain.Execute(context.Background(), &Driver{}, ExecutionRequest{Operation: ExecutionContinue, Scope: all}); err == nil {
			t.Fatalf("Execute(%#v) succeeded", samples)
		}
	}
}

func TestExecutionDomainExecutorMustReturnCompleteObservation(t *testing.T) {
	domain, err := NewExecutionDomain(fixtureExecutionTopology())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domain.AllConfiguredScope()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeExecutionExecutor{samples: executionSamples{
		before: []CoreExecutionObservation{
			{Core: 0, Status: StatusStopped, StopStamp: 10, HasStopStamp: true},
			{Core: 4, Status: StatusStopped, StopStamp: 11, HasStopStamp: true},
		},
		after: []CoreExecutionObservation{
			{Core: 0, Status: StatusRunning, StopStamp: 10, HasStopStamp: true},
			{Core: 4, Status: StatusRunning, StopStamp: 11, HasStopStamp: true},
		},
	}}
	domain.executor = fake
	observation, err := domain.Execute(context.Background(), &Driver{}, ExecutionRequest{Operation: ExecutionContinue, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("executor requests = %#v", fake.requests)
	}
	if truth := domain.Truth(observation); !truth.AllThreadsContinued || truth.AllThreadsStopped {
		t.Fatalf("truth = %#v", truth)
	}

	fake.samples.after = fake.samples.after[:1]
	if _, err := domain.Execute(context.Background(), &Driver{}, ExecutionRequest{Operation: ExecutionContinue, Scope: scope}); err == nil {
		t.Fatal("partial executor observation was accepted")
	}
}
