// This source-only fixture exercises the real control and DAP transports.
// It never constructs a MULTI runtime or launches a vendor executable.
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

type backend struct {
	server       *dap.Server
	source       string
	log          *os.File
	mu           sync.Mutex
	nativeMu     sync.Mutex
	nativeStatus tracecontrol.Status
	nativeSource timemachine.Source
}

func (b *backend) record(event string, action dap.ActionKind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = json.NewEncoder(b.log).Encode(map[string]any{"event": event, "action": action})
	_ = b.log.Sync()
}

func (b *backend) DAPCapabilities() dap.Capabilities {
	return dap.Capabilities{SupportsDelayedStackTraceLoading: true, MultiDapSupportsNativeTraceRequests: true}
}

func (b *backend) Execute(_ context.Context, action dap.Action) (dap.ActionResult, error) {
	b.record("action", action.Kind)
	switch action.Kind {
	case dap.ActionAttach, dap.ActionDisconnect:
		return dap.ActionResult{}, nil
	case dap.ActionConfigurationDone:
		if !b.server.PublishTargetState(dap.TargetState{
			Execution: dap.TargetStopped,
			Stopped:   dap.StoppedBody{Reason: "pause", ThreadID: 1},
		}) {
			return dap.ActionResult{}, errors.New("synthetic stopped event was not accepted")
		}
		return dap.ActionResult{}, nil
	case dap.ActionThreads:
		return dap.ActionResult{Threads: []dap.Thread{{ID: 1, Name: "Synthetic core"}}}, nil
	case dap.ActionStackTrace:
		frames := []dap.StackFrame{{
			ID: 1, Name: "synthetic_main", Source: &dap.Source{Name: "fixture.c", Path: b.source},
			Line: 1, Column: 1,
		}}
		if action.StackTrace.StartFrame > 0 {
			frames = []dap.StackFrame{}
		}
		return dap.ActionResult{StackFrames: frames, TotalFrames: 1}, nil
	case dap.ActionScopes:
		return dap.ActionResult{Scopes: []dap.Scope{{
			Name: "Locals", PresentationHint: "locals", VariablesReference: 2,
		}}}, nil
	case dap.ActionVariables:
		return dap.ActionResult{Variables: []dap.Variable{{
			Name: "synthetic_value", Value: "42", Type: "int", EvaluateName: "synthetic_value",
		}}}, nil
	case dap.ActionEvaluate:
		if action.Evaluate.Expression != "synthetic_value" {
			return dap.ActionResult{}, errors.New("only the synthetic fixture expression is available")
		}
		return dap.ActionResult{Evaluation: dap.EvaluateBody{Result: "42", Type: "int"}}, nil
	case dap.ActionSetBreakpoints:
		if len(action.SetBreakpoints.Breakpoints) == 0 {
			return dap.ActionResult{Breakpoints: []dap.Breakpoint{}}, nil
		}
	case dap.ActionTraceStatus, dap.ActionTraceOpen, dap.ActionTracePoll, dap.ActionTraceRead, dap.ActionTraceClose:
		return b.trace(action)
	}
	return dap.ActionResult{}, errors.New("operation is outside the synthetic editor fixture")
}

