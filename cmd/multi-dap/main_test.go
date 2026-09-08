package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/daemon"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	buildversion "github.com/Tacrolimus/multi-dap/internal/version"
)

const testConfigDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const otherTestConfigDigest = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

func bindingDigestForPath(t *testing.T, path string) string {
	t.Helper()
	validated, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := config.BindingDigest(validated)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestVersionCommandsWriteMachineReadableBuildMetadata(t *testing.T) {
	previousVersion, previousCommit, previousBuildDate := buildversion.Version, buildversion.Commit, buildversion.BuildDate
	t.Cleanup(func() {
		buildversion.Version, buildversion.Commit, buildversion.BuildDate = previousVersion, previousCommit, previousBuildDate
	})
	buildversion.Version, buildversion.Commit, buildversion.BuildDate = "1.2.3", "abc1234", "2026-09-08T00:00:00Z"
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var output bytes.Buffer
		if err := run(context.Background(), args, &output); err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		var got buildversion.Info
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatalf("decode %v output %q: %v", args, output.String(), err)
		}
		if want := (buildversion.Info{Version: "1.2.3", Commit: "abc1234", BuildDate: "2026-09-08T00:00:00Z"}); got != want {
			t.Fatalf("version output = %#v, want %#v", got, want)
		}
	}
}

func TestResolveBridgeScriptUsesExecutableDirectory(t *testing.T) {
	executableDir := t.TempDir()
	installedBridge := filepath.Join(executableDir, defaultBridgeScript)
	if err := os.MkdirAll(filepath.Dir(installedBridge), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedBridge, []byte("# bridge\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousExecutable := executablePath
	executablePath = func() (string, error) { return filepath.Join(executableDir, "multi-dap.exe"), nil }
	t.Cleanup(func() { executablePath = previousExecutable })
	got, err := resolveBridgeScript("")
	if err != nil {
		t.Fatal(err)
	}
	if got != installedBridge {
		t.Fatalf("resolved bridge = %q, want %q", got, installedBridge)
	}
}

func TestResolveBridgeScriptRejectsMissingBundledBridgeAndHonorsExplicitPath(t *testing.T) {
	explicitDirectory := t.TempDir()
	bridge := filepath.Join(explicitDirectory, defaultBridgeScript)
	if err := os.MkdirAll(filepath.Dir(bridge), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bridge, []byte("# bridge\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousExecutable := executablePath
	executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "multi-dap.exe"), nil }
	t.Cleanup(func() { executablePath = previousExecutable })
	if _, err := resolveBridgeScript(""); err == nil || !strings.Contains(err.Error(), "pass --bridge-script explicitly") {
		t.Fatalf("missing bundled bridge error = %v", err)
	}
	if explicit, err := resolveBridgeScript(bridge); err != nil || explicit != bridge {
		t.Fatalf("explicit bridge = (%q, %v), want (%q, nil)", explicit, err, bridge)
	}
}

func TestDoctorChecksConfigAndWritesStableJSON(t *testing.T) {
	configPath := writeConfig(t)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"doctor", "--config", configPath}, &output); err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	if got, want := output.String(), "{\"event\":\"config_checked\",\"ok\":true}\n"; got != want {
		t.Fatalf("doctor output = %q, want %q", got, want)
	}
}

func TestCheckIsDocumentedDoctorAlias(t *testing.T) {
	configPath := writeConfig(t)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"check", "--config", configPath}, &output); err != nil {
		t.Fatalf("run check: %v", err)
	}
	if got, want := output.String(), "{\"event\":\"config_checked\",\"ok\":true}\n"; got != want {
		t.Fatalf("check output = %q, want %q", got, want)
	}
	if !strings.Contains(usage(), "doctor|check") {
		t.Fatalf("usage does not document check alias: %q", usage())
	}
}

