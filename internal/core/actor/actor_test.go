package actor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/handle"
	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/mbp"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

type testBridge struct {
	mu            sync.Mutex
	states        []map[string]any
	components    string
	calls         chan string
	block         map[string]chan struct{}
	fail          map[string]*mbp.Error
	commandRaw    string
	commandRawFor map[string]string
	console       map[string]any
	client        *bridge.Client
	executor      *bridge.Executor
}

const bridgeCommandMethod = "run_" + "commands"

func newTestBridge(t *testing.T, states ...map[string]any) *testBridge {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	b := &testBridge{states: states, components: "fixture.component.5 (debugger.name.core0)\nfixture.component.6 (debugger.name.core1)\n", calls: make(chan string, 32), block: make(map[string]chan struct{}), fail: make(map[string]*mbp.Error), commandRaw: "Process not running.\n", commandRawFor: make(map[string]string), console: map[string]any{"server": "", "io": ""}}
	go b.serve(serverConn)
	client, err := bridge.NewClient(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	b.client = client
	b.executor = bridge.NewExecutor(client)
	t.Cleanup(func() { b.executor.Close() })
	return b
}

func stopped(stamp uint64) map[string]any {
	return map[string]any{"status": 2, "process_info": map[string]string{"stopStamp": itoa(stamp), "pid": "1234"}}
}
func running(stamp uint64) map[string]any {
	return map[string]any{"status": 3, "process_info": map[string]string{"stopStamp": itoa(stamp), "pid": "1234"}}
}
func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

func (b *testBridge) serve(conn net.Conn) {
	defer conn.Close()
	_, _ = conn.Write([]byte(`{"protocol_version":1,"bridge_version":"test","max_message_size":1048576,"encoding":"utf-8"}` + "\n"))
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var request mbp.Request
		if json.Unmarshal(line, &request) != nil {
			return
		}
		b.calls <- request.Method
		b.mu.Lock()
		gate := b.block[request.Method]
		b.mu.Unlock()
		if gate != nil {
			<-gate
		}
		b.mu.Lock()
		failure := b.fail[request.Method]
		b.mu.Unlock()
		var response mbp.Response
		if failure != nil {
			response = mbp.Response{ID: request.ID, OK: false, Error: failure}
		} else {
			response = mbp.Response{ID: request.ID, OK: true, Result: b.result(request)}
		}
		encoded, _ := json.Marshal(response)
		if _, err := conn.Write(append(encoded, '\n')); err != nil {
			return
		}
	}
}

func (b *testBridge) result(request mbp.Request) json.RawMessage {
	method := request.Method
	b.mu.Lock()
	defer b.mu.Unlock()
	var value any
	switch method {
	case "open":
		value = map[string]bool{"opened": true}
	case "close":
		value = map[string]bool{"closed": true}
	case "cores":
		value = map[string]any{"components": b.components, "status": 1}
	case "resume", "halt", "step_in", "next":
		value = map[string]bool{"accepted": true}
	case bridgeCommandMethod:
		var params map[string]string
		_ = json.Unmarshal(request.Params, &params)
		command := params["commands"]
		if raw, ok := b.commandRawFor[command]; ok {
			value = map[string]any{"raw": raw, "status": 1}
			break
		}
		switch command {
		case "route fixture.component.5 P":
			value = map[string]any{"raw": fixtureProcessTable(`C:\fixture\core0.elf`), "status": 1}
		case "route fixture.component.6 P":
			value = map[string]any{"raw": fixtureProcessTable(`C:\fixture\core1.elf`), "status": 1}
		default:
			value = map[string]any{"raw": b.commandRaw, "status": 1}
		}
	case "state":
		if len(b.states) == 0 {
			value = stopped(1)
		} else {
			value = b.states[0]
			b.states = b.states[1:]
		}
	case "console_read":
		value = b.console
	case "console_reset":
		value = map[string]bool{"reset": true}
	default:
		value = map[string]any{}
	}
	raw, _ := json.Marshal(value)
	return raw
}

func (b *testBridge) setConsole(server, io string, lossy bool) {
	b.mu.Lock()
	b.console = map[string]any{"server": server, "io": io, "raw_lossy": lossy}
	b.mu.Unlock()
}

func fixtureProcessTable(program string) string {
	return "     # PID        PPID       Status        CBEFITDHR Name and Arguments\n" +
		">>   0 0x1 N/A        Stopped       011111101 " + program + "\n"
}

func (b *testBridge) setCommandRaw(raw string) {
	b.mu.Lock()
	b.commandRaw = raw
	b.mu.Unlock()
}

func (b *testBridge) setComponents(components string) {
	b.mu.Lock()
	b.components = components
	b.mu.Unlock()
}

func (b *testBridge) setCommandRawFor(command, raw string) {
	b.mu.Lock()
	b.commandRawFor[command] = raw
	b.mu.Unlock()
}

func (b *testBridge) blockMethod(method string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	gate := make(chan struct{})
	b.block[method] = gate
	return gate
}
func (b *testBridge) unblock(method string) {
	b.mu.Lock()
	gate := b.block[method]
	delete(b.block, method)
	b.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}
func (b *testBridge) appendStates(states ...map[string]any) {
	b.mu.Lock()
	b.states = append(b.states, states...)
	b.mu.Unlock()
}

func (b *testBridge) failMethod(method, kind, message string) {
	b.mu.Lock()
	b.fail[method] = &mbp.Error{Kind: kind, Message: message, Raw: ""}
	b.mu.Unlock()
}

func (b *testBridge) clearFailure(method string) {
	b.mu.Lock()
	delete(b.fail, method)
	b.mu.Unlock()
}

func newActor(t *testing.T, b *testBridge, handles *handle.Store) *Actor {
	return newActorWithCoreSpecs(t, b, handles, testCoreSpecs())
}

func newSingleCoreActor(t *testing.T, b *testBridge, handles *handle.Store) *Actor {
	b.setComponents("fixture.component.5 (debugger.name.core0)\n")
	return newActorWithCoreSpecs(t, b, handles, testCoreSpecs()[:1])
}

func newActorWithCoreSpecs(t *testing.T, b *testBridge, handles *handle.Store, specs []multi.CoreSpec) *Actor {
	t.Helper()
	a, err := New(Options{Executor: b.executor, Open: openRequest(), CoreSpecs: specs, PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8, Handles: handles})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}
func openRequest() (r multi.OpenRequest) { return multi.OpenRequest{Project: "p", Connection: "c"} }
func testCoreSpecs() []multi.CoreSpec {
	return []multi.CoreSpec{{ID: 0, ELF: `C:\fixture\core0.elf`}, {ID: 1, ELF: `C:\fixture\core1.elf`}}
}