func (b *backend) trace(action dap.Action) (dap.ActionResult, error) {
	b.nativeMu.Lock()
	defer b.nativeMu.Unlock()
	encode := func(value any) (dap.ActionResult, error) {
		data, err := json.Marshal(value)
		return dap.ActionResult{TraceBody: data}, err
	}
	if b.nativeStatus.State == "" {
		b.nativeStatus = tracecontrol.Status{
			State: "idle", SourceBinding: "operator_declared", Timebase: "unknown", TraceState: "unknown",
		}
	}
	if action.Kind == dap.ActionTraceStatus {
		return encode(struct {
			Status tracecontrol.Status `json:"status"`
		}{b.nativeStatus})
	}
	if action.Kind == dap.ActionTraceOpen {
		if (b.nativeStatus.State != "idle" && b.nativeStatus.State != "closed") ||
			action.TraceOpen.SessionID != 17 || action.TraceOpen.SourceKind != "live" ||
			action.TraceOpen.SourceID != "editor-synthetic-source" || !action.TraceOpen.AcknowledgeUnverifiedSource {
			return dap.ActionResult{}, errors.New("synthetic native source admission failed")
		}
		b.nativeSource = timemachine.Source{
			Kind: action.TraceOpen.SourceKind, SessionID: action.TraceOpen.SessionID,
			SourceID: action.TraceOpen.SourceID, CoreID: action.TraceOpen.CoreID,
			ImageSHA256: action.TraceOpen.ImageSHA256,
		}
		b.nativeStatus = tracecontrol.Status{
			Handle: b.nativeStatus.Handle + 1, State: "open", NativeGeneration: 18446744073709551614,
			PacketCount: 3, PacketCountKnown: true, Timebase: "picoseconds", TraceState: "unknown",
			SourceBinding: "operator_declared",
			LastReceipt:   tracecontrol.Receipt{Operation: "open", Outcome: "observed", CollectionMayContinue: true},
		}
		return encode(tracecontrol.Result{Status: b.nativeStatus, Receipt: b.nativeStatus.LastReceipt})
	}
	if b.nativeStatus.State != "open" {
		return dap.ActionResult{}, errors.New("synthetic native source is not open")
	}
	if action.Kind == dap.ActionTraceRead {
		if action.TraceRead.Handle != b.nativeStatus.Handle || action.TraceRead.Generation != b.nativeStatus.NativeGeneration ||
			action.TraceRead.Start != 0 || action.TraceRead.Count != 3 {
			return dap.ActionResult{}, errors.New("synthetic native interval differs from its fixture")
		}
		profile, err := timemachine.ProfileForVersion("8.1.4")
		if err != nil {
			return dap.ActionResult{}, err
		}
		info := make([]byte, 64)
		binary.LittleEndian.PutUint32(info[8:], 3)
		binary.LittleEndian.PutUint32(info[24:], 1)
		copy(info[32:], "synthetic-only")
		pid := make([]byte, 32)
		pid[0] = 5
		binary.LittleEndian.PutUint64(pid[16:], 17)
		binary.LittleEndian.PutUint64(pid[24:], 23)
		function := func(kind uint32, tag uint64) string {
			packet := make([]byte, 32)
			packet[0] = 10
			binary.LittleEndian.PutUint64(packet[8:], 0x1234)
			binary.LittleEndian.PutUint32(packet[16:], kind)
			binary.LittleEndian.PutUint64(packet[24:], tag)
			return hex.EncodeToString(packet)
		}
		capture := timemachine.Capture{
			Schema: timemachine.CaptureSchema, MultiVersion: "8.1.4", ABI: profile.ABI,
			Source: b.nativeSource, InfoHex: hex.EncodeToString(info),
			Generations: []timemachine.Generation{{
				ID: b.nativeStatus.NativeGeneration, Reason: "initial", FirstIndex: 0,
				PacketsHex: []string{hex.EncodeToString(pid), function(0, 18446744073000000000), function(1, 18446744073100000000)},
			}},
		}
		return encode(tracecontrol.Result{Status: b.nativeStatus, Batch: &capture,
			Receipt: tracecontrol.Receipt{Operation: "read", Outcome: "read", CollectionMayContinue: true}})
	}
	if action.TraceHandle.Handle != b.nativeStatus.Handle {
		return dap.ActionResult{}, errors.New("synthetic native handle differs from its fixture")
	}
	if action.Kind == dap.ActionTraceClose {
		b.nativeStatus.State = "closed"
		b.nativeStatus.LastReceipt = tracecontrol.Receipt{
			Operation: "close", Outcome: "closed", NativeDisconnectConfirmed: true, CollectionMayContinue: true,
		}
		return encode(tracecontrol.Result{Status: b.nativeStatus, Receipt: b.nativeStatus.LastReceipt})
	}
	return encode(tracecontrol.Result{Status: b.nativeStatus,
		Receipt: tracecontrol.Receipt{Operation: "status", Outcome: "observed", CollectionMayContinue: true}})
}