func TestServeWritesReadyAfterDaemonStartAndClosesOnCancellation(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := startRuntime
	t.Cleanup(func() { startRuntime = previous })
	fake := newFakeRuntime()
	var received daemon.Options
	startRuntime = func(_ context.Context, options daemon.Options) (runtime, error) {
		received = options
		return fake, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if err := run(ctx, []string{"serve", "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", t.TempDir()}, &output); err != nil {
		t.Fatalf("run serve: %v", err)
	}
	var ready readyMessage
	if err := json.Unmarshal(output.Bytes(), &ready); err != nil {
		t.Fatalf("decode ready output: %v", err)
	}
	if ready.Event != "ready" || ready.DAPAddress != "127.0.0.1:41234" || ready.PID != os.Getpid() {
		t.Fatalf("unexpected ready message: %+v", ready)
	}
	if bytes.Contains(output.Bytes(), []byte("target server arguments")) {
		t.Fatalf("ready output leaked connection arguments: %q", output.String())
	}
	if received.BridgeScript != bridgeScript {
		t.Fatalf("bridge script = %q, want %q", received.BridgeScript, bridgeScript)
	}
	if !fake.closed {
		t.Fatal("runtime was not closed")
	}
}

func TestServePassesWarmSessionAsRuntimeOnlyState(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	primaryELF := filepath.Join(filepath.Dir(configPath), "core.elf")
	previous := startRuntime
	t.Cleanup(func() { startRuntime = previous })
	fake := newFakeRuntime()
	var received daemon.Options
	startRuntime = func(_ context.Context, options daemon.Options) (runtime, error) {
		received = options
		return fake, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := run(ctx, []string{
		"serve", "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", t.TempDir(),
		"--session-mode", "warm", "--service-router-port", "40123", "--primary-elf", primaryELF,
	}, &output)
	if err != nil {
		t.Fatalf("run warm serve: %v", err)
	}
	if received.Warm == nil || received.Warm.ServiceRouterHost != "127.0.0.1" || received.Warm.ServiceRouterPort != 40123 || received.Warm.PrimaryELF != primaryELF {
		t.Fatalf("warm options = %#v", received.Warm)
	}
	if strings.Contains(output.String(), "40123") || strings.Contains(output.String(), primaryELF) {
		t.Fatalf("ready output leaked warm session identity: %q", output.String())
	}
}

func TestWarmSessionFlagsAreAtomic(t *testing.T) {
	primaryELF := filepath.Join(t.TempDir(), "primary.elf")
	if err := os.WriteFile(primaryELF, []byte("elf"), 0o600); err != nil {
		t.Fatal(err)
	}
	warm, err := warmSessionOptions("warm", "", 40123, primaryELF)
	if err != nil {
		t.Fatal(err)
	}
	if warm.ServiceRouterHost != "127.0.0.1" || warm.ServiceRouterPort != 40123 || warm.PrimaryELF != primaryELF {
		t.Fatalf("warm session = %#v", warm)
	}
	if cold, err := warmSessionOptions("cold", "", 0, ""); err != nil || cold != nil {
		t.Fatalf("cold session = (%#v, %v)", cold, err)
	}
	for _, test := range []struct {
		mode    string
		host    string
		port    int
		primary string
	}{
		{mode: "cold", port: 40123},
		{mode: "warm", primary: primaryELF},
		{mode: "warm", port: 40123},
		{mode: "recover", port: 40123, primary: primaryELF},
	} {
		if _, err := warmSessionOptions(test.mode, test.host, test.port, test.primary); err == nil {
			t.Fatalf("warmSessionOptions(%q, %q, %d, %q) unexpectedly succeeded", test.mode, test.host, test.port, test.primary)
		}
	}
}

func TestServeReturnsDaemonFailure(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := startRuntime
	t.Cleanup(func() { startRuntime = previous })
	fake := newFakeRuntime()
	fake.serveErr = errors.New("bridge exited")
	startRuntime = func(context.Context, daemon.Options) (runtime, error) { return fake, nil }
	err := run(context.Background(), []string{"serve", "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", t.TempDir()}, &bytes.Buffer{})
	if err == nil || err.Error() != "bridge exited" {
		t.Fatalf("serve error = %v", err)
	}
}

func TestServeWritesSafeTerminalDiagnostic(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	controlDir := t.TempDir()
	previous := startRuntime
	t.Cleanup(func() { startRuntime = previous })
	fake := newFakeRuntime()
	fake.serveErr = fmt.Errorf("%w: sensitive raw detail", daemon.ErrActorFaulted)
	fake.terminal = daemon.TerminalMetadata{ActorOperation: actor.OperationState, HasActorOperation: true}
	startRuntime = func(context.Context, daemon.Options) (runtime, error) { return fake, nil }
	if err := run(context.Background(), []string{"serve", "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", controlDir}, io.Discard); err == nil {
		t.Fatal("serve unexpectedly succeeded")
	}
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.LoadTerminal("test-probe")
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Phase != control.TerminalPhaseRuntimeServe || terminal.Classification != control.TerminalActorFault || terminal.Operation != control.TerminalOperationState || terminal.OccurredAt.IsZero() {
		t.Fatalf("terminal = %#v", terminal)
	}
	raw, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sensitive raw detail") {
		t.Fatalf("terminal record stored raw error: %q", raw)
	}
}

func TestServeWritesStartupTerminalDiagnosticAfterServerIdentityExists(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	controlDir := t.TempDir()
	previous := startRuntime
	t.Cleanup(func() { startRuntime = previous })
	startRuntime = func(context.Context, daemon.Options) (runtime, error) {
		return nil, fmt.Errorf("startup failed: %w", context.DeadlineExceeded)
	}
	var output bytes.Buffer
	err := run(context.Background(), []string{"serve", "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", controlDir}, &output)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("serve error = %v, want deadline", err)
	}
	if output.Len() != 0 {
		t.Fatalf("startup failure wrote ready output: %q", output.String())
	}
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.LoadTerminal("test-probe")
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Phase != control.TerminalPhaseRuntimeStart || terminal.Classification != control.TerminalContextDeadline || terminal.Operation != "" || terminal.OccurredAt.IsZero() {
		t.Fatalf("startup terminal = %#v", terminal)
	}
	if _, err := store.Load("test-probe"); !errors.Is(err, control.ErrNoRecord) {
		t.Fatalf("startup reservation remains: %v", err)
	}
}

func TestDiagnoseReportsOnlySafeTerminalFields(t *testing.T) {
	configPath := writeConfig(t)
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	instance := strings.Repeat("A", 43)
	if err := store.WriteTerminal(control.TerminalRecord{
		Version: control.ProtocolVersion, ProbeID: "test-probe", ConfigDigest: bindingDigestForPath(t, configPath), InstanceID: instance, PID: 99,
		Phase: control.TerminalPhaseRuntimeServe, Classification: control.TerminalBridgeExit,
		Operation:  control.TerminalOperationInspection,
		OccurredAt: time.Date(2026, time.August, 20, 1, 2, 3, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"diagnose", "--config", configPath, "--control-dir", controlDir}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"event\":\"terminal_diagnostic\",\"found\":true,\"probe_id\":\"test-probe\",\"pid\":99,\"phase\":\"runtime_serve\",\"classification\":\"bridge_exit\",\"operation\":\"inspection\",\"occurred_at\":\"2026-08-20T01:02:03Z\"}\n"; got != want {
		t.Fatalf("diagnose output = %q, want %q", got, want)
	}
	if strings.Contains(output.String(), instance) {
		t.Fatalf("diagnose output leaked instance ID: %q", output.String())
	}
}

func TestDiagnoseOmitsTerminalFieldsWhenNoRecordExists(t *testing.T) {
	configPath := writeConfig(t)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"diagnose", "--config", configPath, "--control-dir", t.TempDir()}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"event\":\"terminal_diagnostic\",\"found\":false}\n"; got != want {
		t.Fatalf("diagnose output = %q, want %q", got, want)
	}
}

func TestClassifyTerminalUsesStableCauseClasses(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want control.TerminalClassification
	}{
		{name: "deadline wins through actor wrapper", err: fmt.Errorf("%w: %w", daemon.ErrActorFaulted, context.DeadlineExceeded), want: control.TerminalContextDeadline},
		{name: "bridge exit", err: daemon.ErrBridgeExited, want: control.TerminalBridgeExit},
		{name: "actor fault", err: daemon.ErrActorFaulted, want: control.TerminalActorFault},
		{name: "runtime closed", err: daemon.ErrRuntimeClosed, want: control.TerminalRuntimeClosed},
		{name: "other", err: errors.New("unclassified"), want: control.TerminalOther},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyTerminal(test.err); got != test.want {
				t.Fatalf("classifyTerminal(%v) = %q, want %q", test.err, got, test.want)
			}
		})
	}
}