func newConsoleActor(t *testing.T, b *testBridge) *Actor {
	t.Helper()
	a, err := New(Options{Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: 5 * time.Millisecond, ConsoleInterval: 5 * time.Millisecond, RPCDeadline: time.Second, EventBuffer: 8, ConsoleEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

type blockingCommand struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (c blockingCommand) apply(*runtime) {
	c.entered <- struct{}{}
	<-c.release
}

type noReplyCommand struct{}

func (noReplyCommand) apply(*runtime) {}

func TestCloseClosesSubmissionGateAndWaitsForLoopExit(t *testing.T) {
	b := newTestBridge(t)
	a := newActor(t, b, nil)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	if err := a.send(context.Background(), blockingCommand{entered: entered, release: release}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actor did not start blocking command")
	}

	closed := make(chan struct{})
	go func() {
		a.Close()
		close(closed)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := a.send(ctx, noReplyCommand{})
		cancel()
		if errors.Is(err, ErrClosed) {
			break
		}
		if err != nil {
			t.Fatalf("submission while Close begins = %v, want accepted or ErrClosed", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not close the submission gate")
		}
	}

	select {
	case <-closed:
		t.Fatal("Close returned before the actor loop could exit")
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after the actor loop exited")
	}
	select {
	case <-a.done:
	default:
		t.Fatal("Close returned before done closed")
	}
	for range 8 {
		if err := a.send(context.Background(), noReplyCommand{}); !errors.Is(err, ErrClosed) {
			t.Fatalf("submission after Close = %v, want ErrClosed", err)
		}
	}
}

func TestCloseAndSendConcurrentNeverStrandsSubmission(t *testing.T) {
	for range 50 {
		b := newTestBridge(t)
		a := newActor(t, b, nil)
		start := make(chan struct{})
		results := make(chan error, 16)
		var senders sync.WaitGroup
		for range cap(results) {
			senders.Add(1)
			go func() {
				defer senders.Done()
				<-start
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				results <- a.send(ctx, noReplyCommand{})
			}()
		}
		closed := make(chan struct{})
		go func() {
			<-start
			a.Close()
			close(closed)
		}()
		close(start)
		senders.Wait()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("concurrent Close did not finish")
		}
		close(results)
		for err := range results {
			if err != nil && !errors.Is(err, ErrClosed) {
				t.Fatalf("concurrent submission = %v, want nil or ErrClosed", err)
			}
		}
		if err := a.send(context.Background(), noReplyCommand{}); !errors.Is(err, ErrClosed) {
			t.Fatalf("submission after concurrent Close = %v, want ErrClosed", err)
		}
	}
}

func TestQueuedPublicRequestReturnsClosedWhenCloseStopsLoop(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a := newActor(t, b, nil)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.blockMethod("state")
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	if err := a.send(context.Background(), blockingCommand{entered: entered, release: release}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actor did not enter command barrier")
	}

	result := make(chan error, 1)
	go func() { result <- a.Poll(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for len(a.commands) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Poll was not queued behind the command barrier")
		}
		time.Sleep(time.Millisecond)
	}
	closed := make(chan struct{})
	go func() {
		a.Close()
		close(closed)
	}()
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("queued Poll() error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued Poll() remained blocked after Close")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish queued-request shutdown")
	}
	b.unblock("state")
}

func TestInflightPublicRequestReturnsClosedWhenCloseStopsLoop(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a := newActor(t, b, nil)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.blockMethod("state")
	result := make(chan error, 1)
	go func() { result <- a.Poll(context.Background()) }()
	waitCall(t, b.calls, "state")
	closed := make(chan struct{})
	go func() {
		a.Close()
		close(closed)
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("inflight Poll() error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("inflight Poll() remained blocked after Close")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish inflight-request shutdown")
	}
	b.unblock("state")
}

func TestRepresentativePublicMethodsRejectAfterClose(t *testing.T) {
	b := newTestBridge(t)
	a := newActor(t, b, nil)
	a.Close()
	checks := map[string]func() error{
		"Open": func() error { return a.Open(context.Background()) },
		"Attach": func() error {
			_, err := a.Attach(context.Background(), "frontend")
			return err
		},
		"Snapshot": func() error {
			_, err := a.Snapshot(context.Background())
			return err
		},
		"SetBreakpoints": func() error {
			_, err := a.SetBreakpoints(context.Background(), "frontend", source.Identity{Key: "src/main.c"}, []int{1})
			return err
		},
		"ReadMemory": func() error {
			_, err := a.ReadMemory(context.Background(), MemoryReadRequest{CoreID: 0, Address: 1, Count: 1})
			return err
		},
		"ResetConsole": func() error { return a.ResetConsole(context.Background()) },
	}
	for name, check := range checks {
		if err := check(); !errors.Is(err, ErrClosed) {
			t.Fatalf("%s() after Close = %v, want ErrClosed", name, err)
		}
	}
}

func TestConsolePollingRequiresAttachedConfiguredFrontend(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.setConsole("target\n", "uart\n", false)
	a := newConsoleActor(t, b)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Before attach, periodic state sampling is allowed but console collection
	// is not: it would otherwise drain pane history with no frontend.
	time.Sleep(25 * time.Millisecond)
	assertNoCall(t, b.calls, "console_read")
	attachment, err := a.Attach(context.Background(), "console-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.BeginConfiguration(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	assertNoCall(t, b.calls, "console_read")
	if err := a.ConfigurationDone(context.Background()); err != nil {
		t.Fatal(err)
	}
	var event Event
	for {
		event = receiveEvent(t, attachment.Events)
		if event.Kind == EventOutput {
			break
		}
	}
	if event.Kind != EventOutput || event.Console.Server != "target\n" || event.Console.IO != "uart\n" {
		t.Fatalf("console event = %#v", event)
	}
}

func TestConsoleFailureDisablesCollectionWithoutFaultingActor(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.failMethod("console_read", "refused", `C:\sensitive\detail`)
	a := newConsoleActor(t, b)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	attachment, err := a.Attach(context.Background(), "console-test")
	if err != nil {
		t.Fatal(err)
	}
	event := receiveEvent(t, attachment.Events)
	if event.Kind != EventOutput || !event.ConsoleUnavailable {
		t.Fatalf("console failure event = %#v", event)
	}
	assertNoEvent(t, attachment.Events)
	select {
	case fault := <-a.Faults():
		t.Fatalf("console error faulted actor: %v", fault)
	default:
	}
}

func TestConsoleTimeoutFailClosesSharedBridgeImmediately(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.blockMethod("console_read")
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(),
		PollInterval: 5 * time.Millisecond, ConsoleInterval: 5 * time.Millisecond, RPCDeadline: 20 * time.Millisecond,
		EventBuffer: 8, ConsoleEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Attach(context.Background(), "console-timeout"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, b.calls, "console_read")
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationConsole || !errors.Is(fault, context.DeadlineExceeded) || !errors.Is(fault, ErrReconciliationNeeded) {
		t.Fatalf("console timeout fault = %#v", fault)
	}
}

func TestConsoleReadDoesNotCrossDetachReattachGeneration(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.setConsole("old frontend text\n", "", false)
	b.blockMethod("console_read")
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(),
		PollInterval: 5 * time.Millisecond, ConsoleInterval: 100 * time.Millisecond,
		RPCDeadline: time.Second, EventBuffer: 8, ConsoleEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Attach(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, b.calls, "console_read")
	if err := a.Detach(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	newAttachment, err := a.Attach(context.Background(), "new")
	if err != nil {
		t.Fatal(err)
	}
	b.unblock("console_read")
	assertNoEvent(t, newAttachment.Events)
}

func TestResetConsoleSerializesAfterInFlightRead(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.blockMethod("console_read")
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(),
		PollInterval: 5 * time.Millisecond, ConsoleInterval: 100 * time.Millisecond,
		RPCDeadline: time.Second, EventBuffer: 8, ConsoleEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Attach(context.Background(), "console-reset"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, b.calls, "console_read")
	result := make(chan error, 1)
	go func() { result <- a.ResetConsole(context.Background()) }()
	assertNoCall(t, b.calls, "console_reset")
	b.unblock("console_read")
	waitCall(t, b.calls, "console_reset")
	if err := <-result; err != nil {
		t.Fatalf("ResetConsole() error = %v", err)
	}
}

func TestResetConsoleSuccessReenablesPolling(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.failMethod("console_read", "refused", "read unavailable")
	a := newConsoleActor(t, b)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	attachment, err := a.Attach(context.Background(), "console-reset")
	if err != nil {
		t.Fatal(err)
	}
	event := receiveEvent(t, attachment.Events)
	if event.Kind != EventOutput || !event.ConsoleUnavailable {
		t.Fatalf("console failure event = %#v", event)
	}
	b.clearFailure("console_read")
	b.setConsole("after reset\n", "", false)
	if err := a.ResetConsole(context.Background()); err != nil {
		t.Fatalf("ResetConsole() error = %v", err)
	}
	for {
		event = receiveEvent(t, attachment.Events)
		if event.Kind == EventOutput {
			break
		}
	}
	if event.Console.Server != "after reset\n" {
		t.Fatalf("console after reset = %#v", event)
	}
}

func TestResetConsoleFailureLeavesPollingDisabled(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.failMethod("console_read", "refused", "read unavailable")
	a := newConsoleActor(t, b)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	attachment, err := a.Attach(context.Background(), "console-reset")
	if err != nil {
		t.Fatal(err)
	}
	if event := receiveEvent(t, attachment.Events); event.Kind != EventOutput || !event.ConsoleUnavailable {
		t.Fatalf("console failure event = %#v", event)
	}
	b.failMethod("console_reset", "refused", "reset unavailable")
	if err := a.ResetConsole(context.Background()); err == nil {
		t.Fatal("ResetConsole() unexpectedly succeeded")
	}
	if _, err := a.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot() after reset failure = %v", err)
	}
	select {
	case fault := <-a.Faults():
		t.Fatalf("reset error faulted actor: %v", fault)
	default:
	}
	b.clearFailure("console_read")
	b.clearFailure("console_reset")
	b.setConsole("must remain disabled\n", "", false)
	assertNoCall(t, b.calls, "console_read")
	assertNoEvent(t, attachment.Events)
}

func TestResetConsoleRejectsClosedNotOpenAndDisabled(t *testing.T) {
	t.Run("not open", func(t *testing.T) {
		b := newTestBridge(t)
		a := newConsoleActor(t, b)
		if err := a.ResetConsole(context.Background()); !errors.Is(err, ErrNotOpen) {
			t.Fatalf("ResetConsole() error = %v, want not open", err)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		b := newTestBridge(t, stopped(1))
		a := newActor(t, b, nil)
		if err := a.Open(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := a.ResetConsole(context.Background()); !errors.Is(err, ErrConsoleUnavailable) {
			t.Fatalf("ResetConsole() error = %v, want console unavailable", err)
		}
	})
	t.Run("closed", func(t *testing.T) {
		b := newTestBridge(t)
		a := newConsoleActor(t, b)
		a.Close()
		if err := a.ResetConsole(context.Background()); !errors.Is(err, ErrClosed) {
			t.Fatalf("ResetConsole() error = %v, want closed", err)
		}
	})
}

func TestColdBootstrapFailureClosesOnlyAfterConfirmedOpen(t *testing.T) {
	b := newTestBridge(t)
	b.failMethod("cores", "refused", "cores unavailable")
	a := newSingleCoreActor(t, b, nil)

	err := a.Open(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cores unavailable") {
		t.Fatalf("Open() error = %v, want cores failure", err)
	}
	for _, method := range []string{"open", "cores", "close"} {
		waitCall(t, b.calls, method)
	}
	if _, err := a.Snapshot(context.Background()); !errors.Is(err, ErrReconciliationNeeded) {
		t.Fatalf("Snapshot after failed bootstrap = %v, want reconciliation required", err)
	}
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationCores || !errors.Is(fault, ErrReconciliationNeeded) {
		t.Fatalf("terminal fault = %#v, want cores reconciliation fault", fault)
	}
}

func TestColdInitialStateReconciliationFailureClosesBeforeFaulting(t *testing.T) {
	// A stopped sample without the mandatory stop stamp is a semantic state
	// failure, rather than an MBP transport error. It used to fault the actor
	// inside observe before the startup rollback could enter the queue.
	b := newTestBridge(t, map[string]any{"status": 2, "process_info": map[string]string{"pid": "1234"}})
	a := newActor(t, b, nil)
	err := a.Open(context.Background())
	if err == nil {
		t.Fatal("Open unexpectedly succeeded")
	}
	for _, method := range []string{"open", "cores", "state", "close"} {
		waitCall(t, b.calls, method)
	}
	if _, err := a.Snapshot(context.Background()); !errors.Is(err, ErrReconciliationNeeded) {
		t.Fatalf("Snapshot after failed bootstrap = %v, want reconciliation required", err)
	}
}

func TestColdBootstrapRollbackFailureKeepsBothCauses(t *testing.T) {
	b := newTestBridge(t)
	b.failMethod("cores", "refused", "cores unavailable")
	b.failMethod("close", "refused", "disconnect unavailable")
	a := newActor(t, b, nil)

	err := a.Open(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cores unavailable") || !strings.Contains(err.Error(), "disconnect unavailable") {
		t.Fatalf("Open() error = %v, want bootstrap and rollback causes", err)
	}
	var remote *bridge.RemoteError
	if !errors.As(err, &remote) || remote.Message != "cores unavailable" {
		t.Fatalf("Open() does not preserve bootstrap remote error: %v", err)
	}
	for _, method := range []string{"open", "cores", "close"} {
		waitCall(t, b.calls, method)
	}
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationRollbackClose || !errors.Is(fault, ErrReconciliationNeeded) {
		t.Fatalf("terminal fault = %#v, want rollback-close reconciliation fault", fault)
	}
}

func TestStaleGenerationDuringColdBootstrapDoesNotCloseReplacement(t *testing.T) {
	b := newTestBridge(t)
	b.failMethod("cores", "refused", "cores unavailable")
	b.blockMethod("cores")
	a := newActor(t, b, nil)
	result := make(chan error, 1)
	go func() { result <- a.Open(context.Background()) }()
	waitCall(t, b.calls, "open")
	waitCall(t, b.calls, "cores")
	// Replace advances the generation and invalidates the connection which
	// proved cold-session ownership. Rollback must not target this replacement.
	b.executor.Replace(nil)
	b.unblock("cores")
	select {
	case err := <-result:
		if !errors.Is(err, ErrBridgeStaleCompletion) {
			t.Fatalf("Open() error = %v, want stale completion", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Open hung after stale bootstrap completion")
	}
	assertNoCall(t, b.calls, "close")
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationCores || !errors.Is(fault, ErrBridgeStaleCompletion) {
		t.Fatalf("terminal fault = %#v, want cores stale completion", fault)
	}
}

func TestFaultExposesOperationWithoutCauseText(t *testing.T) {
	cause := errors.New(`command "route C:\\target\\secret.elf" failed`)
	fault := Fault{Operation: OperationInspection, cause: cause}
	if !errors.Is(fault, ErrReconciliationNeeded) || !errors.Is(fault, cause) {
		t.Fatalf("Fault does not preserve error chain: %v", fault)
	}
	if strings.Contains(fault.Error(), "secret.elf") || strings.Contains(fault.Error(), "route") {
		t.Fatalf("Fault exposed cause text: %q", fault.Error())
	}
	if got, want := fault.Error(), "actor: reconciliation is required during inspection"; got != want {
		t.Fatalf("Fault.Error() = %q, want %q", got, want)
	}
}

func TestOperationMappingIsTotal(t *testing.T) {
	tests := []struct {
		kind opKind
		want Operation
	}{
		{opOpen, OperationOpen},
		{opCores, OperationCores},
		{opState, OperationState},
		{opExecution, OperationExecution},
		{opBreakpoints, OperationBreakpoints},
		{opBreakpointCleanup, OperationBreakpointCleanup},
		{opInspection, OperationInspection},
		{opRollbackClose, OperationRollbackClose},
		{opConsole, OperationConsole},
		{opKind(255), OperationUnknown},
	}
	for _, test := range tests {
		if got := test.kind.operation(); got != test.want {
			t.Errorf("opKind(%d).operation() = %s, want %s", test.kind, got, test.want)
		}
	}
}

func TestInternalFaultMayUseUnknownOperation(t *testing.T) {
	faults := make(chan Fault, 1)
	r := &runtime{
		reducer:  session.New(session.Options{}),
		handles:  handle.NewStore(),
		faults:   faults,
		subs:     make(map[uint64]*subscriber),
		pending:  nil,
		inflight: nil,
	}
	cause := errors.New("internal invariant")
	r.failClosed(OperationUnknown, cause)
	fault := receiveFault(t, faults)
	if fault.Operation != OperationUnknown || !errors.Is(fault, ErrReconciliationNeeded) || !errors.Is(fault, cause) {
		t.Fatalf("internal terminal fault = %#v", fault)
	}
}

func TestColdBootstrapTransportPoisonReportsRollbackFailure(t *testing.T) {
	b := newTestBridge(t)
	b.blockMethod("cores")
	defer b.unblock("cores")
	a, err := New(Options{Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	result := make(chan error, 1)
	go func() { result <- a.Open(context.Background()) }()
	waitCall(t, b.calls, "open")
	waitCall(t, b.calls, "cores")
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "cold startup rollback close") {
			t.Fatalf("Open() error = %v, want rollback failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Open hung after poisoned bootstrap transport")
	}
	assertNoCall(t, b.calls, "close")
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationRollbackClose || !errors.Is(fault, bridge.ErrPoisoned) {
		t.Fatalf("terminal fault = %#v, want poisoned rollback-close fault", fault)
	}
}

func TestColdBootstrapRollbackDeadlinePreservesBothCauses(t *testing.T) {
	b := newTestBridge(t)
	b.failMethod("cores", "refused", "cores unavailable")
	b.blockMethod("close")
	defer b.unblock("close")
	a, err := New(Options{
		Executor:          b.executor,
		Open:              openRequest(),
		CoreSpecs:         testCoreSpecs(),
		PollInterval:      time.Hour,
		RPCDeadline:       20 * time.Millisecond,
		BootstrapDeadline: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	result := make(chan error, 1)
	go func() { result <- a.Open(context.Background()) }()
	for _, method := range []string{"open", "cores", "close"} {
		waitCall(t, b.calls, method)
	}
	err = <-result
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "cores unavailable") || !strings.Contains(err.Error(), "cold startup rollback close") {
		t.Fatalf("Open() error = %v, want original cores and rollback deadline causes", err)
	}
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationRollbackClose || !errors.Is(fault, context.DeadlineExceeded) {
		t.Fatalf("terminal fault = %#v, want rollback deadline fault", fault)
	}
	assertNoCall(t, b.calls, "close")
}

func TestOpenDeadlineFaultIsClassified(t *testing.T) {
	b := newTestBridge(t)
	b.blockMethod("open")
	defer b.unblock("open")
	a, err := New(Options{Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: 20 * time.Millisecond, BootstrapDeadline: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open() error = %v, want deadline", err)
	}
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationOpen || !errors.Is(fault, context.DeadlineExceeded) {
		t.Fatalf("terminal fault = %#v, want open deadline fault", fault)
	}
}

func TestBootstrapOpenUsesDedicatedDeadline(t *testing.T) {
	b := newTestBridge(t)
	b.blockMethod("open")
	defer b.unblock("open")
	a, err := New(Options{
		Executor:          b.executor,
		Open:              openRequest(),
		CoreSpecs:         testCoreSpecs(),
		PollInterval:      time.Hour,
		RPCDeadline:       20 * time.Millisecond,
		BootstrapDeadline: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	result := make(chan error, 1)
	go func() { result <- a.Open(context.Background()) }()
	waitCall(t, b.calls, "open")
	select {
	case err := <-result:
		t.Fatalf("Open returned before bootstrap budget elapsed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	b.unblock("open")
	if err := <-result; err != nil {
		t.Fatalf("Open() error = %v, want success after ordinary RPC deadline", err)
	}
}

func TestBootstrapCancellationStopsRemainingStartupWork(t *testing.T) {
	b := newTestBridge(t)
	b.blockMethod("cores")
	defer b.unblock("cores")
	a, err := New(Options{
		Executor:          b.executor,
		Open:              openRequest(),
		CoreSpecs:         testCoreSpecs(),
		PollInterval:      time.Hour,
		RPCDeadline:       time.Second,
		BootstrapDeadline: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- a.Open(ctx) }()
	waitCall(t, b.calls, "open")
	waitCall(t, b.calls, "cores")
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want caller cancellation", err)
	}
	b.unblock("cores")
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationRollbackClose || !errors.Is(fault, bridge.ErrPoisoned) {
		t.Fatalf("terminal fault = %#v, want poisoned rollback fault", fault)
	}
	assertNoCall(t, b.calls, "state")
	assertNoCall(t, b.calls, "close")
}

func newConfirmedOpenRuntime(b *testBridge, request multi.OpenRequest, rpcDeadline time.Duration) (*runtime, context.CancelFunc, <-chan Fault) {
	bootstrap, cancel := context.WithCancel(context.Background())
	faults := make(chan Fault, 1)
	return &runtime{
		opts:           Options{Executor: b.executor, Open: request, RPCDeadline: rpcDeadline},
		executor:       b.executor,
		reducer:        session.New(session.Options{}),
		handles:        handle.NewStore(),
		faults:         faults,
		complete:       make(chan completion, 2),
		subs:           make(map[uint64]*subscriber),
		opening:        true,
		openSucceeded:  true,
		openGeneration: b.executor.Generation(),
		bootstrap:      bootstrap,
		bootstrapStop:  cancel,
	}, cancel, faults
}

func drainRollbackCompletion(t *testing.T, r *runtime) {
	t.Helper()
	for range 2 {
		select {
		case completion := <-r.complete:
			r.completeWork(completion)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for rollback completion")
		}
	}
}

func TestCancelledBootstrapAfterConfirmedColdOpenClosesExactlyOnce(t *testing.T) {
	b := newTestBridge(t)
	r, cancel, faults := newConfirmedOpenRuntime(b, openRequest(), time.Second)
	cancel()
	reply := make(chan reply[struct{}], 1)
	r.bootstrapFailed(OperationCores, reply, context.Canceled)
	r.startNext()
	waitCall(t, b.calls, "close")
	drainRollbackCompletion(t, r)
	if result := <-reply; !errors.Is(result.err, context.Canceled) {
		t.Fatalf("rollback reply = %v, want original bootstrap cancellation", result.err)
	}
	fault := receiveFault(t, faults)
	if fault.Operation != OperationCores || !errors.Is(fault, context.Canceled) {
		t.Fatalf("terminal fault = %#v, want original cancelled cores failure", fault)
	}
	assertNoCall(t, b.calls, "close")
}

func TestCancelledBootstrapNeverClosesWarmSession(t *testing.T) {
	b := newTestBridge(t)
	r, cancel, faults := newConfirmedOpenRuntime(b, multi.OpenRequest{Mode: multi.OpenModeWarm, PrimaryELF: `C:\fixture\core0.elf`}, time.Second)
	cancel()
	reply := make(chan reply[struct{}], 1)
	r.bootstrapFailed(OperationCores, reply, context.Canceled)
	if result := <-reply; !errors.Is(result.err, context.Canceled) {
		t.Fatalf("warm rollback reply = %v, want cancellation", result.err)
	}
	fault := receiveFault(t, faults)
	if fault.Operation != OperationCores || !errors.Is(fault, context.Canceled) {
		t.Fatalf("warm terminal fault = %#v, want cancelled cores failure", fault)
	}
	assertNoBridgeCalls(t, b.calls)
}

func TestConfirmedOpenGenerationMismatchNeverClosesReplacement(t *testing.T) {
	b := newTestBridge(t)
	r, cancel, faults := newConfirmedOpenRuntime(b, openRequest(), time.Second)
	defer cancel()
	b.executor.Replace(nil)
	reply := make(chan reply[struct{}], 1)
	r.bootstrapFailed(OperationCores, reply, context.Canceled)
	if result := <-reply; !errors.Is(result.err, ErrBridgeStaleCompletion) {
		t.Fatalf("rollback reply = %v, want stale completion", result.err)
	}
	fault := receiveFault(t, faults)
	if fault.Operation != OperationCores || !errors.Is(fault, ErrBridgeStaleCompletion) {
		t.Fatalf("terminal fault = %#v, want stale cores failure", fault)
	}
	assertNoBridgeCalls(t, b.calls)
}

func TestWarmAndUnconfirmedOpenFailuresNeverClose(t *testing.T) {
	for _, test := range []struct {
		name string
		open multi.OpenRequest
		fail string
	}{
		{name: "warm bootstrap", open: multi.OpenRequest{Mode: multi.OpenModeWarm, PrimaryELF: `C:\\project\\primary.elf`}, fail: "cores"},
		{name: "cold open unconfirmed", open: openRequest(), fail: "open"},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := newTestBridge(t)
			b.failMethod(test.fail, "refused", test.fail+" unavailable")
			a, err := New(Options{Executor: b.executor, Open: test.open, CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Close)
			if err := a.Open(context.Background()); err == nil {
				t.Fatal("Open unexpectedly succeeded")
			}
			waitCall(t, b.calls, "open")
			if test.fail == "cores" {
				waitCall(t, b.calls, "cores")
			}
			assertNoCall(t, b.calls, "close")
		})
	}
}

func TestCommandDoesNotTransitionUntilPoll(t *testing.T) {
	b := newTestBridge(t, stopped(1), running(1))
	a := newSingleCoreActor(t, b, nil)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "frontend")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventStopped)
	if err := a.AcquireControl(ctx, "frontend"); err != nil {
		t.Fatal(err)
	}
	if err := a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := a.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != session.TargetStopped {
		t.Fatalf("command changed state: %+v", snapshot)
	}
	assertNoEvent(t, attachment.Events)
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventInvalidated)
	assertEvent(t, attachment.Events, EventResumed)
}

func TestSingleCoreExecuteResultCarriesScopeButNoAllThreadsTruth(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a := newSingleCoreActor(t, b, nil)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "frontend")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventStopped)
	if err := a.AcquireControl(ctx, "frontend"); err != nil {
		t.Fatal(err)
	}
	result, err := a.ExecuteResult(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Cores) != 1 || result.Cores[0] != 0 || result.Truth.AllThreadsContinued || result.Truth.AllThreadsStopped {
		t.Fatalf("execution result = %#v", result)
	}
	assertNoEvent(t, attachment.Events)
}

func TestMulticoreExecutionCommandsFailClosedBeforeBridgeIO(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a := newActorWithCoreSpecs(t, b, nil, []multi.CoreSpec{{ID: 0, ELF: `C:\fixture\core0.elf`}, {ID: 4, ELF: `C:\fixture\core1.elf`}})
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "frontend"); err != nil {
		t.Fatal(err)
	}
	drainCalls(b.calls)

	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "resume", call: func() error {
			return a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 4, Operation: multi.ExecutionContinue})
		}},
		{name: "halt", call: func() error {
			return a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionPause})
		}},
		{name: "step in", call: func() error {
			return a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 4, Operation: multi.ExecutionStepIn})
		}},
		{name: "next", call: func() error {
			return a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionNext})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, multi.ErrExecutionDomainUnavailable) {
				t.Fatalf("execution command error = %v", err)
			}
			assertNoBridgeCalls(t, b.calls)
		})
	}
	if err := a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 1, Operation: multi.ExecutionContinue}); err == nil || errors.Is(err, multi.ErrExecutionDomainUnavailable) {
		t.Fatalf("unconfigured core error = %v", err)
	}
	assertNoBridgeCalls(t, b.calls)
}

