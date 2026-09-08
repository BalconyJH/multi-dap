// Command multi-dap exposes the local M1 daemon lifecycle.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/daemon"
	"github.com/Tacrolimus/multi-dap/internal/version"
)

const defaultBridgeScript = "bridge/bridge.py"

var executablePath = os.Executable
var acquireEnsureChild = acquireEnsureChildOwnership

type runtime interface {
	DAPAddress() string
	ServeDAPConnection(context.Context, net.Conn) error
	Serve(context.Context) error
	Close()
	Snapshot(context.Context) (session.Snapshot, error)
	BreakpointCounts(context.Context) (daemon.BreakpointCounts, error)
}

type runtimeStarter func(context.Context, daemon.Options) (runtime, error)

var startRuntime runtimeStarter = func(ctx context.Context, options daemon.Options) (runtime, error) {
	return daemon.Start(ctx, options)
}

type readyMessage struct {
	Event      string `json:"event"`
	DAPAddress string `json:"dap_address"`
	PID        int    `json:"pid"`
}

type checkMessage struct {
	Event string `json:"event"`
	OK    bool   `json:"ok"`
}

type terminalMessage struct {
	Event          string                         `json:"event"`
	Found          bool                           `json:"found"`
	ProbeID        string                         `json:"probe_id,omitempty"`
	PID            int                            `json:"pid,omitempty"`
	Phase          control.TerminalPhase          `json:"phase,omitempty"`
	Classification control.TerminalClassification `json:"classification,omitempty"`
	Operation      control.TerminalOperation      `json:"operation,omitempty"`
	OccurredAt     *time.Time                     `json:"occurred_at,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stdin, stdout, stderr := os.Stdin, os.Stdout, os.Stderr
	if err := runMain(ctx, os.Args[1:], stdin, stdout, stderr); err != nil {
		os.Exit(1)
	}
}

// runMain keeps diagnostics on the caller-owned stderr even if a command
// installs a private console for a child process.
func runMain(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	err := runWithIO(ctx, args, stdin, stdout)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "multi-dap:", err)
	}
	return err
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	return runWithIO(ctx, args, os.Stdin, stdout)
}

func runWithIO(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return usageError("command is required")
	}
	switch args[0] {
	case "version", "--version":
		if len(args) != 1 {
			return usageError("version accepts no arguments")
		}
		return json.NewEncoder(stdout).Encode(version.Current())
	case "serve":
		return runServe(ctx, args[1:], stdout)
	case "ensure", "start":
		return runEnsure(ctx, args[1:], stdout)
	case "status":
		return runStatus(ctx, args[1:], stdout)
	case "shutdown":
		return runShutdown(ctx, args[1:], stdout)
	case "proxy":
		return runProxy(ctx, args[1:], stdin, stdout)
	case "doctor", "check":
		return runCheck(args[1:], stdout)
	case "diagnose":
		return runDiagnose(args[1:], stdout)
	case "help", "-h", "--help":
		_, err := io.WriteString(stdout, usage())
		return err
	default:
		return usageError(fmt.Sprintf("unknown command %q", args[0]))
	}
}

func runServe(ctx context.Context, args []string, stdout io.Writer) (runErr error) {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "project TOML configuration")
	bridgeScript := flags.String("bridge-script", "", "bridge.py path")
	controlDir := flags.String("control-dir", "", "private daemon control record directory")
	sessionMode := flags.String("session-mode", "cold", "session acquisition mode: cold or warm")
	serviceRouterHost := flags.String("service-router-host", "", "warm session service-router loopback IP")
	serviceRouterPort := flags.Int("service-router-port", 0, "warm session service-router port")
	primaryELF := flags.String("primary-elf", "", "warm session primary ELF identity")
	ensureChild := flags.Bool("ensure-child", false, "internal ensure child marker")
	ensureConfigDigest := flags.String("ensure-config-digest", "", "internal expected ensure configuration binding")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError("serve accepts no positional arguments")
	}
	if !*ensureChild && strings.TrimSpace(*ensureConfigDigest) != "" {
		return usageError("--ensure-config-digest is only valid with --ensure-child")
	}
	if *ensureChild {
		if err := prepareEnsureConsole(); err != nil {
			return err
		}
		if err := acquireEnsureChild(); err != nil {
			return err
		}
	}
	validated, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	digest, err := config.BindingDigest(validated)
	if err != nil {
		return fmt.Errorf("configuration binding: %w", err)
	}
	if *ensureChild && (strings.TrimSpace(*ensureConfigDigest) == "" || *ensureConfigDigest != digest) {
		return control.ErrConfigurationMismatch
	}
	script, err := resolveBridgeScript(*bridgeScript)
	if err != nil {
		return fmt.Errorf("bridge script: %w", err)
	}
	warm, err := warmSessionOptions(*sessionMode, *serviceRouterHost, *serviceRouterPort, *primaryELF)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	store, err := control.NewStore(*controlDir)
	if err != nil {
		return err
	}
	reservation, err := reserveRecord(ctx, store, validated.Probe.ID, digest)
	if err != nil {
		return err
	}
	defer func() { _ = reservation.Cancel() }()

	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	var runtime runtime
	var shutdownRequested atomic.Bool
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: validated.Probe.ID, ConfigDigest: digest,
		Status: func(statusCtx context.Context) (control.Status, error) {
			if runtime == nil {
				return control.Status{}, errors.New("control: daemon is still starting")
			}
			snapshot, err := runtime.Snapshot(statusCtx)
			if err != nil {
				return control.Status{}, err
			}
			breakpoints, err := runtime.BreakpointCounts(statusCtx)
			if err != nil {
				return control.Status{}, err
			}
			return control.Status{
				ProbeID: validated.Probe.ID, PID: os.Getpid(), DAPAddress: runtime.DAPAddress(),
				TargetState: targetStateName(snapshot.State), ExecutionEpoch: uint64(snapshot.ExecutionEpoch), StopEpoch: uint64(snapshot.StopEpoch),
				DAPOwned: breakpoints.DAPOwned, Pending: breakpoints.Pending, Orphaned: breakpoints.Orphaned,
			}, nil
		},
		Shutdown: func() {
			shutdownRequested.Store(true)
			stopServe()
			if runtime != nil {
				runtime.Close()
			}
		},
		Proxy: func(proxyCtx context.Context, connection net.Conn) error {
			if runtime == nil {
				return errors.New("control: daemon is still starting")
			}
			return runtime.ServeDAPConnection(proxyCtx, connection)
		},
	})
	if err != nil {
		return err
	}
	defer server.Close()
	record := server.Record()
	terminal := control.TerminalRecord{
		Version: control.ProtocolVersion, ProbeID: record.ProbeID, ConfigDigest: digest, InstanceID: record.InstanceID, PID: record.PID,
		Phase: control.TerminalPhaseRuntimeStart,
	}
	defer func() {
		if runErr == nil {
			return
		}
		terminal.Classification = classifyTerminal(runErr)
		if reporter, ok := runtime.(interface {
			TerminalMetadata() daemon.TerminalMetadata
		}); ok {
			terminal.Operation = classifyTerminalOperation(reporter.TerminalMetadata())
		}
		terminal.OccurredAt = time.Now().UTC()
		if err := store.WriteTerminal(terminal); err != nil {
			runErr = fmt.Errorf("%w (control: write terminal diagnostic: %v)", runErr, err)
		}
	}()
	startCtx, cancel := context.WithTimeout(ctx, validated.Timing.StartupDeadline)
	defer cancel()
	runtime, err = startRuntime(startCtx, daemon.Options{Config: validated, BridgeScript: script, Warm: warm})
	if err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	defer runtime.Close()
	if err := server.SetDAPAddress(runtime.DAPAddress()); err != nil {
		return err
	}
	record = server.Record()
	// Reserve has already exercised exclusive creation, ACL application, and a
	// complete marker write before cold Open. Commit must remain after Start
	// because a configured DAP port of zero is selected inside Runtime. Thus an
	// exceptional post-Open filesystem failure is still possible; the defers
	// below release only this process's listener/bridge resources and never
	// issue a MULTI disconnect (which would be unsafe for warm sessions).
	if err := reservation.Commit(record); err != nil {
		return err
	}
	terminal.Phase = control.TerminalPhaseRuntimeServe
	defer func() { _ = store.RemoveIfInstance(record.ProbeID, record.InstanceID) }()
	controlErrors := make(chan error, 1)
	go func() { controlErrors <- server.Serve(serveCtx) }()

	if err := json.NewEncoder(stdout).Encode(readyMessage{
		Event: "ready", DAPAddress: runtime.DAPAddress(), PID: os.Getpid(),
	}); err != nil {
		return fmt.Errorf("write ready message: %w", err)
	}
	runtimeErrors := make(chan error, 1)
	go func() { runtimeErrors <- runtime.Serve(serveCtx) }()
	var runtimeErr, controlErr error
	select {
	case runtimeErr = <-runtimeErrors:
		stopServe()
		controlErr = <-controlErrors
	case controlErr = <-controlErrors:
		// Losing the authenticated control listener makes the published
		// daemon record unusable. Converge the runtime too instead of leaving
		// a DAP daemon that can no longer be inspected or shut down.
		if controlErr == nil && !shutdownRequested.Load() && serveCtx.Err() == nil {
			controlErr = errors.New("control server stopped unexpectedly")
		}
		stopServe()
		runtime.Close()
		runtimeErr = <-runtimeErrors
	}
	if controlErr != nil {
		return controlErr
	}
	if shutdownRequested.Load() && errors.Is(runtimeErr, daemon.ErrRuntimeClosed) {
		return nil
	}
	return runtimeErr
}

func classifyTerminal(err error) control.TerminalClassification {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return control.TerminalContextDeadline
	case errors.Is(err, daemon.ErrBridgeExited):
		return control.TerminalBridgeExit
	case errors.Is(err, daemon.ErrActorFaulted):
		return control.TerminalActorFault
	case errors.Is(err, daemon.ErrRuntimeClosed):
		return control.TerminalRuntimeClosed
	default:
		return control.TerminalOther
	}
}

func classifyTerminalOperation(metadata daemon.TerminalMetadata) control.TerminalOperation {
	if !metadata.HasActorOperation {
		return ""
	}
	switch metadata.ActorOperation {
	case actor.OperationUnknown:
		return control.TerminalOperationUnknown
	case actor.OperationOpen:
		return control.TerminalOperationOpen
	case actor.OperationCores:
		return control.TerminalOperationCores
	case actor.OperationState:
		return control.TerminalOperationState
	case actor.OperationExecution:
		return control.TerminalOperationExecution
	case actor.OperationBreakpoints:
		return control.TerminalOperationBreakpoints
	case actor.OperationBreakpointCleanup:
		return control.TerminalOperationBreakpointCleanup
	case actor.OperationInspection:
		return control.TerminalOperationInspection
	case actor.OperationRollbackClose:
		return control.TerminalOperationRollbackClose
	default:
		return control.TerminalOperationUnknown
	}
}

func warmSessionOptions(mode, host string, port int, primaryELF string) (*daemon.WarmSession, error) {
	mode = strings.TrimSpace(mode)
	host = strings.TrimSpace(host)
	primaryELF = strings.TrimSpace(primaryELF)
	switch mode {
	case "cold":
		if host != "" || port != 0 || primaryELF != "" {
			return nil, usageError("warm session flags require --session-mode warm")
		}
		return nil, nil
	case "warm":
		if host == "" {
			host = "127.0.0.1"
		}
		if port < 1 || port > 65535 {
			return nil, usageError("--service-router-port must be in 1..65535 for a warm session")
		}
		path, err := absoluteRegularFile(primaryELF)
		if err != nil {
			return nil, fmt.Errorf("primary ELF: %w", err)
		}
		return &daemon.WarmSession{
			ServiceRouterHost: host,
			ServiceRouterPort: port,
			PrimaryELF:        path,
		}, nil
	default:
		return nil, usageError("--session-mode must be cold or warm")
	}
}

func reserveRecord(ctx context.Context, store *control.Store, probeID, digest string) (*control.Reservation, error) {
	for {
		reservation, err := store.Reserve(probeID, digest)
		if !errors.Is(err, control.ErrRecordActive) {
			return reservation, err
		}
		current, err := store.LoadForConfig(probeID, digest)
		if errors.Is(err, control.ErrRecordActive) {
			recovered, recoveryErr := store.RecoverStarting(probeID, digest)
			if recoveryErr != nil {
				return nil, recoveryErr
			}
			if recovered {
				continue
			}
			return nil, fmt.Errorf("control: daemon for probe %q is already starting", probeID)
		}
		if err != nil {
			return nil, fmt.Errorf("control: inspect existing daemon record: %w", err)
		}
		if _, err := control.Call(ctx, current, control.MethodStatus); err == nil {
			return nil, fmt.Errorf("control: daemon for probe %q is already live", probeID)
		} else if !errors.Is(err, control.ErrStaleRecord) {
			return nil, err
		}
		if err := store.RemoveIfEqual(current); err != nil {
			return nil, err
		}
	}
}

func runStatus(ctx context.Context, args []string, stdout io.Writer) error {
	validated, digest, store, err := controlCommandFlags("status", args)
	if err != nil {
		return err
	}
	record, err := store.LoadForConfig(validated.Probe.ID, digest)
	if err != nil {
		return err
	}
	status, err := control.Call(ctx, record, control.MethodStatus)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(status); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

func runShutdown(ctx context.Context, args []string, stdout io.Writer) error {
	validated, digest, store, err := controlCommandFlags("shutdown", args)
	if err != nil {
		return err
	}
	record, err := store.LoadForConfig(validated.Probe.ID, digest)
	if err != nil {
		return err
	}
	if _, err := control.Call(ctx, record, control.MethodShutdown); err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(struct {
		Event   string `json:"event"`
		ProbeID string `json:"probe_id"`
	}{Event: "shutdown_requested", ProbeID: validated.Probe.ID}); err != nil {
		return fmt.Errorf("write shutdown result: %w", err)
	}
	return nil
}

func runProxy(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	target, err := proxyCommandFlags(args)
	if err != nil {
		return err
	}
	var record control.Record
	if target.legacy {
		record, err = target.store.Load(target.probeID)
	} else {
		record, err = target.store.LoadForConfig(target.probeID, target.digest)
	}
	if err != nil {
		return err
	}
	return control.Proxy(ctx, record, stdin, stdout)
}

type proxyTarget struct {
	probeID string
	digest  string
	legacy  bool
	store   *control.Store
}

func proxyCommandFlags(args []string) (proxyTarget, error) {
	flags := flag.NewFlagSet("proxy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "project TOML configuration")
	probeID := flags.String("probe-id", "", "legacy unbound stable probe identity")
	controlDir := flags.String("control-dir", "", "private daemon control record directory")
	if err := flags.Parse(args); err != nil {
		return proxyTarget{}, usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return proxyTarget{}, usageError("proxy accepts no positional arguments")
	}
	if strings.TrimSpace(*configPath) != "" && strings.TrimSpace(*probeID) != "" {
		return proxyTarget{}, usageError("proxy accepts exactly one of --config or --probe-id")
	}
	target := proxyTarget{legacy: strings.TrimSpace(*configPath) == ""}
	if strings.TrimSpace(*configPath) != "" {
		validated, err := loadConfig(*configPath)
		if err != nil {
			return proxyTarget{}, err
		}
		digest, err := config.BindingDigest(validated)
		if err != nil {
			return proxyTarget{}, fmt.Errorf("configuration binding: %w", err)
		}
		target.probeID, target.digest = validated.Probe.ID, digest
	} else {
		var err error
		target.probeID, err = config.ValidateProbeID(*probeID)
		if err != nil {
			return proxyTarget{}, usageError("--probe-id " + err.Error())
		}
	}
	store, err := control.NewStore(*controlDir)
	if err != nil {
		return proxyTarget{}, err
	}
	target.store = store
	return target, nil
}

func controlCommandFlags(name string, args []string) (config.Validated, string, *control.Store, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "project TOML configuration")
	controlDir := flags.String("control-dir", "", "private daemon control record directory")
	if err := flags.Parse(args); err != nil {
		return config.Validated{}, "", nil, usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return config.Validated{}, "", nil, usageError(name + " accepts no positional arguments")
	}
	validated, err := loadConfig(*configPath)
	if err != nil {
		return config.Validated{}, "", nil, err
	}
	digest, err := config.BindingDigest(validated)
	if err != nil {
		return config.Validated{}, "", nil, fmt.Errorf("configuration binding: %w", err)
	}
	store, err := control.NewStore(*controlDir)
	if err != nil {
		return config.Validated{}, "", nil, err
	}
	return validated, digest, store, nil
}

func targetStateName(state session.TargetState) string {
	switch state {
	case session.TargetStopped:
		return "stopped"
	case session.TargetRunning:
		return "running"
	case session.TargetFaulted:
		return "faulted"
	default:
		return "unknown"
	}
}

func runCheck(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "project TOML configuration")
	if err := flags.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if flags.NArg() != 0 {
		return usageError("doctor accepts no positional arguments")
	}
	if _, err := loadConfig(*configPath); err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(checkMessage{Event: "config_checked", OK: true}); err != nil {
		return fmt.Errorf("write check message: %w", err)
	}
	return nil
}

func runDiagnose(args []string, stdout io.Writer) error {
	validated, digest, store, err := controlCommandFlags("diagnose", args)
	if err != nil {
		return err
	}
	terminal, err := store.LoadTerminalForConfig(validated.Probe.ID, digest)
	if errors.Is(err, control.ErrNoTerminalRecord) {
		return json.NewEncoder(stdout).Encode(terminalMessage{Event: "terminal_diagnostic", Found: false})
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(terminalMessage{
		Event: "terminal_diagnostic", Found: true, ProbeID: terminal.ProbeID, PID: terminal.PID,
		Phase: terminal.Phase, Classification: terminal.Classification, Operation: terminal.Operation, OccurredAt: &terminal.OccurredAt,
	})
}

func loadConfig(path string) (config.Validated, error) {
	if strings.TrimSpace(path) == "" {
		return config.Validated{}, usageError("--config is required")
	}
	validated, err := config.Load(path)
	if err != nil {
		return config.Validated{}, err
	}
	return validated, nil
}

func absoluteRegularFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("must be a regular file")
	}
	return filepath.Clean(abs), nil
}

// resolveBridgeScript keeps explicit paths unchanged while allowing installed
// archives to keep their bridge beside the executable. It deliberately does
// not search the working directory: a project checkout must not be able to
// replace executable-adjacent code implicitly.
func resolveBridgeScript(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return absoluteRegularFile(path)
	}
	executable, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	if strings.TrimSpace(executable) == "" {
		return "", errors.New("resolve executable: path is empty")
	}
	script, err := absoluteRegularFile(filepath.Join(filepath.Dir(executable), defaultBridgeScript))
	if err != nil {
		return "", fmt.Errorf("not found beside the executable; pass --bridge-script explicitly: %w", err)
	}
	return script, nil
}

type usageError string

func (e usageError) Error() string { return string(e) + "\n" + usage() }

func usage() string {
	return "usage:\n  multi-dap version|--version\n  multi-dap serve --config <project.toml> [--bridge-script <bridge.py>] [--control-dir <path>]\n    [--session-mode cold|warm] [--service-router-host <loopback-ip> --service-router-port <port> --primary-elf <path>]\n  multi-dap ensure --config <project.toml> [--acquisition warm --primary-elf <path> | --acquisition cold]\n    [--bridge-script <bridge.py>] [--control-dir <path>]\n  multi-dap status|shutdown|diagnose --config <project.toml> [--control-dir <path>]\n  multi-dap proxy (--config <project.toml> | --probe-id <id>) [--control-dir <path>]\n  multi-dap doctor|check --config <project.toml>\n"
}