func TestClassifyTerminalOperationUsesActorMetadata(t *testing.T) {
	if got := classifyTerminalOperation(daemon.TerminalMetadata{ActorOperation: actor.OperationInspection, HasActorOperation: true}); got != control.TerminalOperationInspection {
		t.Fatalf("inspection operation = %q", got)
	}
	if got := classifyTerminalOperation(daemon.TerminalMetadata{}); got != "" {
		t.Fatalf("absent operation = %q, want empty", got)
	}
	if got := classifyTerminalOperation(daemon.TerminalMetadata{ActorOperation: actor.Operation(99), HasActorOperation: true}); got != control.TerminalOperationUnknown {
		t.Fatalf("unknown operation = %q", got)
	}
}

func TestControlCommandsRequireConfig(t *testing.T) {
	for _, command := range []string{"status", "shutdown"} {
		err := run(context.Background(), []string{command}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--config is required") {
			t.Fatalf("%s error = %v", command, err)
		}
	}
}

func TestProxyAcceptsStableProbeIdentityWithoutProjectConfig(t *testing.T) {
	controlDir := t.TempDir()
	target, err := proxyCommandFlags([]string{"--probe-id", "  test-probe  ", "--control-dir", controlDir})
	if err != nil {
		t.Fatal(err)
	}
	if target.probeID != "test-probe" || target.store == nil || !target.legacy {
		t.Fatalf("proxy flags = %#v", target)
	}
	if _, err := target.store.Load(target.probeID); !errors.Is(err, control.ErrNoRecord) {
		t.Fatalf("empty proxy store Load = %v, want ErrNoRecord", err)
	}
}

func TestProxyIdentityFlagsAreExclusiveAndValidated(t *testing.T) {
	configPath := writeConfig(t)
	for _, arguments := range [][]string{
		{},
		{"--probe-id", "not/a/probe"},
		{"--probe-id", "test-probe", "--config", configPath},
	} {
		if _, err := proxyCommandFlags(arguments); err == nil {
			t.Fatalf("proxyCommandFlags(%q) unexpectedly succeeded", arguments)
		}
	}
}

func TestConfigBoundProxyRejectsDifferentConfigurationBeforeDAP(t *testing.T) {
	configPath := writeConfig(t)
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: otherTestConfigDigest, DAPAddress: "127.0.0.1:43123",
		Status:   func(context.Context) (control.Status, error) { return control.Status{}, nil },
		Shutdown: func() {},
		Proxy:    func(context.Context, net.Conn) error { called = true; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := store.Publish(server.Record()); err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.Serve(serveCtx) }()
	t.Cleanup(func() { cancel(); <-served })
	err = runProxy(context.Background(), []string{"--config", configPath, "--control-dir", controlDir}, strings.NewReader("dap"), io.Discard)
	if !errors.Is(err, control.ErrConfigurationMismatch) {
		t.Fatalf("proxy mismatch error = %v", err)
	}
	if called {
		t.Fatal("config-bound proxy contacted a differently bound daemon")
	}
}

func TestConfigBoundShutdownRejectsDifferentConfigurationBeforeControlCall(t *testing.T) {
	configPath := writeConfig(t)
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	shutdown := false
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: otherTestConfigDigest, DAPAddress: "127.0.0.1:43123",
		Status:   func(context.Context) (control.Status, error) { return control.Status{}, nil },
		Shutdown: func() { shutdown = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := store.Publish(server.Record()); err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.Serve(serveCtx) }()
	t.Cleanup(func() { cancel(); <-served })
	err = runShutdown(context.Background(), []string{"--config", configPath, "--control-dir", controlDir}, io.Discard)
	if !errors.Is(err, control.ErrConfigurationMismatch) {
		t.Fatalf("shutdown mismatch error = %v", err)
	}
	if shutdown {
		t.Fatal("config-bound shutdown contacted a differently bound daemon")
	}
}

func TestEnsureRejectsDifferentConfigurationWithoutDiscoveryOrLaunch(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: otherTestConfigDigest, DAPAddress: "127.0.0.1:43123",
		Status: func(context.Context) (control.Status, error) { return control.Status{}, nil }, Shutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := store.Publish(server.Record()); err != nil {
		t.Fatal(err)
	}
	previousDiscovery, previousLaunch := discoverWarmSession, launchEnsureServe
	t.Cleanup(func() { discoverWarmSession, launchEnsureServe = previousDiscovery, previousLaunch })
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		t.Fatal("ensure discovered a session for a differently bound daemon")
		return daemon.WarmSession{}, nil
	}
	launchEnsureServe = func(ensureServeRequest) (ensureChild, error) {
		t.Fatal("ensure launched a child for a differently bound daemon")
		return nil, nil
	}
	err = run(context.Background(), []string{"ensure", "--config", configPath, "--acquisition", "cold", "--bridge-script", bridgeScript, "--control-dir", controlDir}, io.Discard)
	if !errors.Is(err, control.ErrConfigurationMismatch) {
		t.Fatalf("ensure mismatch error = %v", err)
	}
}

func TestEnsureChildRejectsParentBindingMismatchBeforeRuntime(t *testing.T) {
	configPath := writeConfig(t)
	previousRuntime, previousConsole, previousOwnership := startRuntime, prepareEnsureConsole, acquireEnsureChild
	t.Cleanup(func() {
		startRuntime, prepareEnsureConsole, acquireEnsureChild = previousRuntime, previousConsole, previousOwnership
	})
	prepareEnsureConsole = func() error { return nil }
	acquireEnsureChild = func() error { return nil }
	startRuntime = func(context.Context, daemon.Options) (runtime, error) {
		t.Fatal("ensure child started runtime after parent binding mismatch")
		return nil, nil
	}
	err := run(context.Background(), []string{"serve", "--ensure-child", "--config", configPath, "--ensure-config-digest", testConfigDigest}, io.Discard)
	if !errors.Is(err, control.ErrConfigurationMismatch) {
		t.Fatalf("ensure child mismatch error = %v", err)
	}
}