func TestStepCommandsRequireLeaseAndAwaitObservation(t *testing.T) {
	tests := []struct {
		name      string
		operation multi.ExecutionOperation
		method    string
	}{
		{name: "step in", operation: multi.ExecutionStepIn, method: "step_in"},
		{name: "next", operation: multi.ExecutionNext, method: "next"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := newTestBridge(t, stopped(1), running(1))
			a := newSingleCoreActor(t, b, nil)
			ctx := context.Background()
			attachment, err := a.Attach(ctx, "frontend")
			if err != nil {
				t.Fatal(err)
			}
			defer attachment.Close()
			if err := a.Open(ctx); err != nil {
				t.Fatal(err)
			}
			assertEvent(t, attachment.Events, EventStopped)
			if err := a.Execute(ctx, "other", ExecutionRequest{CoreID: 0, Operation: test.operation}); !errors.Is(err, session.ErrNotLeaseHolder) {
				t.Fatalf("step without lease = %v", err)
			}
			if err := a.AcquireControl(ctx, "frontend"); err != nil {
				t.Fatal(err)
			}
			if err := a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: test.operation}); err != nil {
				t.Fatal(err)
			}
			waitCall(t, b.calls, test.method)
			snapshot, err := a.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.State != session.TargetStopped {
				t.Fatalf("step confirmation changed state: %+v", snapshot)
			}
			assertNoEvent(t, attachment.Events)
			if err := a.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			assertEvent(t, attachment.Events, EventInvalidated)
			assertEvent(t, attachment.Events, EventResumed)
		})
	}
}

