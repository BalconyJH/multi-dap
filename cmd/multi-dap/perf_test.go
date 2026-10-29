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
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/perf"
)

func TestProxyPerformanceFlagsAreExplicitBoundedAndConfigBound(t *testing.T) {
	configPath := writeConfig(t)
	output := filepath.Join(t.TempDir(), "host-trace.json")
	base := []string{"--config", configPath, "--control-dir", t.TempDir()}
	target, err := proxyCommandFlags(base)
	if err != nil {
		t.Fatal(err)
	}
	if target.perf.output != "" {
		t.Fatal("performance capture enabled without an option")
	}
	target, err = proxyCommandFlags(append(append([]string(nil), base...), "--perf-output", output))
	if err != nil {
		t.Fatal(err)
	}
	if target.perf.output != output || target.perf.maxEvents != perf.DefaultMaxEvents {
		t.Fatalf("performance options = %#v", target.perf)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("flag parsing created output: %v", err)
	}
	for _, extra := range [][]string{
		{"--perf-max-events", "100"},
		{"--perf-output", ""},
		{"--perf-output", "relative.json"},
		{"--perf-output", output, "--perf-max-events", "0"},
		{"--perf-output", output, "--perf-max-events", "-1"},
		{"--perf-output", output, "--perf-max-events", "100001"},
		{"--perf-output", output, "--perf-max-events", "bad"},
	} {
		if _, err := proxyCommandFlags(append(append([]string(nil), base...), extra...)); err == nil {
			t.Fatalf("performance options %q succeeded", extra)
		}
	}
	for _, extra := range [][]string{
		{"--perf-output", output},
		{"--perf-max-events", "100"},
	} {
		args := append([]string{"--probe-id", "test-probe", "--control-dir", t.TempDir()}, extra...)
		if _, err := proxyCommandFlags(args); err == nil || !strings.Contains(err.Error(), "requires --config") {
			t.Fatalf("legacy performance options %q = %v", args, err)
		}
	}
	if !strings.Contains(usage(), "--perf-output") || !strings.Contains(usage(), "--perf-max-events") {
		t.Fatal("usage does not describe performance capture")
	}
}

func TestCreatePerfFileNeverOverwritesOrCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host-trace.json")
	file, err := createPerfFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("preserve me"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if replacement, err := createPerfFile(path); err == nil {
		replacement.Close()
		t.Fatal("existing output was replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve me" {
		t.Fatalf("existing output = %q, %v", data, err)
	}
	if goruntime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("output permissions = %v, %v", info, err)
		}
	}
	parent := filepath.Join(dir, "absent")
	if file, err := createPerfFile(filepath.Join(parent, "trace.json")); err == nil {
		file.Close()
		t.Fatal("missing output directory accepted")
	}
	if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output directory created: %v", err)
	}
}

