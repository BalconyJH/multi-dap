package actor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

type testM5Runner struct {
	mu           sync.Mutex
	reads        int
	disassembles int
	active       int
	maximum      int
	entered      chan struct{}
	release      <-chan struct{}
	memory       []byte
}

func (r *testM5Runner) ReadMemory(_ context.Context, _ *bridge.Client, _ int, _ uint64, _ uint64) ([]byte, error) {
	r.mu.Lock()
	r.reads++
	r.active++
	if r.active > r.maximum {
		r.maximum = r.active
	}
	r.mu.Unlock()
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.release != nil {
		<-r.release
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	if r.memory != nil {
		return r.memory, nil
	}
	return []byte{1, 2, 3}, nil
}

func (r *testM5Runner) Disassemble(context.Context, *bridge.Client, int, uint64, int64, uint32) ([]multi.Instruction, error) {
	r.mu.Lock()
	r.disassembles++
	r.mu.Unlock()
	return []multi.Instruction{{Address: 0x1000, Bytes: []byte{0, 1}, Text: "nop"}}, nil
}

func newM5Actor(t *testing.T, b *testBridge, runner M5Runner) *Actor {
	t.Helper()
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(),
		PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8, M5: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func TestM5RejectsRunningStaleUnknownAndOutOfBoundsRequests(t *testing.T) {
	runner := &testM5Runner{}
	runningActor := newM5Actor(t, newTestBridge(t, running(1)), runner)
	if err := runningActor.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runningActor.ReadMemory(context.Background(), MemoryReadRequest{CoreID: 0, Count: 1}); err == nil {
		t.Fatal("ReadMemory while running succeeded")
	}

	b := newTestBridge(t, stopped(7))
	a := newM5Actor(t, b, runner)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	stale := uint64(6)
	for name, request := range map[string]MemoryReadRequest{
		"stale":   {CoreID: 0, Count: 1, ExpectedStopEpoch: &stale},
		"unknown": {CoreID: 99, Count: 1},
		"zero":    {CoreID: 0, Count: 0},
		"large":   {CoreID: 0, Count: maxM5MemoryBytes + 1},
	} {
		if _, err := a.ReadMemory(context.Background(), request); err == nil {
			t.Fatalf("ReadMemory %s succeeded", name)
		}
	}
	for name, request := range map[string]DisassemblyRequest{
		"zero":  {CoreID: 0, InstructionCount: 0},
		"large": {CoreID: 0, InstructionCount: maxM5DisassembleEntries + 1},
		"core":  {CoreID: 99, InstructionCount: 1},
	} {
		if _, err := a.Disassemble(context.Background(), request); err == nil {
			t.Fatalf("Disassemble %s succeeded", name)
		}
	}
	runner.mu.Lock()
	reads := runner.reads
	runner.mu.Unlock()
	if reads != 0 {
		t.Fatalf("rejected M5 requests reached runner %d times", reads)
	}
}

func TestM5RunnerCallsAreSerializedAndResultsAreCopied(t *testing.T) {
	release := make(chan struct{})
	runner := &testM5Runner{entered: make(chan struct{}, 2), release: release, memory: []byte{1, 2, 3}}
	a := newM5Actor(t, newTestBridge(t, stopped(1)), runner)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	results := make(chan []byte, 2)
	errs := make(chan error, 2)
	request := MemoryReadRequest{CoreID: 0, Count: 3}
	for range 2 {
		go func() {
			value, err := a.ReadMemory(context.Background(), request)
			results <- value
			errs <- err
		}()
	}
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("first M5 runner call did not start")
	}
	select {
	case <-runner.entered:
		t.Fatal("second M5 runner call bypassed actor serialization")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		value := <-results
		if len(value) != 3 {
			t.Fatalf("memory result = %v", value)
		}
		value[0] = 99
	}
	runner.mu.Lock()
	maximum := runner.maximum
	reads := runner.reads
	runner.mu.Unlock()
	if maximum != 1 || reads != 2 {
		t.Fatalf("runner concurrency/calls = %d/%d, want 1/2", maximum, reads)
	}
	value, err := a.ReadMemory(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if value[0] != 1 {
		t.Fatalf("runner-owned memory was mutated through result: %v", value)
	}
}

func TestM5QueuedBehindStateEpochChangeDoesNotReachRunner(t *testing.T) {
	b := newTestBridge(t, stopped(1), stopped(2))
	runner := &testM5Runner{}
	a := newM5Actor(t, b, runner)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainCalls(b.calls)
	b.blockMethod("state")
	poll := make(chan error, 1)
	go func() { poll <- a.Poll(context.Background()) }()
	waitCall(t, b.calls, "state")

	readReply := make(chan reply[[]byte], 1)
	if err := a.send(context.Background(), readMemoryCommand{request: MemoryReadRequest{CoreID: 0, Count: 1}, reply: readReply}); err != nil {
		t.Fatal(err)
	}
	disassembleReply := make(chan reply[[]multi.Instruction], 1)
	if err := a.send(context.Background(), disassembleCommand{request: DisassemblyRequest{CoreID: 0, InstructionCount: 1}, reply: disassembleReply}); err != nil {
		t.Fatal(err)
	}
	snapshotReply := make(chan reply[session.Snapshot], 1)
	if err := a.send(context.Background(), snapshotCommand{reply: snapshotReply}); err != nil {
		t.Fatal(err)
	}
	if got := <-snapshotReply; got.err != nil || uint64(got.value.StopEpoch) != 1 {
		t.Fatalf("queued M5 snapshot = %#v", got)
	}
	b.unblock("state")
	if err := <-poll; err != nil {
		t.Fatal(err)
	}
	if got := <-readReply; !errors.Is(got.err, ErrInspectionStale) {
		t.Fatalf("queued ReadMemory error = %v, want stale", got.err)
	}
	if got := <-disassembleReply; !errors.Is(got.err, ErrInspectionStale) {
		t.Fatalf("queued Disassemble error = %v, want stale", got.err)
	}
	runner.mu.Lock()
	reads, disassembles := runner.reads, runner.disassembles
	runner.mu.Unlock()
	if reads != 0 || disassembles != 0 {
		t.Fatalf("stale M5 requests reached runner reads/disassembles = %d/%d", reads, disassembles)
	}
}