func TestStepCommandsSerializeBridgeTraffic(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a := newSingleCoreActor(t, b, nil)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "frontend"); err != nil {
		t.Fatal(err)
	}
	b.blockMethod("step_in")
	stepInDone := make(chan error, 1)
	nextDone := make(chan error, 1)
	go func() {
		stepInDone <- a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionStepIn})
	}()
	waitCall(t, b.calls, "step_in")
	go func() {
		nextDone <- a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionNext})
	}()
	select {
	case method := <-b.calls:
		t.Fatalf("concurrent command reached bridge before step completion: %q", method)
	case <-time.After(20 * time.Millisecond):
	}
	b.unblock("step_in")
	if err := <-stepInDone; err != nil {
		t.Fatal(err)
	}
	waitCall(t, b.calls, "next")
	if err := <-nextDone; err != nil {
		t.Fatal(err)
	}
}

func TestThreadsUseStableOneBasedCoreIDs(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a := newActor(t, b, nil)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	threads, err := a.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].ID != 1 || threads[1].ID != 2 {
		t.Fatalf("threads = %#v", threads)
	}
	event := receiveEvent(t, attachment.Events)
	if event.Kind != EventStopped || event.ThreadID != 0 {
		t.Fatalf("event = %#v", event)
	}
}