func TestPerfProxyPreservesExactDAPBytesAndExportsOnlyObservations(t *testing.T) {
	clientBytes := perfFrame(`{"seq":1,"type":"request","command":"threads","arguments":{"secret":"private-source-path"},"extra":"preserve-this-extension"}`) +
		perfFrame(`{"seq":2,"type":"request","command":"evaluate","arguments":{"expression":"private-variable"}}`)
	serverBytes := perfFrame(`{ "seq":1,"type":"response","request_seq":2,"command":"evaluate","success":false,"message":"private-error-text","body":{"value":"private-memory"},"extra":"preserve-this-response-extension" }`) +
		perfFrame(`{"seq":2,"type":"event","event":"stopped","body":{"reason":"private-stop-reason"}}`) +
		perfFrame(`{"seq":3,"type":"response","request_seq":1,"command":"threads","success":true,"body":{"threads":[{"id":1,"name":"private-core-name"}]}}`)
	received := make(chan string, 1)
	configPath, controlDir := perfProxyFixture(t, func(_ context.Context, connection net.Conn) error {
		data, err := io.ReadAll(connection)
		if err != nil {
			return err
		}
		received <- string(data)
		_, err = io.WriteString(connection, serverBytes)
		return err
	})
	path := filepath.Join(t.TempDir(), "trace.json")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := runProxy(ctx, []string{
		"--config", configPath, "--control-dir", controlDir, "--perf-output", path,
	}, strings.NewReader(clientBytes), &output); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != clientBytes {
			t.Fatalf("proxy modified client bytes: %q", got)
		}
	default:
		t.Fatal("authenticated proxy handler was not reached")
	}
	if output.String() != serverBytes {
		t.Fatalf("proxy modified server bytes or wrote diagnostics to stdout: %q", output.String())
	}
	trace, encoded := readPerfTrace(t, path)
	if trace.OtherData.CompletedRequests != 2 || trace.OtherData.FailedRequests != 1 ||
		trace.OtherData.ObservedEvents != 1 || trace.OtherData.FramingErrors != 0 ||
		trace.OtherData.ProxyFailed || !trace.OtherData.CaptureComplete || trace.OtherData.TargetCPUTiming {
		t.Fatalf("capture metadata = %#v", trace.OtherData)
	}
	if len(trace.OtherData.RequestSummary) != 2 {
		t.Fatalf("request summaries = %#v", trace.OtherData.RequestSummary)
	}
	for _, summary := range trace.OtherData.RequestSummary {
		if summary.TotalDurationUS < 0 || summary.MaxDurationUS < 0 {
			t.Fatalf("negative host latency = %#v", summary)
		}
	}
	for _, private := range []string{
		"private-source-path", "private-variable", "private-error-text", "private-memory",
		"private-stop-reason", "private-core-name", "preserve-this-extension", "preserve-this-response-extension",
		configPath, controlDir,
	} {
		if strings.Contains(encoded, private) {
			t.Fatalf("trace contains private payload %q", private)
		}
	}
}

func TestPerfProxyCLionSessionAddsNoBackendActions(t *testing.T) {
	backend := &clionProxyBackend{}
	dapServer, err := dap.Listen("127.0.0.1:0", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer dapServer.Close()
	configPath, controlDir := perfProxyFixture(t, dapServer.ServeConnection)
	requests := []dap.Envelope{
		dapRequest(1, "initialize", `{"clientID":"clion","adapterID":"clion.dap.debugger"}`),
		dapRequest(2, "attach", `{"request":"attach"}`),
		dapRequest(3, "configurationDone", `{}`),
		dapRequest(4, "threads", `{}`),
		dapRequest(5, "private-unsupported-command", `{}`),
		dapRequest(6, "disconnect", `{"terminateDebuggee":false}`),
	}
	var input bytes.Buffer
	for _, request := range requests {
		if err := dap.NewEncoder(&input).Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "clion-trace.json")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := runProxy(ctx, []string{
		"--config", configPath, "--control-dir", controlDir, "--perf-output", path,
	}, &input, &output); err != nil {
		t.Fatal(err)
	}
	wantActions := []dap.ActionKind{dap.ActionAttach, dap.ActionConfigurationDone, dap.ActionThreads, dap.ActionDisconnect}
	if len(backend.actions) != len(wantActions) {
		t.Fatalf("capture added backend actions: %#v", backend.actions)
	}
	for index, action := range backend.actions {
		if action.Kind != wantActions[index] {
			t.Fatalf("backend action %d = %v, want %v", index, action.Kind, wantActions[index])
		}
	}
	decoder := dap.NewDecoder(&output)
	responses, failed := 0, 0
	for {
		message, err := decoder.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("performance capture contaminated DAP stdout: %v", err)
		}
		if message.Type == dap.TypeResponse {
			responses++
			if message.Success != nil && !*message.Success {
				failed++
			}
		}
	}
	if responses != len(requests) || failed != 1 {
		t.Fatalf("DAP responses = %d, failures = %d", responses, failed)
	}
	trace, encoded := readPerfTrace(t, path)
	if trace.OtherData.CompletedRequests != uint64(len(requests)) || trace.OtherData.FailedRequests != 1 || !trace.OtherData.CaptureComplete {
		t.Fatalf("CLion capture metadata = %#v", trace.OtherData)
	}
	if strings.Contains(encoded, "private-unsupported-command") {
		t.Fatal("arbitrary command name escaped the recorder allowlist")
	}
}

