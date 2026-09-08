package bridge

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReadyFileValidation(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"ipv4", `{"host":"127.0.0.1","port":43123}`, "127.0.0.1:43123"},
		{"ipv6", `{"host":"::1","port":43123}`, "[::1]:43123"},
		{"spoofed host", `{"host":"192.0.2.1","port":43123}`, ""},
		{"unknown field", `{"host":"127.0.0.1","port":43123,"extra":true}`, ""},
		{"duplicate field", `{"host":"127.0.0.1","host":"127.0.0.1","port":43123}`, ""},
		{"fractional port", `{"host":"127.0.0.1","port":1.5}`, ""},
		{"malformed", `{`, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			address, err := parseReadyFile([]byte(test.data))
			if test.want == "" {
				if err == nil {
					t.Fatal("parseReadyFile() error = nil")
				}
				return
			}
			if err != nil || address != test.want {
				t.Fatalf("parseReadyFile() = %q, %v; want %q, nil", address, err, test.want)
			}
		})
	}
}

func TestExplicitStartupDeadlineIsNotShortenedByFallback(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 2*defaultStartupTimeout)
	defer cancelParent()

	startup, cancelStartup := withDefaultStartupTimeout(parent)
	defer cancelStartup()
	parentDeadline, parentOK := parent.Deadline()
	startupDeadline, startupOK := startup.Deadline()
	if !parentOK || !startupOK || !startupDeadline.Equal(parentDeadline) {
		t.Fatalf("startup deadline = (%v, %v), want parent deadline (%v, %v)", startupDeadline, startupOK, parentDeadline, parentOK)
	}
}

func TestStartupFallbackBoundsUndeadlinedCaller(t *testing.T) {
	startup, cancel := withDefaultStartupTimeout(context.Background())
	defer cancel()
	deadline, ok := startup.Deadline()
	if !ok {
		t.Fatal("fallback startup context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > defaultStartupTimeout {
		t.Fatalf("fallback deadline remaining = %v, want within (0, %v]", remaining, defaultStartupTimeout)
	}
}

func TestReadReadyFileRejectsOversizeAndNonRegular(t *testing.T) {
	directory := t.TempDir()
	overSize := filepath.Join(directory, "large.json")
	if err := os.WriteFile(overSize, []byte(strings.Repeat("x", maxReadyFileSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readReadyFile(overSize); err == nil {
		t.Fatal("oversize ready file was accepted")
	}
	if os.PathSeparator != '\\' {
		link := filepath.Join(directory, "ready-link")
		if err := os.Symlink(overSize, link); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readReadyFile(link); err == nil {
			t.Fatal("symlink ready file was accepted")
		}
	}
}

func TestStartUsesPublishedEphemeralPortAndCloseCleansOwnedState(t *testing.T) {
	withHelperCommand(t, "serve")
	process, err := Start(context.Background(), LaunchSpec{Executable: "ignored", BridgeScript: "ignored", RPCPort: 0})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if process.Client() == nil || process.PID() == 0 || process.Address() == "" {
		t.Fatalf("process = client=%v pid=%d address=%q", process.Client(), process.PID(), process.Address())
	}
	directory := process.tempDir
	if err := process.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("Done did not close")
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private startup directory still exists: %v", err)
	}
}

func TestStartSecuresWorkspaceBeforeLaunchingAndCleansOnFailure(t *testing.T) {
	previousSecure := secureStartupWorkspaceHook
	previousStart := startCommand
	var workspace string
	secured := false
	started := false
	secureStartupWorkspaceHook = func(path string) error {
		workspace = path
		secured = true
		return errors.New("test ACL failure")
	}
	startCommand = func(LaunchSpec) (*exec.Cmd, error) {
		started = true
		return nil, errors.New("must not launch")
	}
	t.Cleanup(func() {
		secureStartupWorkspaceHook = previousSecure
		startCommand = previousStart
	})

	_, err := Start(context.Background(), LaunchSpec{Executable: "ignored", BridgeScript: "ignored", RPCPort: 0})
	if err == nil || !strings.Contains(err.Error(), "bridge: secure startup workspace: test ACL failure") {
		t.Fatalf("Start() error = %v, want stable workspace-security error", err)
	}
	if !secured || workspace == "" {
		t.Fatal("Start() did not secure its workspace")
	}
	if started {
		t.Fatal("Start() launched child after workspace security failure")
	}
	if _, statErr := os.Stat(workspace); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("insecure workspace was not cleaned up: %v", statErr)
	}
}

func TestStartRejectsMalformedAndSpoofedReadyFiles(t *testing.T) {
	for _, mode := range []string{"malformed", "spoofed", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			withHelperCommand(t, mode)
			_, err := Start(context.Background(), LaunchSpec{Executable: "ignored", BridgeScript: "ignored", RPCPort: 0})
			if err == nil {
				t.Fatal("Start() error = nil")
			}
		})
	}
}