func TestThreadsUseConfiguredSparseCoreIDs(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a, err := New(Options{Executor: b.executor, Open: openRequest(), CoreSpecs: []multi.CoreSpec{{ID: 0, ELF: `C:\fixture\core0.elf`}, {ID: 4, ELF: `C:\fixture\core1.elf`}}, PollInterval: time.Hour, RPCDeadline: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	threads, err := a.Threads(context.Background())
	if err != nil || len(threads) != 2 || threads[0].ID != 1 || threads[0].CoreID != 0 || threads[1].ID != 5 || threads[1].CoreID != 4 {
		t.Fatalf("Threads() = (%#v, %v), want configured {0,4} -> {1,5}", threads, err)
	}
}

func TestProcessPIDIsNeverTreatedAsDAPThreadID(t *testing.T) {
	b := newTestBridge(t, stopped(1), running(1))
	a := newActor(t, b, nil)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	stoppedEvent := receiveEvent(t, attachment.Events)
	if stoppedEvent.Kind != EventStopped || stoppedEvent.ThreadID != 0 {
		t.Fatalf("stopped event = %#v, want no unproven core identity", stoppedEvent)
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventInvalidated)
	resumedEvent := receiveEvent(t, attachment.Events)
	if resumedEvent.Kind != EventResumed || resumedEvent.ThreadID != 1 {
		t.Fatalf("resumed event = %#v, want a valid freeze-group representative", resumedEvent)
	}
}

func TestMissedCycleIsPublishedInOrder(t *testing.T) {
	b := newTestBridge(t, stopped(1), stopped(2))
	a := newActor(t, b, nil)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventStopped)
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventInvalidated)
	assertEvent(t, attachment.Events, EventResumed)
	assertEvent(t, attachment.Events, EventStopped)
}

func TestHandleInvalidationPrecedesResumePublication(t *testing.T) {
	handles := handle.NewStore()
	b := newTestBridge(t, stopped(1), running(1))
	a := newSingleCoreActor(t, b, handles)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventStopped)
	id, err := a.AllocateHandle(ctx, handle.KindFrame, 0, "frame")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handles.Resolve(id, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	if err := a.Execute(ctx, "f", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue}); err != nil {
		t.Fatal(err)
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventInvalidated)
	if _, err := handles.Resolve(id, 1, true); !errors.Is(err, handle.ErrStale) {
		t.Fatalf("handle survived invalidation: %v", err)
	}
	assertEvent(t, attachment.Events, EventResumed)
}

func TestActorStaysResponsiveWhileBridgeBlocks(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.blockMethod("open")
	a := newActor(t, b, nil)
	openDone := make(chan error, 1)
	go func() { openDone <- a.Open(context.Background()) }()
	waitCall(t, b.calls, "open")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.Snapshot(ctx); err != nil {
		t.Fatalf("actor was blocked by rpc: %v", err)
	}
	b.unblock("open")
	if err := <-openDone; err != nil {
		t.Fatal(err)
	}
}

