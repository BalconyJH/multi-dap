package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/bridge"
	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/core/actor"
	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/multi"
)

var (
	// ErrBridgeExited marks loss of the one bridge process owned by a Runtime.
	// It is terminal for that runtime: reconnecting an arbitrary new bridge
	// would make its relation to the existing MULTI window ambiguous.
	ErrBridgeExited  = errors.New("daemon: bridge process exited")
	ErrActorFaulted  = errors.New("daemon: session actor faulted")
	ErrServing       = errors.New("daemon: runtime is already serving")
	ErrRuntimeClosed = errors.New("daemon: runtime is closed")
)

// WarmSession is the complete identity required to bind one existing MULTI
// program window. The service-router coordinates are runtime state and must
// never be persisted in the project configuration.
type WarmSession struct {
	ServiceRouterHost string
	ServiceRouterPort int
	PrimaryELF        string
}

// Options is the complete daemon startup input. Config must already be
// validated by config.Load; BridgeScript is deliberately separate because it
// identifies this daemon's bundled mechanism rather than target configuration.
type Options struct {
	Config       config.Validated
	BridgeScript string
	// Warm is nil for a cold session. Non-nil selects strict existing-window
	// binding and is validated as one atomic input before any process starts.
	Warm *WarmSession
}

// bridgeProcess is intentionally the small ownership contract used by the
// runtime. bridge.Process is its production implementation. Keeping this
// private seam makes runtime rollback tests use a real MBP client without
// copying the launcher into this package.
type bridgeProcess interface {
	Client() *bridge.Client
	Done() <-chan struct{}
	Wait() error
	Close() error
}

type bridgeStarter func(context.Context, bridge.LaunchSpec) (bridgeProcess, error)

// Runtime owns one bridge child, one Session Actor, and one loopback-only DAP
// server. It does not own MULTI, its service router, or the physical target.
type Runtime struct {
	config  config.Validated
	process bridgeProcess
	lock    probeLock
	actor   *actor.Actor
	backend *Backend
	dap     *dap.Server

	closeOnce sync.Once
	doneOnce  sync.Once
	closeDone chan struct{}
	failOnce  sync.Once

	mu          sync.Mutex
	serving     bool
	terminalErr error
	terminal    TerminalMetadata
	errors      chan error
}

// BreakpointCounts is the daemon-facing aggregate lifecycle observation.
// It intentionally aliases the actor value so no physical MULTI identity
// crosses the daemon boundary.
type BreakpointCounts = actor.BreakpointCounts

// TerminalMetadata is the bounded, frontend-neutral evidence associated with
// the first terminal runtime failure. ActorOperation is present only when an
// actor fault won the terminal race.
type TerminalMetadata struct {
	ActorOperation    actor.Operation
	HasActorOperation bool
}

// Start launches the private bridge, opens the configured MULTI project
// through the actor, and binds the DAP endpoint. Every partially constructed
// layer is unwound in reverse order. In particular it never issues MULTI's
// close command and never touches a router or target-server process.
func Start(ctx context.Context, options Options) (*Runtime, error) {
	return startWith(ctx, options, func(ctx context.Context, spec bridge.LaunchSpec) (bridgeProcess, error) {
		return bridge.Start(ctx, spec)
	})
}