func TestStartReturnsEarlyExitAndCancellation(t *testing.T) {
	t.Run("early exit", func(t *testing.T) {
		withHelperCommand(t, "exit")
		_, err := Start(context.Background(), LaunchSpec{Executable: "ignored", BridgeScript: "ignored", RPCPort: 0})
		if err == nil || !strings.Contains(err.Error(), "exited") {
			t.Fatalf("Start() error = %v, want early-exit error", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		withHelperCommand(t, "wait")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(100*time.Millisecond, cancel)
		defer timer.Stop()
		_, err := Start(ctx, LaunchSpec{Executable: "ignored", BridgeScript: "ignored", RPCPort: 0})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want context.Canceled", err)
		}
	})
}

func TestStartRejectsWarmLaunchWithoutServiceRouterBeforeStarting(t *testing.T) {
	previous := startCommand
	called := false
	startCommand = func(LaunchSpec) (*exec.Cmd, error) {
		called = true
		return nil, errors.New("must not start")
	}
	t.Cleanup(func() { startCommand = previous })

	_, err := Start(context.Background(), LaunchSpec{
		Executable: "ignored", BridgeScript: "ignored", RPCPort: 0, ReadyFile: "ignored",
		SessionMode: SessionModeWarm,
	})
	if err == nil {
		t.Fatal("Start() error = nil, want warm service-router validation error")
	}
	if !strings.Contains(err.Error(), "warm launch requires a service router") {
		t.Fatalf("Start() error = %v, want warm service-router validation error", err)
	}
	if called {
		t.Fatal("Start() invoked startCommand for an invalid warm launch")
	}
}

func withHelperCommand(t *testing.T, mode string) {
	t.Helper()
	previous := startCommand
	startCommand = func(spec LaunchSpec) (*exec.Cmd, error) {
		command := exec.Command(os.Args[0], "-test.run=TestBridgeBootstrapHelperProcess")
		command.Env = append(os.Environ(), "GO_WANT_BRIDGE_HELPER=1", "GO_BRIDGE_HELPER_MODE="+mode, "GO_BRIDGE_READY_FILE="+spec.ReadyFile)
		return command, nil
	}
	t.Cleanup(func() { startCommand = previous })
}

func TestBridgeBootstrapHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_BRIDGE_HELPER") != "1" {
		return
	}
	readyFile := os.Getenv("GO_BRIDGE_READY_FILE")
	switch os.Getenv("GO_BRIDGE_HELPER_MODE") {
	case "exit":
		return
	case "malformed":
		_ = os.WriteFile(readyFile, []byte("{"), 0o600)
	case "spoofed":
		_ = os.WriteFile(readyFile, []byte(`{"host":"192.0.2.1","port":43123}`), 0o600)
	case "oversize":
		_ = os.WriteFile(readyFile, []byte(strings.Repeat("x", maxReadyFileSize+1)), 0o600)
	case "wait":
		// A bare select is detected as a deadlock when this is the helper's
		// only goroutine, making the process exit before the parent cancels.
		time.Sleep(30 * time.Second)
		return
	case "serve":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		defer listener.Close()
		address := listener.Addr().(*net.TCPAddr)
		ready := `{"host":"127.0.0.1","port":` + strconv.Itoa(address.Port) + `}`
		if err := os.WriteFile(readyFile, []byte(ready), 0o600); err != nil {
			os.Exit(3)
		}
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = connection.Write([]byte(testHandshake))
		buffer := make([]byte, 1)
		_, _ = connection.Read(buffer)
		return
	default:
		os.Exit(4)
	}
	// Keep malformed-ready helpers alive until Start owns and terminates them.
	time.Sleep(30 * time.Second)
}