func TestPerfProxyExistingOutputFailsBeforeDialing(t *testing.T) {
	var calls atomic.Int32
	configPath, controlDir := perfProxyFixture(t, func(context.Context, net.Conn) error {
		calls.Add(1)
		return nil
	})
	path := filepath.Join(t.TempDir(), "trace.json")
	if err := os.WriteFile(path, []byte("existing capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runProxy(context.Background(), []string{
		"--config", configPath, "--control-dir", controlDir, "--perf-output", path,
	}, strings.NewReader("do not forward"), &output)
	if err == nil || !strings.Contains(err.Error(), "create output") {
		t.Fatalf("existing output error = %v", err)
	}
	if calls.Load() != 0 || output.Len() != 0 {
		t.Fatal("invalid output reached the daemon or wrote stdout")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing capture" {
		t.Fatalf("existing capture changed: %q, %v", data, err)
	}
}

func TestPerfProxyFinalizesAfterCancellationAndOutputFailure(t *testing.T) {
	for _, scenario := range []string{"cancelled", "output failure"} {
		t.Run(scenario, func(t *testing.T) {
			configPath, controlDir := perfProxyFixture(t, func(_ context.Context, connection net.Conn) error {
				if _, err := io.ReadAll(connection); err != nil {
					return err
				}
				_, err := io.WriteString(connection, perfFrame(`{"seq":1,"type":"response","request_seq":1,"command":"threads","success":true}`))
				return err
			})
			path := filepath.Join(t.TempDir(), "trace.json")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var output io.Writer = io.Discard
			writeErr := errors.New("private-output-error")
			if scenario == "cancelled" {
				cancel()
			} else {
				output = perfFailureWriter{err: writeErr}
			}
			err := runProxy(ctx, []string{
				"--config", configPath, "--control-dir", controlDir, "--perf-output", path,
			}, strings.NewReader(perfFrame(`{"seq":1,"type":"request","command":"threads"}`)), output)
			if err == nil {
				t.Fatal("failed proxy returned nil")
			}
			if scenario == "output failure" && !errors.Is(err, writeErr) {
				t.Fatalf("output error lost: %v", err)
			}
			trace, encoded := readPerfTrace(t, path)
			if !trace.OtherData.ProxyFailed || trace.OtherData.CaptureComplete {
				t.Fatalf("failed capture metadata = %#v", trace.OtherData)
			}
			if strings.Contains(encoded, "private-output-error") {
				t.Fatal("trace retained a raw proxy failure")
			}
		})
	}
}

func TestFinishPerfTracePreservesProxyAndExportFailures(t *testing.T) {
	file, err := createPerfFile(filepath.Join(t.TempDir(), "closed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	proxyErr := errors.New("proxy failed")
	err = finishPerfTrace(file, perf.Trace{}, proxyErr)
	if !errors.Is(err, proxyErr) || !errors.Is(err, os.ErrClosed) || !strings.Contains(err.Error(), "write output") {
		t.Fatalf("combined finalization error = %v", err)
	}
}

type perfFailureWriter struct{ err error }

func (w perfFailureWriter) Write([]byte) (int, error) { return 0, w.err }

func perfFrame(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

func readPerfTrace(t *testing.T, path string) (perf.Trace, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var trace perf.Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatal(err)
	}
	return trace, string(data)
}

func perfProxyFixture(t *testing.T, handler func(context.Context, net.Conn) error) (string, string) {
	t.Helper()
	configPath := writeConfig(t)
	controlDir := t.TempDir()
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: bindingDigestForPath(t, configPath),
		DAPAddress: "127.0.0.1:43123",
		Status:     func(context.Context) (control.Status, error) { return control.Status{}, nil },
		Shutdown:   func() {},
		Proxy: func(ctx context.Context, connection net.Conn) error {
			defer connection.Close()
			return handler(ctx, connection)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := control.NewStore(controlDir)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	if err := store.Publish(server.Record()); err != nil {
		server.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		if err := <-served; err != nil {
			t.Errorf("control Serve = %v", err)
		}
		_ = store.RemoveIfInstance("test-probe", server.Record().InstanceID)
	})
	return configPath, controlDir
}