func run() error {
	root := flag.String("root", "", "new isolated fixture directory")
	flag.Parse()
	if *root == "" || !filepath.IsAbs(*root) {
		return errors.New("an absolute fixture root is required")
	}
	if err := os.Mkdir(*root, 0o700); err != nil {
		return err
	}
	multiRoot := filepath.Join(*root, "synthetic-multi")
	if err := os.Mkdir(multiRoot, 0o700); err != nil {
		return err
	}
	for name, text := range map[string]string{
		filepath.Join(multiRoot, "mpythonrun.exe"): "not an executable; this fixture must never launch MULTI\n",
		filepath.Join(*root, "fixture.ghsmc"):      "synthetic project\n",
		filepath.Join(*root, "fixture.elf"):        "synthetic image; never loaded\n",
		filepath.Join(*root, "fixture.c"):          "int synthetic_value = 42;\n",
	} {
		if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
			return err
		}
	}
	configPath := filepath.Join(*root, "project.toml")
	configuration := `[multi]
installation = "synthetic-multi"
executable = "synthetic-multi/mpythonrun.exe"
[connection]
project = "fixture.ghsmc"
arguments = "synthetic; never connected"
[[cores]]
id = 0
elf = "fixture.elf"
[endpoints.mbp]
host = "127.0.0.1"
port = 0
[endpoints.dap]
host = "127.0.0.1"
port = 0
[endpoints.hint]
host = "127.0.0.1"
port = 0
[timing]
poll_cadence = "250ms"
rpc_deadline = "3s"
startup_deadline = "5s"
[probe]
id = "synthetic-editor-acceptance"
`
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		return err
	}
	validated, err := config.Load(configPath)
	if err != nil {
		return err
	}
	digest, err := config.BindingDigest(validated)
	if err != nil {
		return err
	}
	logPath := filepath.Join(*root, "events.ndjson")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	b := &backend{source: filepath.Join(*root, "fixture.c"), log: log}
	dapServer, err := dap.Listen("127.0.0.1:0", b)
	if err != nil {
		return err
	}
	defer dapServer.Close()
	b.server = dapServer
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	controlServer, err := control.NewServer(control.ServerOptions{
		ProbeID: validated.Probe.ID, ConfigDigest: digest, DAPAddress: dapServer.Addr().String(),
		Status: func(context.Context) (control.Status, error) {
			b.record("status", 0)
			return control.Status{
				ProbeID: validated.Probe.ID, PID: os.Getpid(), DAPAddress: dapServer.Addr().String(),
				TargetState: "stopped", StopEpoch: 1,
			}, nil
		},
		Shutdown: cancel,
		Proxy: func(ctx context.Context, connection net.Conn) error {
			b.record("proxy", 0)
			return dapServer.ServeConnection(ctx, connection)
		},
	})
	if err != nil {
		return err
	}
	defer controlServer.Close()
	controlDir := filepath.Join(*root, "control")
	store, err := control.NewStore(controlDir)
	if err != nil {
		return err
	}
	if err := store.Publish(controlServer.Record()); err != nil {
		return err
	}
	defer store.RemoveIfInstance(validated.Probe.ID, controlServer.Record().InstanceID)
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"synthetic": true, "configFile": configPath, "controlDirectory": controlDir,
		"primaryElf": filepath.Join(*root, "fixture.elf"), "sourceFile": b.source, "eventsFile": logPath,
	}); err != nil {
		return err
	}
	return controlServer.Serve(ctx)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