func startWith(ctx context.Context, options Options, start bridgeStarter) (*Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if start == nil {
		return nil, errors.New("daemon: bridge starter is required")
	}
	if options.Config.Probe.ID == "" {
		return nil, errors.New("daemon: probe ID is required")
	}
	script, err := regularBridgeScript(options.BridgeScript)
	if err != nil {
		return nil, err
	}
	launchMode, router, openRequest, err := sessionAcquisition(options)
	if err != nil {
		return nil, err
	}
	sourcePaths, sourceIndex, err := runtimeSourceModel(options.Config)
	if err != nil {
		return nil, err
	}
	lock, err := acquireProbeLock(options.Config.Probe.ID)
	if err != nil {
		return nil, err
	}
	lockOwned := true
	defer func() {
		if lockOwned {
			_ = lock.Close()
		}
	}()
	dapListener, err := reserveDAPListener(options.Config.Endpoints.DAP)
	if err != nil {
		return nil, err
	}
	dapListenerOwned := true
	defer func() {
		if dapListenerOwned {
			_ = dapListener.Close()
		}
	}()
	startup, cancel := context.WithTimeout(ctx, options.Config.Timing.StartupDeadline)
	defer cancel()
	process, err := start(startup, bridge.LaunchSpec{
		Executable:    options.Config.Multi.Executable,
		BridgeScript:  script,
		RPCPort:       options.Config.Endpoints.MBP.Port,
		RPCHost:       options.Config.Endpoints.MBP.Host,
		ServiceRouter: router,
		SessionMode:   launchMode,
	})
	if err != nil {
		return nil, fmt.Errorf("daemon: start bridge: %w", err)
	}
	if process == nil || process.Client() == nil {
		if process != nil {
			_ = process.Close()
		}
		return nil, errors.New("daemon: bridge starter returned no MBP client")
	}

	executor := bridge.NewExecutor(process.Client())
	coreSpecs := make([]multi.CoreSpec, 0, len(options.Config.Cores))
	for _, core := range options.Config.Cores {
		coreSpecs = append(coreSpecs, multi.CoreSpec{ID: core.ID, ELF: core.ELF})
	}
	sourceResolver, err := multi.NewDeferredSourceResolver(coreSpecs, multi.NewSourceListProbe())
	if err != nil {
		executor.Close()
		_ = process.Close()
		return nil, fmt.Errorf("daemon: create source resolver: %w", err)
	}
	core, err := actor.New(actor.Options{
		Executor:  executor,
		Open:      openRequest,
		CoreSpecs: coreSpecs,
		Session: session.Options{
			RequireResetAfterDownload: options.Config.Lifecycle.RequireResetAfterDownload,
		},
		PollInterval:      options.Config.Timing.PollCadence,
		RPCDeadline:       options.Config.Timing.RPCDeadline,
		BootstrapDeadline: options.Config.Timing.StartupDeadline,
		BreakpointRunner:  &actor.MULTIBreakpointRunner{},
		SourceIndex:       sourceIndex,
		SourceResolver:    sourceResolver,
		Inspection:        multi.NewInspectionRunner(),
		M5:                multi.NewM5Runner(),
		SourceMap:         sourcePaths,
		ConsoleEnabled:    true,
		ConsoleInterval:   time.Second,
	})
	if err != nil {
		executor.Close()
		_ = process.Close()
		return nil, fmt.Errorf("daemon: create session actor: %w", err)
	}
	actorOwned := true
	defer func() {
		if actorOwned {
			core.Close()
			_ = process.Close()
		}
	}()
	var defaultInspectionCore *uint64
	if options.Config.Inspection.HasDefaultCore {
		value := uint64(options.Config.Inspection.DefaultCore)
		defaultInspectionCore = &value
	}
	backend, err := NewBackendWithOptions(core, BackendOptions{
		SourcePaths:           sourcePaths,
		DefaultInspectionCore: defaultInspectionCore,
		Capabilities: dap.Capabilities{
			SupportsReadMemoryRequest:        true,
			SupportsDisassembleRequest:       true,
			SupportsDelayedStackTraceLoading: true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("daemon: create DAP backend: %w", err)
	}
	backendOwned := true
	defer func() {
		if backendOwned {
			backend.Close()
		}
	}()
	server, err := dap.NewServer(dapListener, backend)
	if err != nil {
		return nil, fmt.Errorf("daemon: create DAP server: %w", err)
	}
	dapListenerOwned = false
	serverOwned := true
	defer func() {
		if serverOwned {
			_ = server.Close()
		}
	}()
	if err := backend.BindPublisher(server); err != nil {
		return nil, fmt.Errorf("daemon: bind DAP publisher: %w", err)
	}
	if err := core.Open(startup); err != nil {
		return nil, fmt.Errorf("daemon: open MULTI session: %w", err)
	}

	runtime := &Runtime{
		config: options.Config, process: process, lock: lock, actor: core, backend: backend, dap: server,
		closeDone: make(chan struct{}), errors: make(chan error, 16),
	}
	actorOwned = false
	backendOwned = false
	serverOwned = false
	lockOwned = false
	go runtime.monitorBridge()
	go runtime.monitorActor()
	go runtime.forwardDAPErrors()
	return runtime, nil
}

func sessionAcquisition(options Options) (bridge.SessionMode, *bridge.ServiceRouter, multi.OpenRequest, error) {
	if options.Warm == nil {
		preparation, err := coldPreparation(options.Config.Connection.Preparation)
		if err != nil {
			return "", nil, multi.OpenRequest{}, err
		}
		return bridge.SessionModeCold, nil, multi.OpenRequest{
			Mode:        multi.OpenModeCold,
			Project:     options.Config.Connection.Project,
			Connection:  options.Config.Connection.Arguments,
			Preparation: preparation,
		}, nil
	}
	if options.Config.Connection.Preparation != config.ConnectionPreparationNone {
		return "", nil, multi.OpenRequest{}, errors.New("daemon: warm session does not accept cold connection preparation")
	}

	host := strings.TrimSpace(options.Warm.ServiceRouterHost)
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsLoopback() {
		return "", nil, multi.OpenRequest{}, errors.New("daemon: warm service-router host must be a loopback IP literal")
	}
	port := options.Warm.ServiceRouterPort
	if port < 1 || port > 65535 {
		return "", nil, multi.OpenRequest{}, errors.New("daemon: warm service-router port must be in 1..65535")
	}
	primaryELF, err := regularFile(options.Warm.PrimaryELF, "warm primary ELF")
	if err != nil {
		return "", nil, multi.OpenRequest{}, err
	}
	configured := false
	for _, core := range options.Config.Cores {
		if strings.EqualFold(filepath.Clean(core.ELF), primaryELF) {
			configured = true
			break
		}
	}
	if !configured {
		return "", nil, multi.OpenRequest{}, errors.New("daemon: warm primary ELF must exactly match a configured core ELF")
	}
	router := &bridge.ServiceRouter{Host: address.String(), Port: port}
	return bridge.SessionModeWarm, router, multi.OpenRequest{
		Mode:       multi.OpenModeWarm,
		PrimaryELF: primaryELF,
	}, nil
}

func runtimeSourceModel(cfg config.Validated) (SourcePaths, *source.Index, error) {
	rewrites := make([]SourceRewrite, 0, len(cfg.SourceRewrites))
	for _, rewrite := range cfg.SourceRewrites {
		rewrites = append(rewrites, SourceRewrite{DebugPrefix: rewrite.From, ClientPrefix: rewrite.To})
	}
	paths, err := NewSourcePaths(rewrites)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: configure source paths: %w", err)
	}
	coreELFs := make(map[int]string, len(cfg.Cores))
	for _, core := range cfg.Cores {
		coreELFs[core.ID] = core.ELF
	}
	index, err := source.BuildIndex(coreELFs)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: build source index: %w", err)
	}
	return paths, index, nil
}

