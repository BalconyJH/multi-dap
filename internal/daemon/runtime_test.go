package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/mbp"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

type runtimeFakeBridge struct {
	client *bridge.Client
	done   chan struct{}

	once     sync.Once
	mu       sync.Mutex
	err      error
	results  map[string]json.RawMessage
	commands map[string]json.RawMessage
	block    map[string]chan struct{}
	requests []mbp.Request
}

func newRuntimeFakeBridge(t *testing.T) *runtimeFakeBridge {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	fake := &runtimeFakeBridge{done: make(chan struct{}), results: make(map[string]json.RawMessage), commands: make(map[string]json.RawMessage), block: make(map[string]chan struct{})}
	go fake.serve(serverConn)
	client, err := bridge.NewClient(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	fake.client = client
	t.Cleanup(func() { _ = fake.Close() })
	return fake
}

func (f *runtimeFakeBridge) Client() *bridge.Client { return f.client }
func (f *runtimeFakeBridge) Done() <-chan struct{}  { return f.done }
func (f *runtimeFakeBridge) Wait() error {
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}
func (f *runtimeFakeBridge) Close() error {
	f.finish(nil)
	return nil
}
func (f *runtimeFakeBridge) crash(err error) { f.finish(err) }
func (f *runtimeFakeBridge) setResult(method string, value any) {
	encoded, _ := json.Marshal(value)
	f.mu.Lock()
	f.results[method] = encoded
	f.mu.Unlock()
}

func (f *runtimeFakeBridge) setCommandResult(command string, value any) {
	encoded, _ := json.Marshal(value)
	f.mu.Lock()
	f.commands[command] = encoded
	f.mu.Unlock()
}

func (f *runtimeFakeBridge) blockMethod(method string) {
	f.mu.Lock()
	f.block[method] = make(chan struct{})
	f.mu.Unlock()
}

func (f *runtimeFakeBridge) unblockMethod(method string) {
	f.mu.Lock()
	gate := f.block[method]
	delete(f.block, method)
	f.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (f *runtimeFakeBridge) result(request mbp.Request) (json.RawMessage, bool) {
	method := request.Method
	f.mu.Lock()
	result, overridden := f.results[method]
	if method == "run_"+"commands" {
		var params map[string]string
		_ = json.Unmarshal(request.Params, &params)
		result, overridden = f.commands[params["commands"]]
	}
	f.mu.Unlock()
	if overridden {
		return result, true
	}
	return runtimeBridgeResult(request)
}

func (f *runtimeFakeBridge) finish(err error) {
	f.once.Do(func() {
		f.mu.Lock()
		f.err = err
		f.mu.Unlock()
		if f.client != nil {
			_ = f.client.Close()
		}
		close(f.done)
	})
}

func (f *runtimeFakeBridge) serve(connection net.Conn) {
	defer connection.Close()
	_, _ = connection.Write([]byte(`{"protocol_version":1,"bridge_version":"runtime-test","max_message_size":1048576,"encoding":"utf-8"}` + "\n"))
	decoder := json.NewDecoder(bufio.NewReader(connection))
	encoder := json.NewEncoder(connection)
	for {
		var request mbp.Request
		if err := decoder.Decode(&request); err != nil {
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, request)
		gate := f.block[request.Method]
		f.mu.Unlock()
		if gate != nil {
			<-gate
		}
		result, ok := f.result(request)
		if !ok {
			_ = encoder.Encode(mbp.Response{ID: request.ID, OK: false, Error: &mbp.Error{Kind: "unsupported", Message: request.Method, Raw: ""}})
			continue
		}
		_ = encoder.Encode(mbp.Response{ID: request.ID, OK: true, Result: result})
	}
}

func (f *runtimeFakeBridge) requestParams(method string) (map[string]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		if request.Method != method {
			continue
		}
		var params map[string]string
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, false
		}
		return params, true
	}
	return nil, false
}

func waitRuntimeRequest(t *testing.T, fake *runtimeFakeBridge, method string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := fake.requestParams(method); ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for bridge request %q", method)
}

func runtimeBridgeResult(request mbp.Request) (json.RawMessage, bool) {
	method := request.Method
	var value any
	switch method {
	case "open":
		value = map[string]bool{"opened": true}
	case "cores":
		value = map[string]any{"components": "fixture.component.5 (debugger.name.core0)\nfixture.component.6 (debugger.name.core1)\n", "status": 1}
	case "run_" + "commands":
		var params map[string]string
		_ = json.Unmarshal(request.Params, &params)
		if params["commands"] == "H" {
			value = map[string]any{"status": 1, "raw": "Process not running.\n"}
			break
		}
		program := `C:\fixture\core0.elf`
		if params["commands"] == "route fixture.component.6 P" {
			program = `C:\fixture\core1.elf`
		}
		value = map[string]any{"status": 1, "raw": "     # PID        PPID       Status        CBEFITDHR Name and Arguments\n>>   0 0x1 N/A        Stopped       011111101 " + program + "\n"}
	case "state":
		value = map[string]any{"status": 2, "process_info": map[string]string{"stopStamp": "1", "pid": "1"}}
	case "resume", "halt":
		value = map[string]bool{"accepted": true}
	default:
		return nil, false
	}
	encoded, _ := json.Marshal(value)
	return encoded, true
}

