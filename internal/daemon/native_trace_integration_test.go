package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/multi"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracebridge"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

const (
	nativeTraceFixtureEnvironment = "MULTI_DAP_DAEMON_NATIVE_TRACE_TEST_SCRIPT"
	nativeTraceFixtureMarker      = "# multi-dap daemon native trace fake helper v1\n"
	nativeTraceFixtureFilename    = "daemon-native-trace-fixture.py"
)

// Start passes Python flags to this test executable. Intercept only the
// explicitly marked fixture before testing parses those flags; no Python,
// MULTI installation, vendor library, or target is used by this subprocess.
func init() {
	script := os.Getenv(nativeTraceFixtureEnvironment)
	if script == "" || len(os.Args) != 6 ||
		!slices.Equal(os.Args[1:5], []string{"-I", "-S", "-B", "-u"}) ||
		os.Args[5] != script || filepath.Base(script) != nativeTraceFixtureFilename {
		return
	}
	data, err := os.ReadFile(script)
	if err != nil || !bytes.HasPrefix(data, []byte(nativeTraceFixtureMarker)) {
		return
	}
	mode := strings.TrimPrefix(string(data), nativeTraceFixtureMarker)
	if mode != "read_only\n" && mode != "status_timeout\n" {
		return
	}
	os.Exit(runNativeTraceIntegrationHelper(strings.TrimSpace(mode), filepath.Dir(script)))
}