func TestProxyByProbeIDUpgradesAuthenticatedControlSocket(t *testing.T) {
	controlDir := t.TempDir()
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: testConfigDigest,
		DAPAddress: "127.0.0.1:43123",
		Status: func(context.Context) (control.Status, error) {
			return control.Status{}, nil
		},
		Shutdown: func() {},
		Proxy: func(_ context.Context, connection net.Conn) error {
			defer connection.Close()
			payload, err := io.ReadAll(connection)
			if err != nil {
				return err
			}
			if string(payload) != "clion-dap-request" {
				return errors.New("unexpected proxy payload")
			}
			_, err = io.WriteString(connection, "clion-dap-response")
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(server.Record()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.RemoveIfInstance("test-probe", server.Record().InstanceID) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()

	var output bytes.Buffer
	if err := runProxy(ctx, []string{"--probe-id", "test-probe", "--control-dir", controlDir}, strings.NewReader("clion-dap-request"), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "clion-dap-response" {
		t.Fatalf("proxy output = %q", output.String())
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatalf("control Serve = %v", err)
	}
}

func TestCLionAttachProfileTraversesAuthenticatedStdioProxy(t *testing.T) {
	backend := &clionProxyBackend{}
	dapServer, err := dap.Listen("127.0.0.1:0", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer dapServer.Close()

	configPath := writeConfig(t)
	configDigest := bindingDigestForPath(t, configPath)
	controlDir := t.TempDir()
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: configDigest,
		DAPAddress: dapServer.Addr().String(),
		Status: func(context.Context) (control.Status, error) {
			return control.Status{}, nil
		},
		Shutdown: func() {},
		Proxy: func(ctx context.Context, connection net.Conn) error {
			return dapServer.ServeConnection(ctx, connection)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(server.Record()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.RemoveIfInstance("test-probe", server.Record().InstanceID) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()

	requests := []dap.Envelope{
		dapRequest(1, "initialize", `{
			"clientID":"clion",
			"clientName":"CLion",
			"adapterID":"clion.dap.debugger",
			"locale":"en",
			"pathFormat":"path",
			"linesStartAt1":true,
			"columnsStartAt1":true,
			"supportsVariablePaging":true,
			"supportsVariableType":true,
			"supportsRunInTerminalRequest":true
		}`),
		dapRequest(2, "attach", `{"request":"attach"}`),
		dapRequest(3, "configurationDone", `{}`),
		dapRequest(4, "threads", `{}`),
		dapRequest(5, "disconnect", `{"terminateDebuggee":false}`),
	}
	var input bytes.Buffer
	encoder := dap.NewEncoder(&input)
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := runProxy(ctx, []string{"--config", configPath, "--control-dir", controlDir}, &input, &output); err != nil {
		t.Fatal(err)
	}

	decoder := dap.NewDecoder(&output)
	wantMessages := []struct {
		messageType dap.MessageType
		name        string
	}{{dap.TypeResponse, "initialize"}, {dap.TypeEvent, "initialized"}, {dap.TypeResponse, "configurationDone"}, {dap.TypeResponse, "attach"}, {dap.TypeResponse, "threads"}, {dap.TypeResponse, "disconnect"}}
	for index, want := range wantMessages {
		message, err := decoder.Decode()
		if err != nil {
			t.Fatalf("decode CLion response %d: %v", index, err)
		}
		name := message.Command
		if message.Type == dap.TypeEvent {
			name = message.Event
		} else if message.Success == nil || !*message.Success {
			t.Fatalf("CLion response %d failed: %#v", index, message)
		}
		if message.Type != want.messageType || name != want.name {
			t.Fatalf("CLion response %d = (%q, %q), want (%q, %q)", index, message.Type, name, want.messageType, want.name)
		}
	}
	if _, err := decoder.Decode(); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing CLion response decode = %v, want EOF", err)
	}
	wantActions := []dap.ActionKind{dap.ActionAttach, dap.ActionConfigurationDone, dap.ActionThreads, dap.ActionDisconnect}
	if len(backend.actions) != len(wantActions) {
		t.Fatalf("backend actions = %#v, want %v", backend.actions, wantActions)
	}
	for index, want := range wantActions {
		if backend.actions[index].Kind != want {
			t.Fatalf("backend action %d = %v, want %v", index, backend.actions[index].Kind, want)
		}
	}
	initialize := backend.actions[0].Initialize
	if initialize.ClientID != "clion" || initialize.ClientName != "CLion" || initialize.AdapterID != "clion.dap.debugger" || !initialize.SupportsVariablePaging || !initialize.SupportsVariableType {
		t.Fatalf("CLion initialize arguments = %#v", initialize)
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatalf("control Serve = %v", err)
	}
}

type clionProxyBackend struct {
	actions []dap.Action
}

func (b *clionProxyBackend) Execute(_ context.Context, action dap.Action) (dap.ActionResult, error) {
	b.actions = append(b.actions, action)
	if action.Kind == dap.ActionThreads {
		return dap.ActionResult{Threads: []dap.Thread{{ID: 1, Name: "Core 0"}}}, nil
	}
	return dap.ActionResult{}, nil
}

func dapRequest(sequence int64, command, arguments string) dap.Envelope {
	return dap.Envelope{
		Seq: sequence, Type: dap.TypeRequest, Command: command,
		Arguments: json.RawMessage(arguments),
	}
}

func TestStatusAndShutdownUseLiveControlRecord(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	controlDir := t.TempDir()
	previous := startRuntime
	t.Cleanup(func() { startRuntime = previous })
	fake := newFakeRuntime()
	startRuntime = func(context.Context, daemon.Options) (runtime, error) { return fake, nil }
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	prior := control.TerminalRecord{
		Version: control.ProtocolVersion, ProbeID: "test-probe", ConfigDigest: bindingDigestForPath(t, configPath), InstanceID: strings.Repeat("A", 43), PID: 99,
		Phase: control.TerminalPhaseRuntimeServe, Classification: control.TerminalBridgeExit,
		OccurredAt: time.Date(2026, time.August, 20, 1, 2, 3, 0, time.UTC),
	}
	if err := store.WriteTerminal(prior); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- run(ctx, []string{"serve", "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", controlDir}, io.Discard)
	}()
	var record control.Record
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, err = store.Load("test-probe")
		if err == nil {
			break
		}
		select {
		case serveErr := <-serveDone:
			t.Fatalf("serve exited before publishing the control record: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("control record was not published: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	var status bytes.Buffer
	if err := run(context.Background(), []string{"status", "--config", configPath, "--control-dir", controlDir}, &status); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), `"target_state":"stopped"`) {
		t.Fatalf("status output = %q", status.String())
	}
	var shutdown bytes.Buffer
	if err := run(context.Background(), []string{"shutdown", "--config", configPath, "--control-dir", controlDir}, &shutdown); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shutdown.String(), "shutdown_requested") {
		t.Fatalf("shutdown output = %q", shutdown.String())
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("serve after shutdown: %v", err)
	}
	if !fake.closed {
		t.Fatal("shutdown did not close runtime")
	}
	terminal, err := store.LoadTerminal("test-probe")
	if err != nil {
		t.Fatal(err)
	}
	if terminal.PID != prior.PID || terminal.Classification != prior.Classification || !terminal.OccurredAt.Equal(prior.OccurredAt) {
		t.Fatalf("normal shutdown overwrote terminal record: %#v", terminal)
	}
	if err := store.RemoveIfEqual(record); err != nil {
		t.Fatal(err)
	}
}

func TestServeConfigurationFailureWritesNoReadyMessage(t *testing.T) {
	var output bytes.Buffer
	err := run(context.Background(), []string{"serve", "--config", "missing.toml"}, &output)
	if err == nil {
		t.Fatal("serve unexpectedly accepted a missing config")
	}
	if output.Len() != 0 {
		t.Fatalf("serve wrote stdout before startup: %q", output.String())
	}
}

func TestServeDoesNotStartRuntimeWhenControlRecordIsLive(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: bindingDigestForPath(t, configPath), DAPAddress: "127.0.0.1:41234",
		Status: func(context.Context) (control.Status, error) {
			return control.Status{ProbeID: "test-probe", PID: os.Getpid(), DAPAddress: "127.0.0.1:41234", TargetState: "stopped"}, nil
		},
		Shutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := store.Publish(server.Record()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.RemoveIfInstance("test-probe", server.Record().InstanceID) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	t.Cleanup(func() { <-served })
	if _, err := control.Call(context.Background(), server.Record(), control.MethodStatus); err != nil {
		t.Fatalf("control preflight server was not ready: %v", err)
	}

	previous := startRuntime
	t.Cleanup(func() { startRuntime = previous })
	started := false
	startRuntime = func(context.Context, daemon.Options) (runtime, error) {
		started = true
		return newFakeRuntime(), nil
	}
	err = run(context.Background(), []string{"serve", "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", controlDir}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already live") {
		t.Fatalf("serve error = %v", err)
	}
	if started {
		t.Fatal("control preflight started runtime before rejecting live record")
	}
}

func TestEnsureReusesAuthenticatedDaemonWithoutDiscovery(t *testing.T) {
	configPath := writeConfig(t)
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	var server *control.Server
	server, err = control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: bindingDigestForPath(t, configPath), DAPAddress: "127.0.0.1:41234",
		Status: func(context.Context) (control.Status, error) {
			return control.Status{ProbeID: "test-probe", PID: server.Record().PID, DAPAddress: "127.0.0.1:41234", TargetState: "stopped"}, nil
		}, Shutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := store.Publish(server.Record()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { <-done })
	if _, err := control.Call(context.Background(), server.Record(), control.MethodStatus); err != nil {
		t.Fatalf("control preflight: %v", err)
	}

	previousDiscovery, previousLaunch, previousConsole := discoverWarmSession, launchEnsureServe, prepareEnsureConsole
	prepareEnsureConsole = func() error { return nil }
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		t.Fatal("ensure discovered a router for an authenticated daemon")
		return daemon.WarmSession{}, nil
	}
	launchEnsureServe = func(ensureServeRequest) (ensureChild, error) {
		t.Fatal("ensure launched a child for an authenticated daemon")
		return nil, nil
	}
	t.Cleanup(func() {
		discoverWarmSession, launchEnsureServe, prepareEnsureConsole = previousDiscovery, previousLaunch, previousConsole
	})
	var output bytes.Buffer
	primary := filepath.Join(filepath.Dir(configPath), "core.elf")
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"ensure", "--config", configPath, "--primary-elf", primary, "--bridge-script", bridgeScript, "--control-dir", controlDir}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"event\":\"already_ready\",\"dap_address\":\"127.0.0.1:41234\",\"pid\":"+strconv.Itoa(server.Record().PID)+"}\n"; got != want {
		t.Fatalf("ensure output = %q, want %q", got, want)
	}
	output.Reset()
	if err := run(context.Background(), []string{
		"ensure", "--config", configPath, "--acquisition", "cold",
		"--bridge-script", bridgeScript, "--control-dir", controlDir,
	}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"event\":\"already_ready\",\"dap_address\":\"127.0.0.1:41234\",\"pid\":"+strconv.Itoa(server.Record().PID)+"}\n"; got != want {
		t.Fatalf("cold ensure output = %q, want %q", got, want)
	}
}

func TestEnsureAcquisitionOptionsAreExplicitAndMutuallyExclusive(t *testing.T) {
	configPath := writeConfig(t)
	primary := filepath.Join(filepath.Dir(configPath), "core.elf")

	mode, gotPrimary, err := ensureAcquisitionOptions("warm", primary)
	if err != nil || mode != ensureAcquisitionWarm || gotPrimary != primary {
		t.Fatalf("warm acquisition = (%q, %q, %v)", mode, gotPrimary, err)
	}
	mode, gotPrimary, err = ensureAcquisitionOptions("cold", "")
	if err != nil || mode != ensureAcquisitionCold || gotPrimary != "" {
		t.Fatalf("cold acquisition = (%q, %q, %v)", mode, gotPrimary, err)
	}
	if _, _, err := ensureAcquisitionOptions("cold", primary); err == nil || !strings.Contains(err.Error(), "--primary-elf requires --acquisition warm") {
		t.Fatalf("cold acquisition with primary ELF error = %v", err)
	}
	if _, _, err := ensureAcquisitionOptions("automatic", ""); err == nil || !strings.Contains(err.Error(), "--acquisition must be warm or cold") {
		t.Fatalf("unknown acquisition error = %v", err)
	}
}

func TestEnsureChildArgumentsKeepColdAndWarmAcquisitionSeparate(t *testing.T) {
	cold := ensureServeRequest{
		ConfigPath: "project.toml", BridgeScript: "bridge.py", ControlDir: "control", ConfigDigest: testConfigDigest,
		Acquisition: ensureAcquisitionCold,
	}
	args, err := cold.args()
	if err != nil {
		t.Fatal(err)
	}
	wantCold := []string{"serve", "--ensure-child", "--config", "project.toml", "--bridge-script", "bridge.py", "--ensure-config-digest", testConfigDigest, "--control-dir", "control", "--session-mode", "cold"}
	if strings.Join(args, "\x00") != strings.Join(wantCold, "\x00") {
		t.Fatalf("cold child arguments = %#v, want %#v", args, wantCold)
	}

	warm := cold
	warm.Acquisition = ensureAcquisitionWarm
	warm.Warm = &daemon.WarmSession{ServiceRouterHost: "127.0.0.1", ServiceRouterPort: 40123, PrimaryELF: `C:\fixture\core.elf`}
	args, err = warm.args()
	if err != nil {
		t.Fatal(err)
	}
	wantWarm := []string{
		"serve", "--ensure-child", "--config", "project.toml", "--bridge-script", "bridge.py", "--ensure-config-digest", testConfigDigest, "--control-dir", "control",
		"--session-mode", "warm", "--service-router-host", "127.0.0.1", "--service-router-port", "40123", "--primary-elf", `C:\fixture\core.elf`,
	}
	if strings.Join(args, "\x00") != strings.Join(wantWarm, "\x00") {
		t.Fatalf("warm child arguments = %#v, want %#v", args, wantWarm)
	}

	cold.Warm = warm.Warm
	if _, err := cold.args(); err == nil {
		t.Fatal("cold child arguments accepted warm session identity")
	}
	warm.Warm = nil
	if _, err := warm.args(); err == nil {
		t.Fatal("warm child arguments accepted missing session identity")
	}
}

func TestEnsureColdStartsOneChildWithoutWarmDiscovery(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}

	previousDiscovery, previousLaunch, previousConsole := discoverWarmSession, launchEnsureServe, prepareEnsureConsole
	t.Cleanup(func() {
		discoverWarmSession, launchEnsureServe, prepareEnsureConsole = previousDiscovery, previousLaunch, previousConsole
	})
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		t.Fatal("cold ensure attempted warm discovery")
		return daemon.WarmSession{}, nil
	}
	prepareEnsureConsole = func() error {
		t.Fatal("cold ensure prepared the parent process console")
		return nil
	}

	var server *control.Server
	var cancelServe context.CancelFunc
	var served chan error
	launches := 0
	launchEnsureServe = func(request ensureServeRequest) (ensureChild, error) {
		launches++
		if request.Acquisition != ensureAcquisitionCold || request.Warm != nil {
			t.Fatalf("cold launch request = %#v", request)
		}
		var serverErr error
		server, serverErr = control.NewServer(control.ServerOptions{
			ProbeID: "test-probe", ConfigDigest: request.ConfigDigest, DAPAddress: "127.0.0.1:41234",
			Status: func(context.Context) (control.Status, error) {
				return control.Status{ProbeID: "test-probe", PID: server.Record().PID, DAPAddress: "127.0.0.1:41234", TargetState: "stopped"}, nil
			},
			Shutdown: func() {},
		})
		if serverErr != nil {
			return nil, serverErr
		}
		var serveCtx context.Context
		serveCtx, cancelServe = context.WithCancel(context.Background())
		served = make(chan error, 1)
		go func() { served <- server.Serve(serveCtx) }()
		if err := store.Publish(server.Record()); err != nil {
			return nil, err
		}
		return &fakeEnsureChild{pid: server.Record().PID, done: make(chan error), stop: make(chan struct{})}, nil
	}
	t.Cleanup(func() {
		if cancelServe != nil {
			cancelServe()
			<-served
		}
		if server != nil {
			_ = server.Close()
		}
	})

	var output bytes.Buffer
	if err := run(context.Background(), []string{
		"ensure", "--config", configPath, "--acquisition", "cold",
		"--bridge-script", bridgeScript, "--control-dir", controlDir,
	}, &output); err != nil {
		t.Fatal(err)
	}
	if launches != 1 || !strings.Contains(output.String(), `"event":"ready"`) {
		t.Fatalf("cold ensure = launches %d, output %q", launches, output.String())
	}
}

func TestEnsureColdStartupFailureKillsOnlyLaunchedChild(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousDiscovery, previousLaunch := discoverWarmSession, launchEnsureServe
	t.Cleanup(func() { discoverWarmSession, launchEnsureServe = previousDiscovery, previousLaunch })
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		t.Fatal("cold ensure attempted warm discovery")
		return daemon.WarmSession{}, nil
	}
	child := &fakeEnsureChild{pid: 99123, done: make(chan error, 1), stop: make(chan struct{})}
	child.done <- errors.New("cold child failed")
	launchEnsureServe = func(request ensureServeRequest) (ensureChild, error) {
		if request.Acquisition != ensureAcquisitionCold || request.Warm != nil {
			t.Fatalf("cold launch request = %#v", request)
		}
		return child, nil
	}
	err := run(context.Background(), []string{
		"ensure", "--config", configPath, "--acquisition", "cold",
		"--bridge-script", bridgeScript, "--control-dir", t.TempDir(),
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cold child failed") {
		t.Fatalf("cold ensure failure = %v", err)
	}
	select {
	case <-child.stop:
	default:
		t.Fatal("cold ensure did not kill its failed child")
	}
}

func TestEnsureWaitsForAuthenticatedRecordAfterChildStart(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	previousDiscovery, previousLaunch, previousConsole := discoverWarmSession, launchEnsureServe, prepareEnsureConsole
	t.Cleanup(func() {
		discoverWarmSession, launchEnsureServe, prepareEnsureConsole = previousDiscovery, previousLaunch, previousConsole
	})
	prepareEnsureConsole = func() error { return nil }
	primary := filepath.Join(filepath.Dir(configPath), "core.elf")
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		return daemon.WarmSession{ServiceRouterHost: "127.0.0.1", ServiceRouterPort: 40123, PrimaryELF: primary}, nil
	}
	child := &fakeEnsureChild{pid: 99123, done: make(chan error), stop: make(chan struct{})}
	published := make(chan struct{})
	launchEnsureServe = func(request ensureServeRequest) (ensureChild, error) {
		if request.Acquisition != ensureAcquisitionWarm || request.Warm == nil || request.Warm.ServiceRouterPort != 40123 || request.Warm.PrimaryELF != primary {
			t.Fatalf("launch request = %#v", request)
		}
		go func() {
			server, serverErr := control.NewServer(control.ServerOptions{
				ProbeID: "test-probe", ConfigDigest: request.ConfigDigest, DAPAddress: "127.0.0.1:41234",
				Status: func(context.Context) (control.Status, error) {
					return control.Status{ProbeID: "test-probe", PID: os.Getpid(), DAPAddress: "127.0.0.1:41234", TargetState: "stopped"}, nil
				},
				Shutdown: func() {},
			})
			if serverErr != nil {
				t.Errorf("control server: %v", serverErr)
				return
			}
			child.pid = server.Record().PID
			defer server.Close()
			serveCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				if err := server.Serve(serveCtx); err != nil {
					t.Errorf("serve: %v", err)
				}
			}()
			if err := store.Publish(server.Record()); err != nil {
				t.Errorf("publish: %v", err)
				return
			}
			close(published)
			<-child.stop
			cancel()
		}()
		<-published
		return child, nil
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"ensure", "--config", configPath, "--primary-elf", primary, "--bridge-script", bridgeScript, "--control-dir", controlDir}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"event":"ready"`) {
		t.Fatalf("ensure output = %q", output.String())
	}
	close(child.stop)
}

func TestEnsureReadinessClearsOnlyAuthenticatedStaleRecord(t *testing.T) {
	store, err := control.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: testConfigDigest, DAPAddress: "127.0.0.1:41234",
		Status:   func(context.Context) (control.Status, error) { return control.Status{}, nil },
		Shutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := server.Record()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(record); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, found, _, err := waitForLiveDaemon(ctx, store, "test-probe", testConfigDigest, 0, nil); err != nil || found {
		t.Fatalf("waitForLiveDaemon() = (found=%v, err=%v)", found, err)
	}
	if _, err := store.Load("test-probe"); !errors.Is(err, control.ErrNoRecord) {
		t.Fatalf("stale record remains: %v", err)
	}
}

func TestConcurrentEnsureWaitersReturnReadyAndAlreadyReady(t *testing.T) {
	store, err := control.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.Reserve("test-probe", testConfigDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Cancel()

	var server *control.Server
	server, err = control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: testConfigDigest, DAPAddress: "127.0.0.1:41234",
		Status: func(context.Context) (control.Status, error) {
			return control.Status{ProbeID: "test-probe", PID: server.Record().PID, DAPAddress: "127.0.0.1:41234", TargetState: "stopped"}, nil
		},
		Shutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	served := make(chan error, 1)
	go func() { served <- server.Serve(serveCtx) }()
	t.Cleanup(func() { <-served })

	type result struct {
		status control.Status
		found  bool
		owned  bool
		err    error
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ownerResult := make(chan result, 1)
	contenderResult := make(chan result, 1)
	ownerChild := make(chan error)
	contenderChild := make(chan error)
	go func() {
		status, found, owned, err := waitForLiveDaemon(ctx, store, "test-probe", testConfigDigest, server.Record().PID, ownerChild)
		ownerResult <- result{status: status, found: found, owned: owned, err: err}
	}()
	go func() {
		status, found, owned, err := waitForLiveDaemon(ctx, store, "test-probe", testConfigDigest, server.Record().PID+1, contenderChild)
		contenderResult <- result{status: status, found: found, owned: owned, err: err}
	}()

	childExitSent := make(chan struct{})
	go func() {
		contenderChild <- errors.New("control: daemon is already starting")
		close(childExitSent)
	}()
	<-childExitSent
	if err := reservation.Commit(server.Record()); err != nil {
		t.Fatal(err)
	}

	owner := <-ownerResult
	contender := <-contenderResult
	if owner.err != nil || !owner.found || !owner.owned || owner.status.PID != server.Record().PID {
		t.Fatalf("owner result = %#v, error = %v", owner, owner.err)
	}
	if contender.err != nil || !contender.found || contender.owned || contender.status.PID != server.Record().PID {
		t.Fatalf("contender result = %#v, error = %v", contender, contender.err)
	}
}

func TestConcurrentEnsureCommandsBothSucceed(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(filepath.Dir(configPath), "core.elf")
	controlDir := t.TempDir()
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}

	previousDiscovery, previousLaunch, previousConsole := discoverWarmSession, launchEnsureServe, prepareEnsureConsole
	t.Cleanup(func() {
		discoverWarmSession, launchEnsureServe, prepareEnsureConsole = previousDiscovery, previousLaunch, previousConsole
	})
	prepareEnsureConsole = func() error { return nil }
	discoveryEntered := make(chan struct{}, 2)
	releaseDiscovery := make(chan struct{})
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		discoveryEntered <- struct{}{}
		<-releaseDiscovery
		return daemon.WarmSession{ServiceRouterHost: "127.0.0.1", ServiceRouterPort: 40123, PrimaryELF: primary}, nil
	}

	var server *control.Server
	server, err = control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: bindingDigestForPath(t, configPath), DAPAddress: "127.0.0.1:41234",
		Status: func(context.Context) (control.Status, error) {
			return control.Status{ProbeID: "test-probe", PID: server.Record().PID, DAPAddress: "127.0.0.1:41234", TargetState: "stopped"}, nil
		},
		Shutdown: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	served := make(chan error, 1)
	go func() { served <- server.Serve(serveCtx) }()
	t.Cleanup(func() { <-served })

	var launchMu sync.Mutex
	launchCount := 0
	var reservation *control.Reservation
	commitError := make(chan error, 1)
	launchEnsureServe = func(ensureServeRequest) (ensureChild, error) {
		launchMu.Lock()
		defer launchMu.Unlock()
		launchCount++
		if launchCount == 1 {
			var reserveErr error
			reservation, reserveErr = store.Reserve("test-probe", bindingDigestForPath(t, configPath))
			if reserveErr != nil {
				return nil, reserveErr
			}
			return &fakeEnsureChild{pid: server.Record().PID, done: make(chan error), stop: make(chan struct{})}, nil
		}
		if launchCount != 2 {
			return nil, errors.New("unexpected ensure launch")
		}
		childDone := make(chan error)
		exitConsumed := make(chan struct{})
		go func() {
			childDone <- errors.New("control: daemon is already starting")
			close(exitConsumed)
		}()
		go func() {
			<-exitConsumed
			commitError <- reservation.Commit(server.Record())
		}()
		return &fakeEnsureChild{pid: server.Record().PID + 1, done: childDone, stop: make(chan struct{})}, nil
	}

	arguments := []string{"ensure", "--config", configPath, "--primary-elf", primary, "--bridge-script", bridgeScript, "--control-dir", controlDir}
	type commandResult struct {
		output string
		err    error
	}
	results := make(chan commandResult, 2)
	for range 2 {
		go func() {
			var output bytes.Buffer
			err := run(context.Background(), arguments, &output)
			results <- commandResult{output: output.String(), err: err}
		}()
	}
	<-discoveryEntered
	<-discoveryEntered
	close(releaseDiscovery)

	events := make(map[string]int)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent ensure failed: %v", result.err)
		}
		var message ensureMessage
		if err := json.Unmarshal([]byte(result.output), &message); err != nil {
			t.Fatalf("decode ensure output %q: %v", result.output, err)
		}
		events[message.Event]++
	}
	if err := <-commitError; err != nil {
		t.Fatal(err)
	}
	if events["ready"] != 1 || events["already_ready"] != 1 {
		t.Fatalf("ensure events = %#v", events)
	}
}

func TestEnsureChildExitIsReturnedAfterDeadStartingOwnerRecovery(t *testing.T) {
	controlDir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestEnsureDeadStartingOwnerHelperProcess$")
	command.Env = append(os.Environ(), "GO_WANT_ENSURE_DEAD_OWNER=1", "GO_ENSURE_CONTROL_DIR="+controlDir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("starting-owner helper: %v: %s", err, output)
	}
	store, err := control.NewStore(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("test-probe"); !errors.Is(err, control.ErrRecordActive) {
		t.Fatalf("dead owner marker = %v, want active reservation", err)
	}
	childCause := errors.New("control: daemon is already starting")
	childDone := make(chan error, 1)
	childDone <- childCause
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, _, err = waitForLiveDaemon(ctx, store, "test-probe", testConfigDigest, 99123, childDone)
	if !errors.Is(err, childCause) {
		t.Fatalf("waitForLiveDaemon() error = %v, want child cause", err)
	}
	if _, err := store.Load("test-probe"); !errors.Is(err, control.ErrNoRecord) {
		t.Fatalf("dead owner marker remains: %v", err)
	}
}

func TestEnsureDeadStartingOwnerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_ENSURE_DEAD_OWNER") != "1" {
		return
	}
	store, err := control.NewStore(os.Getenv("GO_ENSURE_CONTROL_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve("test-probe", testConfigDigest); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureFailureKeepsDiagnosticOnOriginalStderr(t *testing.T) {
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(filepath.Dir(configPath), "core.elf")
	previousDiscovery, previousConsole, previousStderr := discoverWarmSession, prepareEnsureConsole, os.Stderr
	redirected, err := os.CreateTemp(t.TempDir(), "hidden-console")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		discoverWarmSession, prepareEnsureConsole, os.Stderr = previousDiscovery, previousConsole, previousStderr
		_ = redirected.Close()
	})
	prepareEnsureConsole = func() error { os.Stderr = redirected; return nil }
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		return daemon.WarmSession{}, errors.New("warm discovery failed")
	}
	var stderr bytes.Buffer
	err = runMain(context.Background(), []string{"ensure", "--config", configPath, "--primary-elf", primary, "--bridge-script", bridgeScript, "--control-dir", t.TempDir()}, strings.NewReader(""), io.Discard, &stderr)
	if err == nil || !strings.Contains(stderr.String(), "warm discovery failed") {
		t.Fatalf("runMain() = (%v, stderr=%q)", err, stderr.String())
	}
}

type fakeEnsureChild struct {
	pid  int
	done chan error
	stop chan struct{}
}

func (p *fakeEnsureChild) PID() int           { return p.pid }
func (p *fakeEnsureChild) Done() <-chan error { return p.done }
func (p *fakeEnsureChild) Kill() error {
	if p.stop == nil {
		p.stop = make(chan struct{})
	}
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	return nil
}

type fakeRuntime struct {
	closed   bool
	serveErr error
	terminal daemon.TerminalMetadata
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{} }

func (f *fakeRuntime) DAPAddress() string                                 { return "127.0.0.1:41234" }
func (f *fakeRuntime) ServeDAPConnection(context.Context, net.Conn) error { return nil }
func (f *fakeRuntime) Close()                                             { f.closed = true }
func (f *fakeRuntime) Snapshot(context.Context) (session.Snapshot, error) {
	return session.Snapshot{State: session.TargetStopped}, nil
}
func (f *fakeRuntime) BreakpointCounts(context.Context) (daemon.BreakpointCounts, error) {
	return daemon.BreakpointCounts{}, nil
}
func (f *fakeRuntime) TerminalMetadata() daemon.TerminalMetadata { return f.terminal }
func (f *fakeRuntime) Serve(ctx context.Context) error {
	if f.serveErr != nil {
		return f.serveErr
	}
	<-ctx.Done()
	return nil
}

func writeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	multiRoot := filepath.Join(dir, "multi")
	if err := os.Mkdir(multiRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(multiRoot, "mpythonrun.exe"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project.ghsmc", "core.elf"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "project.toml")
	contents := `
[multi]
installation = "multi"
executable = "multi/mpythonrun.exe"

[connection]
project = "project.ghsmc"
arguments = "target server arguments"

[[cores]]
id = 0
elf = "core.elf"

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
id = "test-probe"
`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}