func testRuntimeConfig() config.Validated {
	return config.Validated{
		Multi:      config.ValidatedMulti{Executable: "fake-mpythonrun.exe"},
		Connection: config.ValidatedConnection{Project: "project.gpj", Arguments: "-target fake"},
		Probe:      config.ProbeIdentity{ID: "daemon-runtime-test"},
		Endpoints: config.ValidatedEndpoints{
			MBP: config.Endpoint{Host: "127.0.0.1", Port: 0},
			DAP: config.Endpoint{Host: "127.0.0.1", Port: 0},
		},
		Timing: config.ValidatedTiming{
			PollCadence: time.Hour, RPCDeadline: time.Second, StartupDeadline: time.Second,
		},
		Cores: []config.ValidatedCore{{ID: 0, ELF: `C:\fixture\core0.elf`}, {ID: 1, ELF: `C:\fixture\core1.elf`}},
	}
}

func testBridgeScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(path, []byte("# bridge test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func startRuntimeFake(t *testing.T, fake *runtimeFakeBridge, cfg config.Validated) *Runtime {
	t.Helper()
	runtime, err := startWith(context.Background(), Options{Config: cfg, BridgeScript: testBridgeScript(t)}, func(_ context.Context, spec bridge.LaunchSpec) (bridgeProcess, error) {
		if spec.Executable != cfg.Multi.Executable || spec.RPCHost != cfg.Endpoints.MBP.Host || spec.RPCPort != cfg.Endpoints.MBP.Port || spec.SessionMode != bridge.SessionModeCold || spec.ServiceRouter != nil {
			t.Fatalf("bridge spec = %#v", spec)
		}
		return fake, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime
}

func TestRuntimeWarmSessionUsesOneAtomicLaunchAndOpenIdentity(t *testing.T) {
	fake := newRuntimeFakeBridge(t)
	cfg := testRuntimeConfig()
	cfg.Probe.ID = "daemon-runtime-warm"
	primaryELF := filepath.Join(t.TempDir(), "primary.elf")
	if err := os.WriteFile(primaryELF, []byte("not-dwarf"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Cores = []config.ValidatedCore{{ID: 0, ELF: primaryELF}}
	fake.setResult("cores", map[string]any{"components": "fixture.component.5 (debugger.name.core0)\n", "status": 1})
	fake.setCommandResult("route fixture.component.5 P", map[string]any{"status": 1, "raw": "     # PID        PPID       Status        CBEFITDHR Name and Arguments\n>>   0 0x1 N/A        Stopped       011111101 " + primaryELF + "\n"})
	options := Options{
		Config: cfg, BridgeScript: testBridgeScript(t),
		Warm: &WarmSession{ServiceRouterHost: "127.0.0.1", ServiceRouterPort: 40123, PrimaryELF: primaryELF},
	}
	var launched bridge.LaunchSpec
	runtime, err := startWith(context.Background(), options, func(_ context.Context, spec bridge.LaunchSpec) (bridgeProcess, error) {
		launched = spec
		return fake, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	if launched.SessionMode != bridge.SessionModeWarm || launched.ServiceRouter == nil || launched.ServiceRouter.Host != "127.0.0.1" || launched.ServiceRouter.Port != 40123 {
		t.Fatalf("warm launch = %#v", launched)
	}
	params, ok := fake.requestParams("open")
	if !ok {
		t.Fatal("warm open request was not observed")
	}
	if params["mode"] != string(multi.OpenModeWarm) || params["primary_elf"] != primaryELF || len(params) != 2 {
		t.Fatalf("warm open params = %#v", params)
	}
}

func TestRuntimeRejectsInvalidWarmIdentityBeforeStartingBridge(t *testing.T) {
	primaryELF := filepath.Join(t.TempDir(), "primary.elf")
	if err := os.WriteFile(primaryELF, []byte("not-dwarf"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := testRuntimeConfig()
	base.Probe.ID = "daemon-runtime-invalid-warm"
	base.Cores = []config.ValidatedCore{{ID: 0, ELF: primaryELF}}
	for _, test := range []struct {
		name string
		warm WarmSession
	}{
		{name: "non-loopback router", warm: WarmSession{ServiceRouterHost: "192.0.2.1", ServiceRouterPort: 40123, PrimaryELF: primaryELF}},
		{name: "missing router port", warm: WarmSession{ServiceRouterHost: "127.0.0.1", PrimaryELF: primaryELF}},
		{name: "unconfigured ELF", warm: WarmSession{ServiceRouterHost: "127.0.0.1", ServiceRouterPort: 40123, PrimaryELF: testBridgeScript(t)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := false
			_, err := startWith(context.Background(), Options{Config: base, BridgeScript: testBridgeScript(t), Warm: &test.warm}, func(context.Context, bridge.LaunchSpec) (bridgeProcess, error) {
				started = true
				return nil, nil
			})
			if err == nil || started {
				t.Fatalf("startWith() err=%v, bridge started=%v", err, started)
			}
		})
	}
}

func TestRuntimeStartsActorAndDAP(t *testing.T) {
	fake := newRuntimeFakeBridge(t)
	runtime := startRuntimeFake(t, fake, testRuntimeConfig())
	if runtime.DAPAddress() == "" {
		t.Fatal("DAP address is empty")
	}
	threads, err := runtime.Threads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].ID != 1 || threads[1].ID != 2 {
		t.Fatalf("threads = %#v", threads)
	}
	snapshot, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StopEpoch != 1 {
		t.Fatalf("snapshot = %#v, want initial stopped observation", snapshot)
	}
}

func TestRuntimeOpenUsesStartupDeadlineInsteadOfRPCDeadline(t *testing.T) {
	fake := newRuntimeFakeBridge(t)
	fake.blockMethod("open")
	defer fake.unblockMethod("open")
	config := testRuntimeConfig()
	config.Probe.ID = "daemon-runtime-bootstrap-deadline"
	config.Timing.RPCDeadline = 20 * time.Millisecond
	config.Timing.StartupDeadline = 250 * time.Millisecond
	script := testBridgeScript(t)

	result := make(chan struct {
		runtime *Runtime
		err     error
	}, 1)
	go func() {
		runtime, err := startWith(context.Background(), Options{Config: config, BridgeScript: script}, func(context.Context, bridge.LaunchSpec) (bridgeProcess, error) {
			return fake, nil
		})
		result <- struct {
			runtime *Runtime
			err     error
		}{runtime: runtime, err: err}
	}()
	waitRuntimeRequest(t, fake, "open")
	select {
	case completed := <-result:
		t.Fatalf("Start returned before startup budget elapsed: runtime=%v err=%v", completed.runtime, completed.err)
	case <-time.After(50 * time.Millisecond):
	}
	fake.unblockMethod("open")
	completed := <-result
	if completed.err != nil {
		t.Fatalf("Start() error = %v, want success after ordinary RPC deadline", completed.err)
	}
	t.Cleanup(completed.runtime.Close)
}

func TestRuntimeRollsBackWhenDAPListenFails(t *testing.T) {
	fake := newRuntimeFakeBridge(t)
	config := testRuntimeConfig()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reserved.Close()
	config.Endpoints.DAP.Port = reserved.Addr().(*net.TCPAddr).Port
	started := false
	_, err = startWith(context.Background(), Options{Config: config, BridgeScript: testBridgeScript(t)}, func(context.Context, bridge.LaunchSpec) (bridgeProcess, error) {
		started = true
		return fake, nil
	})
	if err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
	if started {
		t.Fatal("DAP preflight started the bridge before rejecting the endpoint")
	}
}

func TestRuntimeRejectsNonRegularBridgeScriptBeforeStart(t *testing.T) {
	called := false
	_, err := startWith(context.Background(), Options{Config: testRuntimeConfig(), BridgeScript: t.TempDir()}, func(context.Context, bridge.LaunchSpec) (bridgeProcess, error) {
		called = true
		return nil, nil
	})
	if err == nil || called {
		t.Fatalf("Start() err=%v, starter called=%v", err, called)
	}
}

func TestRuntimeRefusesDuplicateProbeAndReleasesOnClose(t *testing.T) {
	config := testRuntimeConfig()
	config.Probe.ID = "daemon-runtime-duplicate-probe"
	first := newRuntimeFakeBridge(t)
	runtime := startRuntimeFake(t, first, config)

	secondStarted := false
	_, err := startWith(context.Background(), Options{Config: config, BridgeScript: testBridgeScript(t)}, func(context.Context, bridge.LaunchSpec) (bridgeProcess, error) {
		secondStarted = true
		return newRuntimeFakeBridge(t), nil
	})
	if !errors.Is(err, ErrProbeInUse) || secondStarted {
		t.Fatalf("duplicate Start() err=%v, starter called=%v", err, secondStarted)
	}
	var held *ProbeInUseError
	if !errors.As(err, &held) || held.HolderPID != uint32(os.Getpid()) {
		t.Fatalf("duplicate holder metadata = %#v, want PID %d", held, os.Getpid())
	}

	runtime.Close()
	replacement := newRuntimeFakeBridge(t)
	started := startRuntimeFake(t, replacement, config)
	started.Close()
}

func TestRuntimeDAPAttachAndBridgeExit(t *testing.T) {
	fake := newRuntimeFakeBridge(t)
	runtime := startRuntimeFake(t, fake, testRuntimeConfig())
	serveCtx, stopServe := context.WithCancel(context.Background())
	defer stopServe()
	served := make(chan error, 1)
	go func() { served <- runtime.Serve(serveCtx) }()

	connection, err := net.Dial("tcp", runtime.DAPAddress())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	encoder := dap.NewEncoder(connection)
	decoder := dap.NewDecoder(connection)
	if err := encoder.Encode(dap.Envelope{Seq: 1, Type: dap.TypeRequest, Command: "initialize", Arguments: json.RawMessage(`{"adapterID":"runtime-test"}`)}); err != nil {
		t.Fatal(err)
	}
	assertDAPCommand(t, decoder, "initialize")
	if err := encoder.Encode(dap.Envelope{Seq: 2, Type: dap.TypeRequest, Command: "attach", Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	assertDAPEvent(t, decoder, "initialized")
	if err := encoder.Encode(dap.Envelope{Seq: 3, Type: dap.TypeRequest, Command: "configurationDone", Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	assertDAPCommand(t, decoder, "configurationDone")
	assertDAPCommand(t, decoder, "attach")
	assertDAPEvent(t, decoder, "stopped")

	fake.crash(errors.New("test bridge crash"))
	assertDAPEvent(t, decoder, "terminated")
	select {
	case err := <-served:
		if !errors.Is(err, ErrBridgeExited) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after bridge exit")
	}
	select {
	case err := <-runtime.Errors():
		if !errors.Is(err, ErrBridgeExited) {
			t.Fatalf("runtime error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge exit was not reported")
	}
}

func TestRuntimeServeCancelAndConcurrentClose(t *testing.T) {
	fake := newRuntimeFakeBridge(t)
	runtime := startRuntimeFake(t, fake, testRuntimeConfig())
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- runtime.Serve(ctx) }()
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve(cancelled) = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop on cancellation")
	}

	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			runtime.Close()
		}()
	}
	group.Wait()
	select {
	case <-fake.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not close owned bridge")
	}
}

func TestRuntimeActorFaultIsTerminalWithoutDAPClient(t *testing.T) {
	fake := newRuntimeFakeBridge(t)
	runtime := startRuntimeFake(t, fake, testRuntimeConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- runtime.Serve(ctx) }()

	// Make the next state response violate the typed bridge contract. The
	// actor has no DAP subscriber here, so Runtime must observe Faults rather
	// than relying on a frontend event pump.
	fake.setResult("state", map[string]any{"status": "stopped", "process_info": map[string]string{}})
	if err := runtime.actor.Poll(context.Background()); !errors.Is(err, multi.ErrProtocol) {
		t.Fatalf("Poll() error = %v, want protocol violation", err)
	}
	select {
	case err := <-served:
		if !errors.Is(err, ErrActorFaulted) {
			t.Fatalf("Serve() error = %v, want actor fault", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not terminate after actor fault")
	}
	select {
	case err := <-runtime.Errors():
		if !errors.Is(err, ErrActorFaulted) {
			t.Fatalf("Runtime.Errors() = %v, want actor fault", err)
		}
	case <-time.After(time.Second):
		t.Fatal("actor fault was not reported")
	}
	if metadata := runtime.TerminalMetadata(); !metadata.HasActorOperation || metadata.ActorOperation != actor.OperationState {
		t.Fatalf("terminal metadata = %#v, want state actor operation", metadata)
	}
	select {
	case <-fake.Done():
	case <-time.After(time.Second):
		t.Fatal("actor fault did not close the owned bridge")
	}
}

func assertDAPCommand(t *testing.T, decoder *dap.Decoder, command string) {
	t.Helper()
	message, err := decoder.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != dap.TypeResponse || message.Command != command || message.Success == nil || !*message.Success {
		t.Fatalf("DAP response = %#v, want successful %s", message, command)
	}
}

func assertDAPEvent(t *testing.T, decoder *dap.Decoder, event string) {
	t.Helper()
	message, err := decoder.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != dap.TypeEvent || message.Event != event {
		t.Fatalf("DAP event = %#v, want %s", message, event)
	}
}