func TestCallerCancellationDoesNotCancelSubmittedWork(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a := newSingleCoreActor(t, b, nil)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	b.blockMethod("resume")
	cancelled, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := a.Execute(cancelled, "f", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	b.unblock("resume")
	waitCall(t, b.calls, "resume")
	if err := a.Poll(ctx); err != nil {
		t.Fatalf("actor did not converge after cancelled caller: %v", err)
	}
}

func TestLeaseAndOverflowFailClosed(t *testing.T) {
	b := newTestBridge(t, stopped(1), running(1))
	a, err := New(Options{Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "b"); !errors.Is(err, session.ErrLeaseHeld) {
		t.Fatalf("got %v", err)
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case event, ok := <-attachment.Events:
		if !ok || event.Kind != EventStopped {
			t.Fatal("subscription lost its already accepted event")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not retain its accepted event")
	}
	select {
	case _, ok := <-attachment.Events:
		if ok {
			t.Fatal("overflow did not detach subscription")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription was not closed")
	}
}

func TestBridgeGenerationFence(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a := newSingleCoreActor(t, b, nil)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	b.blockMethod("resume")
	result := make(chan error, 1)
	go func() {
		result <- a.Execute(context.Background(), "f", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue})
	}()
	waitCall(t, b.calls, "resume")
	b.executor.Replace(nil)
	if err := <-result; !errors.Is(err, ErrBridgeStaleCompletion) {
		t.Fatalf("got %v", err)
	}
	b.unblock("resume")
	if _, err := a.Snapshot(ctx); !errors.Is(err, ErrReconciliationNeeded) && err != nil {
		t.Fatalf("snapshot failed unexpectedly: %v", err)
	}
	fault := receiveFault(t, a.Faults())
	if fault.Operation != OperationExecution || !errors.Is(fault, ErrBridgeStaleCompletion) {
		t.Fatalf("terminal fault = %#v, want execution stale completion", fault)
	}
}

func TestStepCommandsHonorBridgeGenerationFence(t *testing.T) {
	tests := []struct {
		name      string
		operation multi.ExecutionOperation
		method    string
	}{
		{name: "step in", operation: multi.ExecutionStepIn, method: "step_in"},
		{name: "next", operation: multi.ExecutionNext, method: "next"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := newTestBridge(t, stopped(1))
			a := newSingleCoreActor(t, b, nil)
			ctx := context.Background()
			if err := a.Open(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.AcquireControl(ctx, "frontend"); err != nil {
				t.Fatal(err)
			}
			b.blockMethod(test.method)
			result := make(chan error, 1)
			go func() { result <- a.Execute(ctx, "frontend", ExecutionRequest{CoreID: 0, Operation: test.operation}) }()
			waitCall(t, b.calls, test.method)
			b.executor.Replace(nil)
			if err := <-result; !errors.Is(err, ErrBridgeStaleCompletion) {
				t.Fatalf("step error = %v, want stale completion", err)
			}
			b.unblock(test.method)
		})
	}
}

func TestTerminalFaultRejectsQueuedControlWork(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	b.setComponents("fixture.component.5 (debugger.name.core0)\n")
	a, err := New(Options{
		Executor:     b.executor,
		Open:         openRequest(),
		CoreSpecs:    testCoreSpecs()[:1],
		PollInterval: time.Hour,
		RPCDeadline:  40 * time.Millisecond,
		EventBuffer:  8,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}

	b.blockMethod("resume")
	resumeResult := make(chan error, 1)
	haltResult := make(chan error, 1)
	go func() {
		resumeResult <- a.Execute(ctx, "f", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionContinue})
	}()
	waitCall(t, b.calls, "resume")
	go func() {
		haltResult <- a.Execute(ctx, "f", ExecutionRequest{CoreID: 0, Operation: multi.ExecutionPause})
	}()

	if err := <-resumeResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resume error = %v, want deadline", err)
	}
	select {
	case fault := <-a.Faults():
		if fault.Operation != OperationExecution || !errors.Is(fault, ErrReconciliationNeeded) || !errors.Is(fault, context.DeadlineExceeded) {
			t.Fatalf("actor fault = %v", fault)
		}
	case <-time.After(time.Second):
		t.Fatal("actor did not publish its terminal fault")
	}
	if err := <-haltResult; !errors.Is(err, ErrReconciliationNeeded) {
		t.Fatalf("queued halt error = %v, want reconciliation", err)
	}
	select {
	case method := <-b.calls:
		if method == "halt" {
			t.Fatal("queued halt reached the bridge after terminal fault")
		}
	default:
	}
	b.unblock("resume")
}

func TestProtocolViolationFailsClosed(t *testing.T) {
	b := newTestBridge(t, map[string]any{"status": "stopped", "process_info": map[string]string{}})
	a := newActor(t, b, nil)
	if err := a.Open(context.Background()); !errors.Is(err, multi.ErrProtocol) {
		t.Fatalf("Open() error = %v, want typed protocol error", err)
	}
	if _, err := a.Snapshot(context.Background()); !errors.Is(err, ErrReconciliationNeeded) {
		t.Fatalf("Snapshot() error = %v, want terminal reconciliation", err)
	}
	select {
	case fault := <-a.Faults():
		if fault.Operation != OperationState || !errors.Is(fault, ErrReconciliationNeeded) || !errors.Is(fault, multi.ErrProtocol) {
			t.Fatalf("actor fault = %v", fault)
		}
	case <-time.After(time.Second):
		t.Fatal("actor did not publish protocol fault")
	}
}

type testSourceIndex map[string][]int

func (i testSourceIndex) Coverage(key string) []source.Observation {
	cores := i[key]
	observations := make([]source.Observation, 0, len(cores))
	for _, core := range cores {
		observations = append(observations, source.Observation{Core: core, Presence: source.PresencePresent})
	}
	return observations
}

type testSourceCoverage []source.Observation

func (i testSourceCoverage) Coverage(string) []source.Observation {
	return append([]source.Observation(nil), i...)
}

type testSourceResolver struct {
	observations []source.Observation
	err          error
	calls        int
}

func (r *testSourceResolver) Resolve(context.Context, *bridge.Client, source.Identity) ([]source.Observation, error) {
	r.calls++
	return append([]source.Observation(nil), r.observations...), r.err
}

type testBreakpointRunner struct{ requests []breakpoint.PlacementRequest }

func (r *testBreakpointRunner) Set(_ context.Context, _ *bridge.Client, request breakpoint.PlacementRequest) (breakpoint.Physical, error) {
	r.requests = append(r.requests, request)
	return breakpoint.Physical{ActualLine: request.Line, MULTIHandle: "bp"}, nil
}
func (*testBreakpointRunner) Clear(context.Context, *bridge.Client, breakpoint.Physical) error {
	return nil
}

func TestSetBreakpointsQueriesSourceResolverAfterIndexMiss(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	runner := &testBreakpointRunner{}
	resolver := &testSourceResolver{observations: []source.Observation{{Core: 0, Presence: source.PresencePresent}, {Core: 1, Presence: source.PresenceAbsent}}}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceCoverage{{Core: 0, Presence: source.PresenceUnknown}, {Core: 1, Presence: source.PresenceUnknown}}, SourceResolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	result, err := a.SetBreakpoints(ctx, "f", source.Identity{Key: "src/main.c"}, []int{18})
	if err != nil || len(result.Breakpoints) != 1 || !result.Breakpoints[0].Verified {
		t.Fatalf("SetBreakpoints() = (%#v, %v)", result, err)
	}
	if resolver.calls != 1 || len(runner.requests) != 1 || runner.requests[0].Core != 0 {
		t.Fatalf("resolver calls=%d, placement requests=%#v", resolver.calls, runner.requests)
	}
}

func TestSetBreakpointsFailsClosedWhenSourceResolutionIsUncertain(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	runner := &testBreakpointRunner{}
	resolver := &testSourceResolver{observations: []source.Observation{{Core: 0, Presence: source.PresencePresent}, {Core: 1, Presence: source.PresenceUnknown}}}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceCoverage{{Core: 0, Presence: source.PresencePresent}, {Core: 1, Presence: source.PresenceUnknown}}, SourceResolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	_, err = a.SetBreakpoints(ctx, "f", source.Identity{Key: "src/main.c"}, []int{18})
	if !errors.Is(err, ErrSourceUncertain) {
		t.Fatalf("SetBreakpoints() error = %v, want ErrSourceUncertain", err)
	}
	if resolver.calls != 1 || len(runner.requests) != 0 {
		t.Fatalf("resolver calls=%d, placement requests=%#v", resolver.calls, runner.requests)
	}
}

func TestSetBreakpointsTreatsMissingResolverAsUncertain(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: &testBreakpointRunner{}, SourceIndex: testSourceIndex{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetBreakpoints(ctx, "f", source.Identity{Key: "src/main.c"}, []int{18}); !errors.Is(err, ErrSourceUncertain) {
		t.Fatalf("SetBreakpoints() error = %v, want ErrSourceUncertain", err)
	}
}

func TestSetBreakpointsPlacesAcrossEveryPresentConfiguredCore(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	runner := &testBreakpointRunner{}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceCoverage{{Core: 0, Presence: source.PresencePresent}, {Core: 1, Presence: source.PresencePresent}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	result, err := a.SetBreakpoints(ctx, "f", source.Identity{Key: "src/main.c"}, []int{18})
	if err != nil || len(result.Breakpoints) != 1 || !result.Breakpoints[0].Verified {
		t.Fatalf("SetBreakpoints() = (%#v, %v)", result, err)
	}
	if len(runner.requests) != 2 || runner.requests[0].Core != 0 || runner.requests[1].Core != 1 {
		t.Fatalf("placement requests = %#v, want cores [0 1]", runner.requests)
	}
}

func TestSetBreakpointsReportsUnavailableOnlyForAuthoritativeAllAbsent(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	runner := &testBreakpointRunner{}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceCoverage{{Core: 0, Presence: source.PresenceAbsent}, {Core: 1, Presence: source.PresenceAbsent}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetBreakpoints(ctx, "f", source.Identity{Key: "src/main.c"}, []int{18}); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("SetBreakpoints() error = %v, want ErrSourceUnavailable", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("placement requests = %#v", runner.requests)
	}
}

func TestSetBreakpointsRejectsResolverCoverageMismatch(t *testing.T) {
	for _, observations := range [][]source.Observation{
		{{Core: 0, Presence: source.PresencePresent}},
		{{Core: 0, Presence: source.PresencePresent}, {Core: 2, Presence: source.PresenceAbsent}},
		{{Core: 0, Presence: source.PresencePresent}, {Core: 0, Presence: source.PresenceAbsent}},
	} {
		t.Run("mismatch", func(t *testing.T) {
			b := newTestBridge(t, stopped(1))
			runner := &testBreakpointRunner{}
			resolver := &testSourceResolver{observations: observations}
			a, err := New(Options{
				Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
				BreakpointRunner: runner, SourceIndex: testSourceCoverage{{Core: 0, Presence: source.PresenceUnknown}, {Core: 1, Presence: source.PresenceUnknown}}, SourceResolver: resolver,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Close)
			ctx := context.Background()
			if err := a.Open(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.AcquireControl(ctx, "f"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.SetBreakpoints(ctx, "f", source.Identity{Key: "src/main.c"}, []int{18}); !errors.Is(err, ErrSourceUncertain) {
				t.Fatalf("SetBreakpoints() error = %v, want ErrSourceUncertain", err)
			}
			if resolver.calls != 1 || len(runner.requests) != 0 {
				t.Fatalf("resolver calls=%d, placement requests=%#v", resolver.calls, runner.requests)
			}
		})
	}
}

type lifecycleBreakpointRunner struct {
	clearErr error
	cleared  chan breakpoint.Physical
}

func (r *lifecycleBreakpointRunner) Set(_ context.Context, _ *bridge.Client, request breakpoint.PlacementRequest) (breakpoint.Physical, error) {
	return breakpoint.Physical{ActualLine: request.Line, MULTIHandle: "bp"}, nil
}

func (r *lifecycleBreakpointRunner) Clear(_ context.Context, _ *bridge.Client, physical breakpoint.Physical) error {
	r.cleared <- physical
	return r.clearErr
}

type blockingBreakpointRunner struct {
	mu           sync.Mutex
	requests     []breakpoint.PlacementRequest
	setStarted   chan struct{}
	releaseSet   chan struct{}
	clearStarted chan struct{}
	releaseClear chan struct{}
}

func (r *blockingBreakpointRunner) Set(_ context.Context, _ *bridge.Client, request breakpoint.PlacementRequest) (breakpoint.Physical, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request)
	started, release := r.setStarted, r.releaseSet
	r.mu.Unlock()
	if started != nil {
		close(started)
		<-release
	}
	return breakpoint.Physical{ActualLine: request.Line, MULTIHandle: "bp"}, nil
}

func (r *blockingBreakpointRunner) Clear(_ context.Context, _ *bridge.Client, physical breakpoint.Physical) error {
	r.mu.Lock()
	started, release := r.clearStarted, r.releaseClear
	r.mu.Unlock()
	if started != nil {
		close(started)
		<-release
	}
	return nil
}

func (r *blockingBreakpointRunner) request(index int) breakpoint.PlacementRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[index]
}

func TestBreakpointStateReadsAndDetachRemainResponsiveDuringBlockedSet(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	runner := &blockingBreakpointRunner{}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceIndex{"src/main.c": {0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetBreakpoints(ctx, "frontend-1", source.Identity{Key: "src/main.c"}, []int{18}); err != nil {
		t.Fatal(err)
	}
	oldToken := runner.request(0).HintToken
	runner.setStarted = make(chan struct{})
	runner.releaseSet = make(chan struct{})
	setDone := make(chan error, 1)
	go func() {
		_, err := a.SetBreakpoints(context.Background(), "frontend-1", source.Identity{Key: "src/main.c"}, []int{30})
		setDone <- err
	}()
	<-runner.setStarted

	fast, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.Snapshot(fast); err != nil {
		t.Fatalf("Snapshot() during blocked Set: %v", err)
	}
	if counts, err := a.BreakpointCounts(fast); err != nil || counts.DAPOwned != 1 {
		t.Fatalf("BreakpointCounts() during blocked Set = %#v, %v", counts, err)
	}
	if err := a.NoteHint(fast, oldToken); err != nil {
		t.Fatalf("NoteHint() during blocked Set: %v", err)
	}
	if err := a.Detach(fast, "frontend-1"); err != nil {
		t.Fatalf("Detach() during blocked Set: %v", err)
	}
	close(runner.releaseSet)
	if err := <-setDone; err != nil {
		t.Fatalf("SetBreakpoints() = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		counts, err := a.BreakpointCounts(ctx)
		if err == nil && counts.DAPOwned == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("post-inflight-detach counts = %#v, %v", counts, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBreakpointStateReadsRemainResponsiveDuringBlockedCleanup(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	runner := &blockingBreakpointRunner{clearStarted: make(chan struct{}), releaseClear: make(chan struct{})}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceIndex{"src/main.c": {0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetBreakpoints(ctx, "frontend-1", source.Identity{Key: "src/main.c"}, []int{18}); err != nil {
		t.Fatal(err)
	}
	token := runner.request(0).HintToken
	if err := a.Detach(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	<-runner.clearStarted
	fast, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.Snapshot(fast); err != nil {
		t.Fatalf("Snapshot() during blocked Clear: %v", err)
	}
	if counts, err := a.BreakpointCounts(fast); err != nil || counts.Pending != 1 {
		t.Fatalf("BreakpointCounts() during blocked Clear = %#v, %v", counts, err)
	}
	if err := a.NoteHint(fast, token); err != nil {
		t.Fatalf("NoteHint() during blocked Clear: %v", err)
	}
	if err := a.Detach(fast, "frontend-1"); err != nil {
		t.Fatalf("repeat Detach() during blocked Clear: %v", err)
	}
	close(runner.releaseClear)
}

func TestCleanupRetriesAtMostOnceNaturallyPerStopEpoch(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	runner := &lifecycleBreakpointRunner{clearErr: errors.New("clear refused"), cleared: make(chan breakpoint.Physical, 8)}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceIndex{"src/main.c": {0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetBreakpoints(ctx, "frontend-1", source.Identity{Key: "src/main.c"}, []int{18}); err != nil {
		t.Fatal(err)
	}
	if err := a.Detach(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.cleared: // immediate stopped-detach attempt
	case <-time.After(time.Second):
		t.Fatal("immediate stopped-detach cleanup did not run")
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.cleared: // one natural retry for this StopEpoch
	case <-time.After(time.Second):
		t.Fatal("natural cleanup retry did not run")
	}
	for range 3 {
		if err := a.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case physical := <-runner.cleared:
		t.Fatalf("unexpected repeated cleanup in one StopEpoch: %#v", physical)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDetachDefersBreakpointCleanupUntilNaturalStop(t *testing.T) {
	b := newTestBridge(t, stopped(1), running(1), stopped(2))
	runner := &lifecycleBreakpointRunner{cleared: make(chan breakpoint.Physical, 2)}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceIndex{"src/main.c": {0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	placed, err := a.SetBreakpoints(ctx, "frontend-1", source.Identity{Key: "src/main.c"}, []int{18})
	if err != nil || len(placed.Breakpoints) != 1 || !placed.Breakpoints[0].Verified {
		t.Fatalf("SetBreakpoints() = (%#v, %v)", placed, err)
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Detach(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	counts, err := a.BreakpointCounts(ctx)
	if err != nil || counts.DAPOwned != 1 || counts.Pending != 1 || counts.Orphaned != 0 {
		t.Fatalf("running-detach counts = %#v, %v", counts, err)
	}
	select {
	case cleared := <-runner.cleared:
		t.Fatalf("cleanup halted/raced while target running: %#v", cleared)
	default:
	}

	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case cleared := <-runner.cleared:
		if cleared.Owner != "frontend-1" || !cleared.Pending {
			t.Fatalf("cleanup physical = %#v", cleared)
		}
	case <-time.After(time.Second):
		t.Fatal("detached breakpoint was not cleared at natural stop")
	}
	deadline := time.Now().Add(time.Second)
	for {
		counts, err = a.BreakpointCounts(ctx)
		if err == nil && counts.DAPOwned == 0 && counts.Pending == 0 && counts.Orphaned == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("post-cleanup counts = %#v, %v", counts, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDetachCleanupFailureIsRetainedAsOrphan(t *testing.T) {
	b := newTestBridge(t, stopped(1))
	runner := &lifecycleBreakpointRunner{clearErr: errors.New("clear refused"), cleared: make(chan breakpoint.Physical, 2)}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceIndex{"src/main.c": {0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireControl(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetBreakpoints(ctx, "frontend-1", source.Identity{Key: "src/main.c"}, []int{18}); err != nil {
		t.Fatal(err)
	}
	if err := a.Detach(ctx, "frontend-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.cleared:
	case <-time.After(time.Second):
		t.Fatal("stopped detach did not attempt cleanup")
	}
	deadline := time.Now().Add(time.Second)
	for {
		counts, countErr := a.BreakpointCounts(ctx)
		if countErr == nil && counts.DAPOwned == 1 && counts.Pending == 0 && counts.Orphaned == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed-cleanup counts = %#v, %v", counts, countErr)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestResolvedCoresDistinguishesAbsentFromUncertain(t *testing.T) {
	cores, err := resolvedCores([]source.Observation{{Core: 1, Presence: source.PresencePresent}, {Core: 2, Presence: source.PresenceAbsent}})
	if err != nil || len(cores) != 1 || cores[0] != 1 {
		t.Fatalf("resolvedCores(present, absent) = (%v, %v), want ([1], nil)", cores, err)
	}
	if _, err := resolvedCores([]source.Observation{{Core: 1, Presence: source.PresenceAbsent}}); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("resolvedCores(all absent) error = %v, want ErrSourceUnavailable", err)
	}
	if _, err := resolvedCores([]source.Observation{{Core: 1, Presence: source.PresenceUnknown}}); !errors.Is(err, ErrSourceUncertain) {
		t.Fatalf("resolvedCores(unknown) error = %v, want ErrSourceUncertain", err)
	}
	if _, err := resolvedCores([]source.Observation{{Core: 1, Presence: source.PresencePresent}, {Core: 1, Presence: source.PresencePresent}}); !errors.Is(err, ErrSourceUncertain) {
		t.Fatalf("resolvedCores(duplicate) error = %v, want ErrSourceUncertain", err)
	}
}

func TestSetBreakpointsResolvesSourcesAndHintsOnlyTriggerStateSample(t *testing.T) {
	b := newTestBridge(t, stopped(1), running(1), stopped(2))
	runner := &testBreakpointRunner{}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceIndex{"src/main.c": {0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventStopped)
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	result, err := a.SetBreakpoints(ctx, "f", source.Identity{Key: "src/main.c"}, []int{18})
	if err != nil || len(result.Breakpoints) != 1 || !result.Breakpoints[0].Verified || len(runner.requests) != 1 {
		t.Fatalf("SetBreakpoints() = (%#v, %v), requests=%#v", result, err, runner.requests)
	}
	if runner.requests[0].Core != 0 || runner.requests[0].HintToken == 0 {
		t.Fatalf("placement request = %#v", runner.requests[0])
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventInvalidated)
	assertEvent(t, attachment.Events, EventResumed)
	if err := a.NoteHint(ctx, runner.requests[0].HintToken); err != nil {
		t.Fatal(err)
	}
	stopped := receiveEvent(t, attachment.Events)
	if stopped.Kind != EventStopped || stopped.Reason != "unknown" || stopped.ThreadID != 1 || len(stopped.HitBreakpointIDs) != 1 || stopped.HitBreakpointIDs[0] != result.Breakpoints[0].DAPID {
		t.Fatalf("hint-confirmed stopped event = %#v", stopped)
	}
}

func TestBreakpointCommandTokenMakesPollingSelfSufficient(t *testing.T) {
	b := newTestBridge(t, stopped(1), running(1), stopped(2))
	runner := &testBreakpointRunner{}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		BreakpointRunner: runner, SourceIndex: testSourceIndex{"src/main.c": {0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	attachment, err := a.Attach(ctx, "f")
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	if err := a.Open(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventStopped)
	if err := a.AcquireControl(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	result, err := a.SetBreakpoints(ctx, "f", source.Identity{Key: "src/main.c"}, []int{18})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, attachment.Events, EventInvalidated)
	assertEvent(t, attachment.Events, EventResumed)
	b.setCommandRaw(fmt.Sprintf("Halted for breakpoint.\nCommand list was: {mprintf(\"HIT 0x%08X\\n\")}\n", runner.requests[0].HintToken))
	if err := a.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := receiveEvent(t, attachment.Events)
	if stopped.Kind != EventStopped || stopped.Reason != "breakpoint" || len(stopped.HitBreakpointIDs) != 1 || stopped.HitBreakpointIDs[0] != result.Breakpoints[0].DAPID {
		t.Fatalf("H-confirmed stopped event = %#v", stopped)
	}
}

type testSourceMapper struct{}

func (testSourceMapper) MapDebugPath(path string) (source.Identity, error) {
	return source.Identity{ClientPath: "C:/workspace/" + path, DebugPath: path, Key: source.Canonicalize(path)}, nil
}

type testInspectionRunner struct {
	stackCalls      int
	stackCores      []inspection.CoreID
	scopeCores      []inspection.CoreID
	variableCores   []inspection.CoreID
	evaluationCores []inspection.CoreID
}

func (r *testInspectionRunner) Stack(_ context.Context, _ *bridge.Client, core inspection.CoreID) ([]inspection.RawFrame, error) {
	r.stackCalls++
	r.stackCores = append(r.stackCores, core)
	return []inspection.RawFrame{{Index: 3, Name: "main", DebugPath: "src/main.c", Line: 7}}, nil
}
func (r *testInspectionRunner) Scopes(_ context.Context, _ *bridge.Client, core inspection.CoreID, _ uint64) ([]inspection.RawScope, error) {
	r.scopeCores = append(r.scopeCores, core)
	return []inspection.RawScope{{Name: "Locals", Kind: inspection.ScopeLocals, Locator: "locals"}}, nil
}
func (r *testInspectionRunner) Variables(_ context.Context, _ *bridge.Client, core inspection.CoreID, _ inspection.ScopeLocator, _ inspection.Page, _ inspection.Format) (inspection.RawValuePage, error) {
	r.variableCores = append(r.variableCores, core)
	return inspection.RawValuePage{Total: 1, Values: []inspection.RawValue{{Name: "count", Value: "7", Type: "int", Access: inspection.Access{Kind: inspection.AccessRoot}}}}, nil
}
func (*testInspectionRunner) Children(context.Context, *bridge.Client, inspection.CoreID, inspection.ValueLocator, inspection.Page, inspection.Format) (inspection.RawValuePage, error) {
	return inspection.RawValuePage{Total: 0}, nil
}
func (r *testInspectionRunner) Evaluate(_ context.Context, _ *bridge.Client, core inspection.CoreID, _ uint64, expression inspection.Expression, _ inspection.Format) (inspection.RawValue, error) {
	r.evaluationCores = append(r.evaluationCores, core)
	return inspection.RawValue{Name: expression.Text(), Value: "8", Type: "int", Access: inspection.Access{Kind: inspection.AccessRoot}}, nil
}

func TestInspectionUsesCurrentStopBoundHandles(t *testing.T) {
	b := newTestBridge(t, stopped(11))
	runner := &testInspectionRunner{}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		Inspection: runner, SourceMap: testSourceMapper{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	stack, err := a.Stack(context.Background(), 1, inspection.Page{Count: 4})
	if err != nil || len(stack.Frames) != 1 || stack.Frames[0].ID == 0 || runner.stackCalls != 1 {
		t.Fatalf("Stack() = (%#v, %v), calls=%d", stack, err, runner.stackCalls)
	}
	scopes, err := a.Scopes(context.Background(), stack.Frames[0].ID)
	if err != nil || len(scopes) != 1 || scopes[0].VariablesReference == 0 {
		t.Fatalf("Scopes() = (%#v, %v)", scopes, err)
	}
	values, err := a.Variables(context.Background(), scopes[0].VariablesReference, inspection.Page{Count: 4}, inspection.FormatDefault)
	if err != nil || len(values.Variables) != 1 || values.Variables[0].Name != "count" {
		t.Fatalf("Variables() = (%#v, %v)", values, err)
	}
	expression, err := inspection.NewExpression("count + 1")
	if err != nil {
		t.Fatal(err)
	}
	evaluated, err := a.Evaluate(context.Background(), stack.Frames[0].ID, expression, inspection.FormatDefault)
	if err != nil || evaluated.Value != "8" {
		t.Fatalf("Evaluate() = (%#v, %v)", evaluated, err)
	}
}

func TestInspectionQueuedBehindRunningStateDoesNotReachRunner(t *testing.T) {
	b := newTestBridge(t, stopped(11), running(11))
	runner := &testInspectionRunner{}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		Inspection: runner, SourceMap: testSourceMapper{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainCalls(b.calls)
	b.blockMethod("state")
	poll := make(chan error, 1)
	go func() { poll <- a.Poll(context.Background()) }()
	waitCall(t, b.calls, "state")

	stackReply := make(chan reply[inspection.Stack], 1)
	if err := a.send(context.Background(), stackCommand{threadID: 1, page: inspection.Page{Count: 4}, reply: stackReply}); err != nil {
		t.Fatal(err)
	}
	// This reply proves the preceding stack command was accepted against the
	// old stopped snapshot while the state RPC was still inflight.
	snapshotReply := make(chan reply[session.Snapshot], 1)
	if err := a.send(context.Background(), snapshotCommand{reply: snapshotReply}); err != nil {
		t.Fatal(err)
	}
	if got := <-snapshotReply; got.err != nil || got.value.State != session.TargetStopped {
		t.Fatalf("queued inspection snapshot = %#v", got)
	}
	b.unblock("state")
	if err := <-poll; err != nil {
		t.Fatal(err)
	}
	if got := <-stackReply; !errors.Is(got.err, ErrInspectionStale) {
		t.Fatalf("queued Stack error = %v, want stale", got.err)
	}
	if runner.stackCalls != 0 {
		t.Fatalf("stale stack reached runner %d times", runner.stackCalls)
	}
}

func TestInspectionKeepsSparseCoreIdentityAcrossThreadAndHandleRequests(t *testing.T) {
	b := newTestBridge(t, stopped(11))
	runner := &testInspectionRunner{}
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(),
		CoreSpecs:    []multi.CoreSpec{{ID: 0, ELF: `C:\fixture\core0.elf`}, {ID: 4, ELF: `C:\fixture\core1.elf`}},
		PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		Inspection: runner, SourceMap: testSourceMapper{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}

	first, err := a.Stack(context.Background(), 1, inspection.Page{Count: 4})
	if err != nil || len(first.Frames) != 1 {
		t.Fatalf("Stack(core0 thread) = (%#v, %v)", first, err)
	}
	second, err := a.Stack(context.Background(), 5, inspection.Page{Count: 4})
	if err != nil || len(second.Frames) != 1 {
		t.Fatalf("Stack(core4 thread) = (%#v, %v)", second, err)
	}
	if first.Frames[0].ID == second.Frames[0].ID {
		t.Fatalf("different cores received the same frame handle: first=%#v second=%#v", first, second)
	}

	expression, err := inspection.NewExpression("count + 1")
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []inspection.Frame{first.Frames[0], second.Frames[0]} {
		scopes, err := a.Scopes(context.Background(), frame.ID)
		if err != nil || len(scopes) != 1 || scopes[0].VariablesReference == 0 {
			t.Fatalf("Scopes(frame=%d) = (%#v, %v)", frame.ID, scopes, err)
		}
		values, err := a.Variables(context.Background(), scopes[0].VariablesReference, inspection.Page{Count: 4}, inspection.FormatDefault)
		if err != nil || len(values.Variables) != 1 || values.Variables[0].Name != "count" {
			t.Fatalf("Variables(frame=%d) = (%#v, %v)", frame.ID, values, err)
		}
		if _, err := a.Evaluate(context.Background(), frame.ID, expression, inspection.FormatDefault); err != nil {
			t.Fatalf("Evaluate(frame=%d) = %v", frame.ID, err)
		}
	}

	for label, got := range map[string][]inspection.CoreID{
		"stack":     runner.stackCores,
		"scopes":    runner.scopeCores,
		"variables": runner.variableCores,
		"evaluate":  runner.evaluationCores,
	} {
		if len(got) != 2 || got[0] != 0 || got[1] != 4 {
			t.Fatalf("%s routed cores = %#v, want [0 4]", label, got)
		}
	}
}

func TestInspectionFormatFailureIsRequestScoped(t *testing.T) {
	b := newTestBridge(t, stopped(11))
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		Inspection: multi.NewInspectionRunner(), SourceMap: testSourceMapper{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Stack(context.Background(), 1, inspection.Page{Count: 4}); !errors.Is(err, multi.ErrInspectionFormat) {
		t.Fatalf("Stack() error = %v, want inspection format failure", err)
	}
	if _, err := a.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot after failed inspection = %v, want live actor", err)
	}
	select {
	case fault := <-a.Faults():
		t.Fatalf("inspection protocol failure faulted actor: %v", fault)
	default:
	}

	b.setCommandRaw("0_ main\t[C:/fixture/src/main.c:7,0]\n")
	b.setCommandRawFor("route fixture.component.5 print /x $pc", "$pc = 0x00000000\n")
	stack, err := a.Stack(context.Background(), 1, inspection.Page{Count: 4})
	if err != nil || len(stack.Frames) != 1 {
		t.Fatalf("Stack retry = (%#v, %v)", stack, err)
	}
}

func TestInspectionRouteVerificationProtocolFailureFaultsActor(t *testing.T) {
	b := newTestBridge(t, stopped(11))
	a, err := New(Options{
		Executor: b.executor, Open: openRequest(), CoreSpecs: testCoreSpecs(), PollInterval: time.Hour, RPCDeadline: time.Second, EventBuffer: 8,
		Inspection: multi.NewInspectionRunner(), SourceMap: testSourceMapper{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.Open(context.Background()); err != nil {
		t.Fatal(err)
	}

	b.setCommandRawFor("route fixture.component.5 P", "malformed routed process table\n")
	if _, err := a.Stack(context.Background(), 1, inspection.Page{Count: 4}); !errors.Is(err, multi.ErrProtocol) {
		t.Fatalf("Stack() error = %v, want route protocol failure", err)
	}
	select {
	case fault := <-a.Faults():
		if fault.Operation != OperationInspection || !errors.Is(fault, ErrReconciliationNeeded) || !errors.Is(fault, multi.ErrProtocol) {
			t.Fatalf("actor fault = %v, want reconciliation protocol fault", fault)
		}
	case <-time.After(time.Second):
		t.Fatal("route verification protocol failure did not fault actor")
	}
	if _, err := a.Snapshot(context.Background()); !errors.Is(err, ErrReconciliationNeeded) {
		t.Fatalf("Snapshot after route verification failure = %v, want reconciliation required", err)
	}
}

func receiveFault(t *testing.T, faults <-chan Fault) Fault {
	t.Helper()
	select {
	case fault := <-faults:
		return fault
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal fault")
		return Fault{}
	}
}

func assertEvent(t *testing.T, events <-chan Event, kind EventKind) {
	t.Helper()
	event := receiveEvent(t, events)
	if event.Kind != kind {
		t.Fatalf("event kind = %v, want %v", event.Kind, kind)
	}
}

func receiveEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event stream closed")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}
func assertNoEvent(t *testing.T, events <-chan Event) {
	t.Helper()
	select {
	case e := <-events:
		t.Fatalf("unexpected event %#v", e)
	case <-time.After(20 * time.Millisecond):
	}
}
func assertNoCall(t *testing.T, calls <-chan string, unwanted string) {
	t.Helper()
	select {
	case got := <-calls:
		if got == unwanted {
			t.Fatalf("unexpected bridge call %q", unwanted)
		}
	case <-time.After(20 * time.Millisecond):
	}
}

func drainCalls(calls <-chan string) {
	for {
		select {
		case <-calls:
		default:
			return
		}
	}
}

func assertNoBridgeCalls(t *testing.T, calls <-chan string) {
	t.Helper()
	select {
	case method := <-calls:
		t.Fatalf("unexpected bridge call %q", method)
	case <-time.After(20 * time.Millisecond):
	}
}
func waitCall(t *testing.T, calls <-chan string, want string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case got := <-calls:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("did not receive call %q", want)
		}
	}
}