func regularBridgeScript(path string) (string, error) {
	return regularFile(path, "bridge script")
}

func regularFile(path, label string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("daemon: %s is required", label)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("daemon: resolve %s: %w", label, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("daemon: stat %s %q: %w", label, abs, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("daemon: %s %q must be a regular file", label, abs)
	}
	return abs, nil
}

func endpointAddress(endpoint config.Endpoint) string {
	return net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
}

// reserveDAPListener claims the DAP endpoint before any cold MULTI Open. The
// runtime's Config is validated input, but retaining the loopback check here
// keeps this staging boundary safe for direct callers and test fixtures.
func reserveDAPListener(endpoint config.Endpoint) (net.Listener, error) {
	host := strings.TrimSpace(endpoint.Host)
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsLoopback() {
		return nil, fmt.Errorf("daemon: DAP host %q must be a loopback IP literal", endpoint.Host)
	}
	listener, err := net.Listen("tcp", endpointAddress(endpoint))
	if err != nil {
		return nil, fmt.Errorf("daemon: listen DAP: %w", err)
	}
	return listener, nil
}

// DAPAddress returns the effective DAP listener address. It reports a numeric
// loopback address, including the kernel-selected port for a configured zero.
func (r *Runtime) DAPAddress() string {
	if r == nil || r.dap == nil {
		return ""
	}
	return r.dap.Addr().String()
}

// ServeDAPConnection upgrades one authenticated control connection into the
// DAP frontend without redialing the published address. The DAP server keeps
// ownership of its single-frontend admission and session lifetime.
func (r *Runtime) ServeDAPConnection(ctx context.Context, connection net.Conn) error {
	if r == nil || r.dap == nil {
		return ErrRuntimeClosed
	}
	return r.dap.ServeConnection(ctx, connection)
}

// Errors reports terminal bridge loss and client-scoped DAP transport errors.
// It is intentionally never closed, because a consumer must not treat a
// shutdown race as evidence that a bridge failure was handled successfully.
func (r *Runtime) Errors() <-chan error {
	if r == nil {
		return nil
	}
	return r.errors
}

// Snapshot returns only the actor's observation-derived target state.
func (r *Runtime) Snapshot(ctx context.Context) (session.Snapshot, error) {
	if r == nil || r.actor == nil {
		return session.Snapshot{}, ErrRuntimeClosed
	}
	return r.actor.Snapshot(ctx)
}

// BreakpointCounts returns aggregate DAP-owned breakpoint lifecycle evidence.
func (r *Runtime) BreakpointCounts(ctx context.Context) (BreakpointCounts, error) {
	if r == nil || r.actor == nil {
		return BreakpointCounts{}, ErrRuntimeClosed
	}
	return r.actor.BreakpointCounts(ctx)
}

// TerminalMetadata returns the operation category retained from an actor
// fault. It never exposes raw bridge, MULTI, or target diagnostics.
func (r *Runtime) TerminalMetadata() TerminalMetadata {
	if r == nil {
		return TerminalMetadata{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminal
}

// Threads returns the actor's stable core-to-thread projection.
func (r *Runtime) Threads(ctx context.Context) ([]actor.Thread, error) {
	if r == nil || r.actor == nil {
		return nil, ErrRuntimeClosed
	}
	return r.actor.Threads(ctx)
}

// Serve accepts one DAP frontend at a time while also observing the owned
// bridge child. A bridge exit closes the listener, terminates the active DAP
// session, releases the actor frontend, and makes Serve return the terminal
// failure instead of pretending the daemon is still healthy.
func (r *Runtime) Serve(ctx context.Context) error {
	if r == nil || r.dap == nil {
		return ErrRuntimeClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.serving {
		r.mu.Unlock()
		return ErrServing
	}
	if r.terminalErr != nil {
		err := r.terminalErr
		r.mu.Unlock()
		return err
	}
	r.serving = true
	r.mu.Unlock()

	serveErr := make(chan error, 1)
	go func() { serveErr <- r.dap.Serve(ctx) }()
	select {
	case <-ctx.Done():
		return nil
	case <-r.closeDone:
		if failed := r.terminalFailure(); failed != nil {
			return failed
		}
		return ErrRuntimeClosed
	case err := <-serveErr:
		if failed := r.terminalFailure(); failed != nil {
			return failed
		}
		return err
	}
}

func coldPreparation(value config.ConnectionPreparation) (multi.ColdPreparation, error) {
	switch value {
	case config.ConnectionPreparationNone:
		return multi.ColdPreparationNone, nil
	case config.ConnectionPreparationAlreadyPresentNoVerify:
		return multi.ColdPreparationAlreadyPresentNoVerify, nil
	default:
		return "", errors.New("daemon: unknown validated connection preparation")
	}
}

// Close is idempotent. It stops new DAP work and releases the frontend before
// stopping actor I/O and then terminating only the mpythonrun child created by
// this Runtime. It deliberately preserves MULTI's warm service-router state.
func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.signalClosed()
		if r.dap != nil {
			_ = r.dap.Close()
			_ = r.dap.Terminate()
		}
		if r.backend != nil {
			r.backend.Close()
		}
		if r.actor != nil {
			r.actor.Close()
		}
		if r.process != nil {
			_ = r.process.Close()
		}
		if r.lock != nil {
			if err := r.lock.Close(); err != nil {
				r.report(err)
			}
		}
	})
}

func (r *Runtime) monitorBridge() {
	<-r.process.Done()
	err := r.process.Wait()
	if err == nil {
		err = ErrBridgeExited
	} else {
		err = fmt.Errorf("%w: %v", ErrBridgeExited, err)
	}

	r.fail(err)
}

func (r *Runtime) monitorActor() {
	select {
	case <-r.closeDone:
		return
	case fault := <-r.actor.Faults():
		r.failWithMetadata(fmt.Errorf("%w: %w", ErrActorFaulted, fault), TerminalMetadata{
			ActorOperation: fault.Operation, HasActorOperation: true,
		})
	}
}

func (r *Runtime) fail(err error) {
	r.failWithMetadata(err, TerminalMetadata{})
}

func (r *Runtime) failWithMetadata(err error, metadata TerminalMetadata) {
	r.failOnce.Do(func() {
		r.mu.Lock()
		select {
		case <-r.closeDone:
			r.mu.Unlock()
			return
		default:
		}
		r.terminalErr = err
		r.terminal = metadata
		r.mu.Unlock()
		r.signalClosed()

		if r.dap != nil {
			_ = r.dap.Close()
			_ = r.dap.Terminate()
		}
		if r.backend != nil {
			r.backend.Close()
		}
		if r.actor != nil {
			r.actor.Close()
		}
		if r.process != nil {
			_ = r.process.Close()
		}
		if r.lock != nil {
			if releaseErr := r.lock.Close(); releaseErr != nil {
				r.report(releaseErr)
			}
		}
		r.report(err)
	})
}

func (r *Runtime) signalClosed() {
	r.doneOnce.Do(func() { close(r.closeDone) })
}

func (r *Runtime) terminalFailure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminalErr
}

func (r *Runtime) forwardDAPErrors() {
	for {
		select {
		case <-r.closeDone:
			return
		case err := <-r.dap.Errors():
			if err != nil {
				r.report(err)
			}
		}
	}
}

func (r *Runtime) report(err error) {
	select {
	case r.errors <- err:
	default:
	}
}