func runNativeTraceIntegrationHelper(mode, directory string) int {
	log, err := os.OpenFile(filepath.Join(directory, "operations.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	defer log.Close()
	output := json.NewEncoder(os.Stdout)
	if err := output.Encode(map[string]any{
		"v": 1, "event": "ready", "protocol": tracebridge.ProtocolName,
		"python_major": 3, "python_minor": 8, "pointer_bits": 64,
	}); err != nil {
		return 3
	}
	input := bufio.NewScanner(os.Stdin)
	input.Buffer(make([]byte, 4096), tracebridge.MaxRequestBytes)
	var sequence int64
	opened := false
	for input.Scan() {
		var request struct {
			Version int             `json:"v"`
			Seq     int64           `json:"seq"`
			Op      string          `json:"op"`
			Args    json.RawMessage `json:"args"`
		}
		if json.Unmarshal(input.Bytes(), &request) != nil || request.Version != 1 || request.Seq != sequence+1 {
			return 4
		}
		sequence = request.Seq
		if _, err := io.WriteString(log, request.Op+"\n"); err != nil {
			return 5
		}
		var result any
		switch request.Op {
		case "open":
			var args tracebridge.OpenArgs
			if opened || json.Unmarshal(request.Args, &args) != nil ||
				args.SessionID != 17 || args.MultiVersion != "8.1.4" ||
				args.DLLSHA256 != strings.Repeat("a", 64) {
				return 6
			}
			opened = true
			result = nativeTraceIntegrationStatus()
		case "status":
			if !opened {
				return 7
			}
			if mode == "status_timeout" {
				// Simulate a blocked native owner. Only the real transport's
				// process-abort boundary can finish this case promptly.
				time.Sleep(30 * time.Second)
				return 8
			}
			result = nativeTraceIntegrationStatus()
		case "read":
			var args tracebridge.ReadArgs
			if !opened || json.Unmarshal(request.Args, &args) != nil ||
				args.Start != 0 || args.Count != 3 || args.Generation != 1 {
				return 9
			}
			result = tracebridge.ReadResult{
				Generation: 1, Start: 0, Count: 3, PacketsHex: nativeTraceIntegrationPackets(),
			}
		case "close":
			if !opened {
				return 10
			}
			result = map[string]bool{"closed": true}
		default:
			// In particular, unexpected start/stop is logged and fails;
			// this read-only fixture cannot accidentally emulate control.
			return 11
		}
		if err := output.Encode(map[string]any{"v": 1, "seq": sequence, "ok": true, "result": result}); err != nil {
			return 12
		}
		if request.Op == "close" {
			return 0
		}
	}
	if input.Err() != nil {
		return 13
	}
	return 0
}

func nativeTraceIntegrationStatus() tracebridge.Status {
	info := make([]byte, 64)
	binary.LittleEndian.PutUint32(info[8:], 3)
	binary.LittleEndian.PutUint32(info[24:], 1)
	return tracebridge.Status{
		InfoHex: hex.EncodeToString(info), PacketCount: 3, Generation: 1, TraceState: "unknown",
		Notifications: tracebridge.Notifications{Events: []string{}},
	}
}

func nativeTraceIntegrationPackets() []string {
	pid, entry, exit := make([]byte, 32), make([]byte, 32), make([]byte, 32)
	pid[0] = 5
	binary.LittleEndian.PutUint64(pid[16:], 7)
	binary.LittleEndian.PutUint64(pid[24:], 11)
	for _, packet := range [][]byte{entry, exit} {
		packet[0] = 10
		binary.LittleEndian.PutUint64(packet[8:], 0x1234)
	}
	binary.LittleEndian.PutUint64(entry[24:], 1_000_000_000_000)
	binary.LittleEndian.PutUint32(exit[16:], 1)
	binary.LittleEndian.PutUint64(exit[24:], 1_000_100_000_000)
	return []string{hex.EncodeToString(pid), hex.EncodeToString(entry), hex.EncodeToString(exit)}
}

type nativeTraceIntegrationLease struct {
	once     sync.Once
	released chan struct{}
}

func (l *nativeTraceIntegrationLease) Close() error {
	l.once.Do(func() { close(l.released) })
	return nil
}

type nativeTraceIntegration struct {
	core         *actor.Actor
	controller   *tracecontrol.Controller
	mbp          *runtimeFakeBridge
	executor     *bridge.Executor
	connection   net.Conn
	encoder      *dap.Encoder
	decoder      *dap.Decoder
	sequence     int64
	directory    string
	spawned      chan *tracebridge.Client
	lease        *nativeTraceIntegrationLease
	acquisitions atomic.Int32
}

func newNativeTraceIntegration(t *testing.T, mode string, rpcDeadline time.Duration) *nativeTraceIntegration {
	t.Helper()
	f := &nativeTraceIntegration{
		directory: t.TempDir(), spawned: make(chan *tracebridge.Client, 1),
		lease: &nativeTraceIntegrationLease{released: make(chan struct{})},
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(f.directory, nativeTraceFixtureFilename)
	if err := os.WriteFile(script, []byte(nativeTraceFixtureMarker+mode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err = filepath.EvalSymlinks(script)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(nativeTraceFixtureEnvironment, script)
	options := tracebridge.Options{
		Python: executable, Script: script, Installation: filepath.Dir(executable),
		ExpectedPointerBits: 64, ExpectedInfoSize: 64,
		StartupTimeout: 5 * time.Second, RPCTimeout: 5 * time.Second, ShutdownTimeout: 3 * time.Second,
	}
	f.controller, err = tracecontrol.New(tracecontrol.Options{
		Installation: options.Installation, MultiVersion: "8.1.4", DLLSHA256: strings.Repeat("a", 64),
		AcquireSource: func(ctx context.Context, id uint32) (io.Closer, error) {
			if ctx.Err() != nil || id != 17 || f.acquisitions.Add(1) != 1 {
				return nil, errors.New("unexpected synthetic source acquisition")
			}
			return f.lease, nil
		},
		Factory: func(ctx context.Context, lease io.Closer) (tracecontrol.Backend, error) {
			if lease != f.lease {
				return nil, errors.New("synthetic source lease was not forwarded")
			}
			client, err := tracebridge.Start(ctx, options)
			if client == nil {
				return nil, err
			}
			if err != nil {
				return client, err
			}
			f.spawned <- client
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { awaitNativeTraceIntegration(t, f.controller.ShutdownDone(), "controller shutdown") })
	f.mbp = newRuntimeFakeBridge(t)
	f.mbp.setResult("cores", map[string]any{
		"components": "fixture.component.5 (debugger.name.core0)\n", "status": 1,
	})
	f.mbp.setResult("state", map[string]any{
		"status": 3, "process_info": map[string]string{"stopStamp": "1", "pid": "1"},
	})
	f.executor = bridge.NewExecutor(f.mbp.client)
	f.core, err = actor.New(actor.Options{
		Executor: f.executor, Open: multi.OpenRequest{Project: "synthetic.gpj", Connection: "-target fake"},
		CoreSpecs:    []multi.CoreSpec{{ID: 0, ELF: `C:\fixture\core0.elf`}},
		PollInterval: time.Hour, RPCDeadline: rpcDeadline, BootstrapDeadline: 6 * time.Second,
		EventBuffer: 16, Trace: f.controller,
	})
	if err != nil {
		f.executor.Close()
		t.Fatal(err)
	}
	t.Cleanup(f.core.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := f.core.Open(ctx); err != nil {
		t.Fatal(err)
	}
	backend, err := NewBackend(f.core)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	server, err := dap.Listen("127.0.0.1:0", backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if err := backend.BindPublisher(server); err != nil {
		t.Fatal(err)
	}
	serveContext, stopServing := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveContext) }()
	t.Cleanup(func() {
		stopServing()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("DAP server shutdown: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Error("DAP server did not shut down")
		}
	})
	f.connection, err = net.DialTimeout("tcp", server.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.connection.Close() })
	f.encoder, f.decoder = dap.NewEncoder(f.connection), dap.NewDecoder(f.connection)
	initialized := f.request(t, "initialize", dap.InitializeArguments{AdapterID: "multi-dap"})
	requireNativeTraceSuccess(t, initialized)
	var capabilities dap.Capabilities
	if err := json.Unmarshal(initialized.Body, &capabilities); err != nil ||
		!capabilities.MultiDapSupportsNativeTraceRequests {
		t.Fatalf("native trace capability not wired: %s (%v)", initialized.Body, err)
	}
	attachSequence := f.send(t, "attach", struct{}{})
	if message := f.next(t); message.Type != dap.TypeEvent || message.Event != "initialized" {
		t.Fatalf("attach did not open configuration: %#v", message)
	}
	configurationSequence := f.send(t, "configurationDone", struct{}{})
	requireNativeTraceSuccess(t, f.response(t, configurationSequence))
	requireNativeTraceSuccess(t, f.response(t, attachSequence))
	return f
}

func (f *nativeTraceIntegration) send(t *testing.T, command string, args any) int64 {
	t.Helper()
	body, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	f.sequence++
	if err := f.connection.SetWriteDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	writeDAPRequest(t, f.encoder, f.sequence, command, string(body))
	return f.sequence
}

func (f *nativeTraceIntegration) next(t *testing.T) dap.Envelope {
	t.Helper()
	if err := f.connection.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	message, err := f.decoder.Decode()
	if err != nil {
		t.Fatalf("read native trace integration DAP message: %v", err)
	}
	return message
}

func (f *nativeTraceIntegration) response(t *testing.T, sequence int64) dap.Envelope {
	t.Helper()
	for range 16 {
		message := f.next(t)
		if message.Type == dap.TypeEvent && message.Event != "terminated" {
			continue
		}
		if message.Type != dap.TypeResponse || message.RequestSeq != sequence {
			t.Fatalf("unexpected DAP output while awaiting %d: %#v", sequence, message)
		}
		return message
	}
	t.Fatal("native trace response was starved by events")
	return dap.Envelope{}
}

func (f *nativeTraceIntegration) request(t *testing.T, command string, args any) dap.Envelope {
	t.Helper()
	return f.response(t, f.send(t, command, args))
}

func requireNativeTraceSuccess(t *testing.T, response dap.Envelope) {
	t.Helper()
	if response.Success == nil || !*response.Success {
		t.Fatalf("%s failed: %s (%s)", response.Command, response.Message, response.Body)
	}
}

func (f *nativeTraceIntegration) open(t *testing.T) (uint64, *tracebridge.Client) {
	t.Helper()
	coreID := uint32(0)
	response := f.request(t, "multi-dap/trace/open", dap.NativeTraceOpenArguments{
		SourceKind: "live", SessionID: 17, SourceID: "synthetic-e2e", CoreID: &coreID,
		AcknowledgeUnverifiedSource: true,
	})
	requireNativeTraceSuccess(t, response)
	var result tracecontrol.Result
	if err := json.Unmarshal(response.Body, &result); err != nil || result.Status.Handle == 0 ||
		result.Status.State != "open" || result.Status.NativeGeneration != 1 ||
		result.Status.SourceBinding != "operator_declared" || result.Status.NativeRuntimeQualified ||
		result.Status.HardwareQualified || result.Batch != nil || f.acquisitions.Load() != 1 {
		t.Fatalf("invalid native open response: %s (%v)", response.Body, err)
	}
	select {
	case client := <-f.spawned:
		return result.Status.Handle, client
	case <-time.After(8 * time.Second):
		t.Fatal("open succeeded without launching the real transport subprocess")
		return 0, nil
	}
}

func (f *nativeTraceIntegration) localStatus(t *testing.T) tracecontrol.Status {
	t.Helper()
	response := f.request(t, "multi-dap/trace/status", struct{}{})
	requireNativeTraceSuccess(t, response)
	var result struct {
		Status tracecontrol.Status `json:"status"`
	}
	if err := json.Unmarshal(response.Body, &result); err != nil ||
		strings.Contains(string(response.Body), "synthetic-e2e") {
		t.Fatalf("invalid or source-disclosing local status: %s (%v)", response.Body, err)
	}
	return result.Status
}

func (f *nativeTraceIntegration) assertOperations(t *testing.T, expected ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.directory, "operations.log"))
	if err != nil {
		t.Fatal(err)
	}
	if operations := strings.Fields(string(data)); !slices.Equal(operations, expected) {
		t.Fatalf("helper operations = %v, want %v", operations, expected)
	}
	f.mbp.mu.Lock()
	defer f.mbp.mu.Unlock()
	for _, request := range f.mbp.requests {
		switch request.Method {
		case "open", "cores", "state":
		case "run_" + "commands":
			var params map[string]string
			if err := json.Unmarshal(request.Params, &params); err != nil ||
				(params["commands"] != "route fixture.component.5 P" && params["commands"] != "H") {
				t.Fatalf("unexpected routed MBP request: %s", request.Params)
			}
		default:
			t.Fatalf("native trace issued an unexpected MBP operation: %s", request.Method)
		}
	}
}

func (f *nativeTraceIntegration) mbpCalls(method string) int {
	f.mbp.mu.Lock()
	defer f.mbp.mu.Unlock()
	count := 0
	for _, request := range f.mbp.requests {
		if method == "" || request.Method == method {
			count++
		}
	}
	return count
}

func awaitNativeTraceIntegration(t *testing.T, done <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func TestNativeTraceProcessE2EReadOnlyCapture(t *testing.T) {
	f := newNativeTraceIntegration(t, "read_only", 4*time.Second)
	before, err := f.core.Snapshot(context.Background())
	if err != nil || before.State != session.TargetRunning {
		t.Fatalf("initial synthetic target state = %#v, %v", before, err)
	}
	mbpCalls := f.mbpCalls("")
	handle, helper := f.open(t)
	if status := f.localStatus(t); status.PacketCount != 3 || !status.PacketCountKnown ||
		status.Timebase != "picoseconds" || status.NeedsReconciliation {
		t.Fatalf("local open status = %#v", status)
	}
	f.assertOperations(t, "open")
	requireNativeTraceSuccess(t, f.request(t, "multi-dap/trace/poll", dap.NativeTraceHandleArguments{Handle: handle}))
	response := f.request(t, "multi-dap/trace/read", dap.NativeTraceReadArguments{
		Handle: handle, Start: 0, Generation: 1, Count: 3,
	})
	requireNativeTraceSuccess(t, response)
	var read struct {
		Status  tracecontrol.Status  `json:"status"`
		Batch   json.RawMessage      `json:"batch"`
		Receipt tracecontrol.Receipt `json:"receipt"`
	}
	if err := json.Unmarshal(response.Body, &read); err != nil || len(read.Batch) == 0 ||
		read.Status.Handle != handle || read.Receipt.Outcome != "read" {
		t.Fatalf("capture was not returned over DAP: %s (%v)", response.Body, err)
	}
	capture, err := timemachine.DecodeCapture(bytes.NewReader(read.Batch))
	if err != nil || capture.Source.Kind != "live" || capture.Source.SourceID != "synthetic-e2e" ||
		capture.Source.SessionID != 17 || capture.Source.CoreID == nil || *capture.Source.CoreID != 0 ||
		len(capture.Generations) != 1 || capture.Generations[0].ID != 1 ||
		!slices.Equal(capture.Generations[0].PacketsHex, nativeTraceIntegrationPackets()) {
		t.Fatalf("capture provenance or packets changed: %#v (%v)", capture, err)
	}
	report, err := timemachine.Normalize(capture)
	if err != nil || len(report.Spans) != 1 || report.Statistics.CompletedFunctionSpans != 1 ||
		report.Spans[0].Elapsed != "100000000" || report.Spans[0].Unit != "picoseconds" ||
		report.Spans[0].Context != (timemachine.Context{TaskID: "7", AddressSpaceID: "11"}) ||
		report.NativeRuntimeQualified || report.HardwareQualified || report.TargetCPUTime {
		t.Fatalf("software-only interval normalization = %#v (%v)", report, err)
	}
	closed := f.request(t, "multi-dap/trace/close", dap.NativeTraceHandleArguments{Handle: handle})
	requireNativeTraceSuccess(t, closed)
	var result tracecontrol.Result
	if err := json.Unmarshal(closed.Body, &result); err != nil || result.Status.State != "closed" ||
		!result.Receipt.NativeDisconnectConfirmed || result.Status.NeedsReconciliation || result.Batch != nil {
		t.Fatalf("explicit helper close = %s (%v)", closed.Body, err)
	}
	awaitNativeTraceIntegration(t, helper.Done(), "closed helper reaping")
	awaitNativeTraceIntegration(t, f.lease.released, "source lease release")
	after, err := f.core.Snapshot(context.Background())
	if err != nil || before != after || helper.Poisoned() || f.mbp.client.Poisoned() ||
		f.mbpCalls("") != mbpCalls {
		t.Fatalf("read-only capture changed execution or poisoned a transport: before=%#v after=%#v err=%v", before, after, err)
	}
	requireNativeTraceSuccess(t, f.request(t, "disconnect", struct{}{}))
	f.core.Close()
	f.assertOperations(t, "open", "status", "status", "read", "close")
}

func TestNativeTraceProcessE2ETimeoutDoesNotPoisonMBP(t *testing.T) {
	f := newNativeTraceIntegration(t, "status_timeout", 500*time.Millisecond)
	before, err := f.core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	generation := f.executor.Generation()
	handle, helper := f.open(t)
	response := f.request(t, "multi-dap/trace/poll", dap.NativeTraceHandleArguments{Handle: handle})
	if response.Success == nil || *response.Success || len(response.Body) != 0 ||
		(response.Message != dap.NewNativeTraceError(dap.NativeTraceTimeout, nil).Error() &&
			response.Message != dap.NewNativeTraceError(dap.NativeTraceReconciliationRequired, nil).Error()) {
		t.Fatalf("helper timeout did not return a sanitized failure: %#v", response)
	}
	awaitNativeTraceIntegration(t, helper.Done(), "timed-out helper reaping")
	status := f.localStatus(t)
	if !status.NeedsReconciliation || !status.LastReceipt.NativeStateUncertain ||
		!status.LastReceipt.HelperAbortRequested || !status.LastReceipt.CollectionMayContinue ||
		status.LastReceipt.NativeDisconnectConfirmed || !helper.Poisoned() {
		t.Fatalf("timeout lost its uncertain native-state receipt: %#v", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stateCalls := f.mbpCalls("state")
	if err := f.core.Poll(ctx); err != nil {
		t.Fatalf("independent native timeout broke normal MBP polling: %v", err)
	}
	after, err := f.core.Snapshot(ctx)
	if err != nil || before != after || f.mbp.client.Poisoned() || f.executor.Generation() != generation ||
		f.mbpCalls("state") != stateCalls+1 {
		t.Fatalf("native timeout changed target/MBP generation: before=%#v after=%#v err=%v", before, after, err)
	}
	requireNativeTraceSuccess(t, f.request(t, "threads", struct{}{}))
	rejected := f.request(t, "multi-dap/trace/read", dap.NativeTraceReadArguments{
		Handle: handle, Start: 0, Generation: 1, Count: 3,
	})
	if rejected.Success == nil || *rejected.Success ||
		rejected.Message != dap.NewNativeTraceError(dap.NativeTraceReconciliationRequired, nil).Error() {
		t.Fatalf("faulted helper was readmitted: %#v", rejected)
	}
	f.core.Close()
	awaitNativeTraceIntegration(t, f.controller.ShutdownDone(), "timed-out controller shutdown")
	awaitNativeTraceIntegration(t, f.lease.released, "shutdown source lease release")
	f.assertOperations(t, "open", "status")
}
