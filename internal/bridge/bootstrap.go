package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	defaultStartupTimeout = 15 * time.Second
	maxReadyFileSize      = 4096
)

// startCommand is replaceable by package tests. Production always uses the
// platform-specific LaunchSpec.Command implementation.
var startCommand = func(spec LaunchSpec) (*exec.Cmd, error) { return spec.Command() }

// secureStartupWorkspace is replaceable by package tests.  It is deliberately
// kept in this package rather than sharing control's store helpers: the bridge
// rendezvous directory is separate, short-lived state with no control-record
// lifecycle or package dependency.
var secureStartupWorkspaceHook = secureStartupWorkspace

// Process owns exactly one mpythonrun child, its private rendezvous directory,
// and the MBP client accepted by that child. It never owns MULTI, a service
// router, or a target server.
type Process struct {
	cmd     *exec.Cmd
	client  *Client
	address string
	tempDir string

	done chan struct{}

	mu      sync.Mutex
	waitErr error
	close   sync.Once
}

// Start creates an owned ready file, starts mpythonrun, consumes exactly one
// ready publication, and validates the MBP handshake. It deliberately does
// not retry a dial: an unavailable ready endpoint is a failed bridge start.
func Start(ctx context.Context, spec LaunchSpec) (*Process, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	startupCtx, cancel := withDefaultStartupTimeout(ctx)
	defer cancel()
	if err := startupCtx.Err(); err != nil {
		return nil, err
	}

	directory, err := os.MkdirTemp("", "multi-dap-bridge-")
	if err != nil {
		return nil, fmt.Errorf("bridge: create startup directory: %w", err)
	}
	// bridge.py uses this directory for its ready publication and console
	// snapshots.  It must be private before the child starts: its atomic ready
	// publication requires the destination to remain absent, so securing the
	// directory (rather than pre-creating ready.json) protects both artifacts.
	if err := secureStartupWorkspaceHook(directory); err != nil {
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("bridge: secure startup workspace: %w", err)
	}
	readyFile := filepath.Join(directory, "ready.json")
	spec.ReadyFile = readyFile
	// Validate before delegating to startCommand so test launchers and future
	// platform implementations cannot bypass the warm-router invariant in
	// LaunchSpec.Args. The owned ready-file path is part of a valid ephemeral
	// launch, so validation happens after it is installed.
	if _, err := spec.Args(); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	command, err := startCommand(spec)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("bridge: start mpythonrun: %w", err)
	}

	process := &Process{
		cmd:     command,
		tempDir: directory,
		done:    make(chan struct{}),
	}
	go process.reap()

	address, err := waitReady(startupCtx, readyFile, process.done)
	if err == nil {
		process.address = address
		var client *Client
		client, err = DialLoopback(startupCtx, address)
		if err == nil {
			process.mu.Lock()
			select {
			case <-process.done:
				process.mu.Unlock()
				_ = client.Close()
				err = errors.New("mpythonrun exited during MBP handshake")
			default:
				process.client = client
				process.mu.Unlock()
			}
		}
	}
	if err != nil {
		if contextErr := startupCtx.Err(); contextErr != nil {
			err = contextErr
		}
		_ = process.Close()
		return nil, fmt.Errorf("bridge: start bridge process: %w", err)
	}
	return process, nil
}

// withDefaultStartupTimeout bounds direct callers which supply no deadline,
// while preserving an explicit caller-owned startup policy. The daemon's
// validated StartupDeadline must not be silently shortened to this package's
// fallback value.
func withDefaultStartupTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, defaultStartupTimeout)
}

// Client returns the validated MBP client owned by this process.
func (p *Process) Client() *Client {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.client
}

// Address returns the numeric loopback endpoint published by bridge.py.
func (p *Process) Address() string { return p.address }

// PID returns the PID of the owned mpythonrun child.
func (p *Process) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Done closes after the child exits and owned resources have been released.
func (p *Process) Done() <-chan struct{} { return p.done }

// Wait waits for mpythonrun to exit. A normal Close which kills the child may
// report the platform's process-exit error; callers that initiated shutdown
// should use Close instead.
func (p *Process) Wait() error {
	if p == nil {
		return errors.New("bridge: nil process")
	}
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

// Close is idempotent. It poisons the MBP client, terminates only the child
// started by Start, then waits for the reaper to remove private state.
func (p *Process) Close() error {
	if p == nil {
		return nil
	}
	p.close.Do(func() {
		p.mu.Lock()
		client := p.client
		p.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		select {
		case <-p.done:
			return
		default:
		}
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-p.done
	})
	return nil
}

func (p *Process) reap() {
	err := p.cmd.Wait()
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if cleanupErr := os.RemoveAll(p.tempDir); cleanupErr != nil && err == nil {
		err = fmt.Errorf("bridge: remove startup directory: %w", cleanupErr)
	}
	p.mu.Lock()
	p.waitErr = err
	p.mu.Unlock()
	close(p.done)
}

func waitReady(ctx context.Context, path string, exited <-chan struct{}) (string, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, found, err := readReadyFile(path)
		if err != nil {
			return "", err
		}
		if found {
			return parseReadyFile(data)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-exited:
			return "", errors.New("mpythonrun exited before publishing ready file")
		case <-ticker.C:
		}
	}
}

func readReadyFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read ready file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("ready file is not a regular file")
	}
	if info.Size() > maxReadyFileSize {
		return nil, false, errors.New("ready file exceeds maximum size")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read ready file: %w", err)
	}
	if len(data) > maxReadyFileSize {
		return nil, false, errors.New("ready file exceeds maximum size")
	}
	return data, true, nil
}

func parseReadyFile(data []byte) (string, error) {
	fields, err := strictReadyFields(data)
	if err != nil {
		return "", err
	}
	if len(fields) != 2 || fields["host"] == nil || fields["port"] == nil {
		return "", errors.New("ready file must contain only host and port")
	}
	var host string
	var port int
	if err := json.Unmarshal(fields["host"], &host); err != nil {
		return "", errors.New("ready file host must be a string")
	}
	if err := json.Unmarshal(fields["port"], &port); err != nil {
		return "", errors.New("ready file port must be an integer")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.String() != host {
		return "", ErrNonLoopbackAddress
	}
	if port < 1 || port > 65535 {
		return "", errors.New("ready file port must be in 1..65535")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func strictReadyFields(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("malformed ready file: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("ready file must be a JSON object")
	}
	fields := make(map[string]json.RawMessage, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("malformed ready file: %w", err)
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("ready file field name must be a string")
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("ready file repeats field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("malformed ready file: %w", err)
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("malformed ready file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("ready file has trailing JSON")
		}
		return nil, fmt.Errorf("malformed ready file: %w", err)
	}
	return fields, nil
}
