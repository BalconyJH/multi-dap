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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

type traceCaptureFixture struct {
	config, directory, output string
	mode                      string
	mu                        sync.Mutex
	requests                  []dap.Envelope
	entered                   atomic.Int32
	shutdowns                 atomic.Int32
	statusCalls               atomic.Int32
	done                      chan error
	stop                      chan struct{}
}

func newTraceCaptureFixture(t *testing.T, mode string) *traceCaptureFixture {
	t.Helper()
	f := &traceCaptureFixture{
		config: writeConfig(t), directory: t.TempDir(), mode: mode,
		output: filepath.Join(t.TempDir(), "PRIVATE_CAPTURE.json"), done: make(chan error, 1), stop: make(chan struct{}),
	}
	server, err := control.NewServer(control.ServerOptions{
		ProbeID: "test-probe", ConfigDigest: bindingDigestForPath(t, f.config),
		DAPAddress: "127.0.0.1:1",
		Status: func(context.Context) (control.Status, error) {
			f.statusCalls.Add(1)
			return control.Status{}, nil
		},
		Shutdown: func() { f.shutdowns.Add(1) },
		Proxy: func(_ context.Context, connection net.Conn) error {
			f.entered.Add(1)
			err := f.serveDAP(connection)
			f.done <- err
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := control.NewStore(f.directory)
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	record := server.Record()
	switch mode {
	case "config_mismatch":
		record.ConfigDigest = otherTestConfigDigest
	case "bad_token":
		record.Token = strings.Repeat("A", 43)
	}
	if err := store.Publish(record); err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	t.Cleanup(func() {
		close(f.stop)
		cancel()
		_ = server.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("synthetic control server: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("synthetic control server did not exit")
		}
		if f.entered.Load() != 0 {
			select {
			case <-f.done:
			case <-time.After(3 * time.Second):
				t.Error("synthetic DAP frontend was not detached")
			}
		}
	})
	return f
}

func (f *traceCaptureFixture) args() []string {
	return []string{
		"trace", "capture", "--config", f.config, "--control-dir", f.directory, "--output", f.output,
		"--session-id", "17", "--source-id", "PRIVATE_CAPTURE_SOURCE",
		"--acknowledge-unverified-source", "--core-id", "0", "--count", "3",
	}
}

func (f *traceCaptureFixture) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []string
	for _, request := range f.requests {
		result = append(result, request.Command)
	}
	return result
}

func (f *traceCaptureFixture) serveDAP(connection net.Conn) error {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	decoder, encoder := dap.NewDecoder(connection), dap.NewEncoder(connection)
	var serverSeq, requestSeq, attachSeq int64
	var source dap.NativeTraceOpenArguments
	write := func(message dap.Envelope) error {
		serverSeq++
		message.Seq = serverSeq
		return encoder.Encode(message)
	}
	reply := func(request dap.Envelope, body any, success bool, message string) error {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		return write(dap.Envelope{
			Type: dap.TypeResponse, RequestSeq: request.Seq, Command: request.Command,
			Success: &success, Message: message, Body: raw,
		})
	}
	status := func() tracecontrol.Status {
		return tracecontrol.Status{
			Handle: 7, State: "open", NativeGeneration: 1, PacketCount: 3, PacketCountKnown: true,
			Timebase: "picoseconds", TraceState: "unknown", SourceBinding: "operator_declared",
			LastReceipt: tracecontrol.Receipt{Operation: "observe", Outcome: "observed", CollectionMayContinue: true},
		}
	}
	for {
		request, err := decoder.Decode()
		if err != nil {
			return err
		}
		requestSeq++
		if request.Type != dap.TypeRequest || request.Seq != requestSeq {
			return errors.New("unexpected synthetic DAP request identity")
		}
		f.mu.Lock()
		f.requests = append(f.requests, request)
		f.mu.Unlock()
		switch request.Command {
		case "initialize":
			if f.mode == "active_frontend" {
				return reply(request, nil, false, "another DAP client is already active")
			}
			if f.mode == "event_flood" {
				for range traceCaptureMaxMessages + 1 {
					if err := write(dap.Envelope{Type: dap.TypeEvent, Event: "output", Body: json.RawMessage(`{"output":"PRIVATE_OUTPUT"}`)}); err != nil {
						return err
					}
				}
				return nil
			}
			if err := reply(request, dap.Capabilities{
				SupportsConfigurationDoneRequest: true, MultiDapSupportsNativeTraceRequests: true,
			}, true, ""); err != nil {
				return err
			}
		case "attach":
			attachSeq = request.Seq
			if string(request.Arguments) != "{}" {
				return errors.New("trace capture attempted nonempty attach arguments")
			}
			if err := write(dap.Envelope{Type: dap.TypeEvent, Event: "initialized"}); err != nil {
				return err
			}
		case "configurationDone":
			if err := reply(request, struct{}{}, true, ""); err != nil {
				return err
			}
			if err := reply(dap.Envelope{Seq: attachSeq, Command: "attach"}, struct{}{}, true, ""); err != nil {
				return err
			}
		case "multi-dap/trace/open":
			if err := json.Unmarshal(request.Arguments, &source); err != nil ||
				source.SessionID != 17 || !source.AcknowledgeUnverifiedSource {
				return errors.New("trace capture lost its explicit source declaration")
			}
			if err := reply(request, tracecontrol.Result{
				Status: status(), Receipt: tracecontrol.Receipt{Operation: "open", Outcome: "observed", CollectionMayContinue: true},
			}, true, ""); err != nil {
				return err
			}
		case "multi-dap/trace/poll":
			current := status()
			if f.mode == "qualified_status" {
				current.HardwareQualified = true
			}
			if err := reply(request, tracecontrol.Result{
				Status: current, Receipt: tracecontrol.Receipt{Operation: "status", Outcome: "observed", CollectionMayContinue: true},
			}, true, ""); err != nil {
				return err
			}
		case "multi-dap/trace/read":
			var args dap.NativeTraceReadArguments
			if err := json.Unmarshal(request.Arguments, &args); err != nil ||
				args.Handle != 7 || args.Generation != 1 {
				return errors.New("trace capture lost its native handle/generation")
			}
			if err := write(dap.Envelope{Type: dap.TypeEvent, Event: "output", Body: json.RawMessage(`{"output":"PRIVATE_OUTPUT"}`)}); err != nil {
				return err
			}
			switch f.mode {
			case "partial", "timeout":
				_, _ = io.WriteString(connection, "Content-Length: 300\r\n\r\n{\"seq\":")
				if f.mode == "timeout" {
					_, _ = io.Copy(io.Discard, connection)
				}
				return nil
			case "oversize":
				_, err := fmt.Fprintf(connection, "Content-Length: %d\r\n\r\n", dap.DefaultMaxContentLength+1)
				return err
			case "read_failure", "stale_generation":
				if err := reply(request, nil, false, "PRIVATE_NATIVE_ERROR"); err != nil {
					return err
				}
				continue
			}
			capture := syntheticNativeCapture(3)
			capture["source"] = timemachine.Source{
				Kind: source.SourceKind, SessionID: source.SessionID, SourceID: source.SourceID, CoreID: source.CoreID,
				ImageSHA256: source.ImageSHA256, SnapshotSHA256: source.SnapshotSHA256,
			}
			generation := capture["generations"].([]any)[0].(map[string]any)
			generation["first_index"] = strconv.FormatUint(args.Start, 10)
			if args.Start != 0 {
				generation["reason"] = "packet_gap"
			}
			switch f.mode {
			case "bad_generation":
				generation["id"] = "2"
			case "bad_index":
				generation["first_index"] = strconv.FormatUint(args.Start+1, 10)
			case "too_many_packets":
				packets := generation["packets_hex"].([]string)
				generation["packets_hex"] = append(packets, packets[0])
			case "invalid_abi":
				capture["abi"].(map[string]any)["packet_size"] = 24
			case "wrong_source":
				different := capture["source"].(timemachine.Source)
				different.SourceID = "PRIVATE_OTHER_SOURCE"
				capture["source"] = different
			case "missing_batch":
				capture = nil
			case "wrong_request_seq":
				request.Seq++
			case "wrong_command":
				request.Command = "PRIVATE_UNEXPECTED_COMMAND"
			case "regressed_server_seq":
				serverSeq = 1
			}
			if err := reply(request, map[string]any{
				"status": status(), "batch": capture,
				"receipt": tracecontrol.Receipt{Operation: "read", Outcome: "read", CollectionMayContinue: true},
			}, true, ""); err != nil {
				return err
			}
		case "multi-dap/trace/status":
			current := status()
			if f.mode == "stale_generation" {
				current.NativeGeneration = 2
			} else {
				current.State, current.NeedsReconciliation = "faulted", true
				current.LastReceipt = tracecontrol.Receipt{
					Operation: "read", Outcome: "failed", NativeStateUncertain: true,
					HelperAbortRequested: true, CollectionMayContinue: true,
				}
			}
			if err := reply(request, map[string]any{"status": current}, true, ""); err != nil {
				return err
			}
		case "multi-dap/trace/close":
			if f.mode == "close_failure" {
				if err := reply(request, nil, false, "PRIVATE_CLOSE_FAILURE"); err != nil {
					return err
				}
				continue
			}
			current := status()
			current.State = "closed"
			receipt := tracecontrol.Receipt{
				Operation: "close", Outcome: "closed", NativeDisconnectConfirmed: true, CollectionMayContinue: true,
			}
			if f.mode == "inconsistent_close" {
				receipt.NativeDisconnectConfirmed = false
			}
			if err := reply(request, tracecontrol.Result{
				Status: current, Receipt: receipt,
			}, true, ""); err != nil {
				return err
			}
		case "disconnect":
			var args dap.DisconnectArguments
			if json.Unmarshal(request.Arguments, &args) != nil || args.TerminateDebuggee == nil ||
				*args.TerminateDebuggee || args.SuspendDebuggee == nil || *args.SuspendDebuggee {
				return errors.New("trace capture did not explicitly preserve target state on detach")
			}
			if err := reply(request, struct{}{}, true, ""); err != nil {
				return err
			}
			switch f.mode {
			case "trailing_frame":
				return write(dap.Envelope{
					Type: dap.TypeEvent, Event: "output", Body: json.RawMessage(`{"output":"PRIVATE_TRAILING_OUTPUT"}`),
				})
			case "no_eof":
				<-f.stop
			}
			return nil
		default:
			return errors.New("trace capture sent an unexpected or mutating command")
		}
	}
}

var traceCaptureHappyCommands = []string{
	"initialize", "attach", "configurationDone", "multi-dap/trace/open",
	"multi-dap/trace/poll", "multi-dap/trace/read", "multi-dap/trace/close", "disconnect",
}

func TestTraceCaptureAuthenticatedReadOnlyFlowWritesNormalizablePrivateCapture(t *testing.T) {
	for _, kind := range []string{"live", "snapshot"} {
		t.Run(kind, func(t *testing.T) {
			f := newTraceCaptureFixture(t, "")
			args := f.args()
			if kind == "live" {
				args = args[:len(args)-2]
			} else {
				args = append(args, "--source-kind", "snapshot", "--snapshot-sha256", strings.Repeat("a", 64))
			}
			var stdout bytes.Buffer
			if err := run(context.Background(), args, &stdout); err != nil {
				t.Fatal(err)
			}
			if got := f.commands(); !slices.Equal(got, traceCaptureHappyCommands) {
				t.Fatalf("wire commands = %v", got)
			}
			if f.entered.Load() != 1 || f.shutdowns.Load() != 0 || f.statusCalls.Load() != 0 {
				t.Fatal("capture bypassed the authenticated proxy or changed daemon lifetime")
			}
			var receipt struct {
				Event                     string `json:"event"`
				CaptureWritten            bool   `json:"capture_written"`
				NativeDisconnectConfirmed bool   `json:"native_disconnect_confirmed"`
				CollectionMayContinue     bool   `json:"collection_may_continue"`
				NativeRuntimeQualified    bool   `json:"native_runtime_qualified"`
				HardwareQualified         bool   `json:"hardware_qualified"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.Event != "native_trace_captured" ||
				!receipt.CaptureWritten || !receipt.NativeDisconnectConfirmed || !receipt.CollectionMayContinue ||
				receipt.NativeRuntimeQualified || receipt.HardwareQualified {
				t.Fatalf("capture receipt = %s (%v)", stdout.Bytes(), err)
			}
			data, err := os.ReadFile(f.output)
			if err != nil {
				t.Fatal(err)
			}
			capture, err := timemachine.DecodeCapture(bytes.NewReader(data))
			if err != nil || capture.Source.Kind != kind || capture.Source.SourceID != "PRIVATE_CAPTURE_SOURCE" ||
				len(capture.Generations[0].PacketsHex) != 3 {
				t.Fatalf("private output is not a complete capture: %v", err)
			}
			reportPath := filepath.Join(t.TempDir(), "normalized.json")
			if err := run(context.Background(), []string{
				"trace", "normalize", "--input", f.output, "--output", reportPath,
			}, io.Discard); err != nil {
				t.Fatalf("capture cannot be consumed by existing normalize: %v", err)
			}
			for _, secret := range []string{"PRIVATE_", f.config, f.output, f.directory} {
				if strings.Contains(stdout.String(), secret) {
					t.Fatal("capture printed private data")
				}
			}
		})
	}
}

func TestTraceCaptureRejectsInvalidOrIncompleteProtocolWithoutSavingBatch(t *testing.T) {
	for _, mode := range []string{
		"partial", "oversize", "bad_generation", "bad_index", "too_many_packets", "invalid_abi",
		"wrong_source", "missing_batch", "wrong_request_seq", "wrong_command", "regressed_server_seq",
		"qualified_status", "event_flood", "active_frontend", "bad_token",
	} {
		t.Run(mode, func(t *testing.T) {
			f := newTraceCaptureFixture(t, mode)
			var stdout bytes.Buffer
			err := run(context.Background(), f.args(), &stdout)
			if err == nil || stdout.Len() != 0 {
				t.Fatal("invalid capture emitted a success receipt")
			}
			data, readErr := os.ReadFile(f.output)
			if readErr != nil || len(data) != 0 {
				t.Fatal("invalid capture saved partial native packets")
			}
			if strings.Contains(err.Error(), "PRIVATE_") || strings.Contains(err.Error(), f.output) ||
				strings.Contains(err.Error(), f.config) || strings.Contains(err.Error(), f.directory) {
				t.Fatalf("capture error disclosed private data: %v", err)
			}
			if slices.Contains(f.commands(), "multi-dap/trace/close") {
				t.Fatal("capture explicitly closed an uncertain native state")
			}
			if mode == "active_frontend" && (!strings.Contains(err.Error(), "already active") ||
				!slices.Equal(f.commands(), []string{"initialize"})) {
				t.Fatal("capture did not clearly reject the existing editor frontend")
			}
			if mode == "bad_token" && (f.entered.Load() != 0 || !errors.Is(err, control.ErrStaleRecord)) {
				t.Fatal("capture bypassed control authentication")
			}
			if f.shutdowns.Load() != 0 {
				t.Fatal("failed capture shut down the daemon")
			}
		})
	}
}

func TestTraceCaptureFailureDistinguishesHealthyCloseFromUnknownNativeState(t *testing.T) {
	for _, mode := range []string{"read_failure", "stale_generation", "close_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newTraceCaptureFixture(t, mode)
			var stdout bytes.Buffer
			err := run(context.Background(), f.args(), &stdout)
			if err == nil || stdout.Len() != 0 || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatalf("capture failure = %v, stdout=%q", err, stdout.String())
			}
			want := append([]string{}, traceCaptureHappyCommands[:6]...)
			if mode != "close_failure" {
				want = append(want, "multi-dap/trace/status")
			}
			if mode != "read_failure" {
				want = append(want, "multi-dap/trace/close")
			}
			want = append(want, "disconnect")
			if got := f.commands(); !slices.Equal(got, want) {
				t.Fatalf("failure cleanup commands = %v, want %v", got, want)
			}
			if confirmed := strings.Contains(err.Error(), "native disconnect confirmed"); confirmed != (mode == "stale_generation") {
				t.Fatalf("capture misrepresented native cleanup: %v", err)
			}
			if !strings.Contains(err.Error(), "collection may continue") {
				t.Fatal("capture implied that native collection was stopped")
			}
			if data, readErr := os.ReadFile(f.output); readErr != nil || len(data) != 0 {
				t.Fatal("failed capture committed an output")
			}
		})
	}
}

func TestTraceCaptureDeadlineInterruptsPartialResponseAndDetaches(t *testing.T) {
	f := newTraceCaptureFixture(t, "timeout")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var stdout bytes.Buffer
	start := time.Now()
	err := run(ctx, f.args(), &stdout)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second ||
		stdout.Len() != 0 || slices.Contains(f.commands(), "multi-dap/trace/close") {
		t.Fatalf("bounded partial-response timeout = %v", err)
	}
	if data, readErr := os.ReadFile(f.output); readErr != nil || len(data) != 0 {
		t.Fatal("timed-out capture wrote a partial batch")
	}
}

func TestTraceCaptureRejectsInconsistentCloseAndIncompleteProxyShutdown(t *testing.T) {
	for _, mode := range []string{"inconsistent_close", "trailing_frame", "no_eof"} {
		t.Run(mode, func(t *testing.T) {
			f := newTraceCaptureFixture(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			var stdout bytes.Buffer
			start := time.Now()
			err := run(ctx, f.args(), &stdout)
			if err == nil || stdout.Len() != 0 || time.Since(start) > 3*time.Second ||
				strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatalf("incomplete shutdown was accepted or disclosed: %v", err)
			}
			want := traceCaptureHappyCommands
			if mode == "inconsistent_close" {
				want = want[:len(want)-1]
			}
			if got := f.commands(); !slices.Equal(got, want) {
				t.Fatalf("shutdown failure retried or changed state: commands=%v", got)
			}
			if confirmed := strings.Contains(err.Error(), "native disconnect confirmed"); confirmed == (mode == "inconsistent_close") {
				t.Fatalf("shutdown failure lost the actual native receipt: %v", err)
			}
			if mode == "no_eof" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("missing EOF was not deadline bounded: %v", err)
			}
			if data, readErr := os.ReadFile(f.output); readErr != nil || len(data) != 0 {
				t.Fatal("incomplete shutdown committed the captured packets")
			}
		})
	}
}

func TestTraceCaptureChecksConfigurationBindingAndOutputExclusivityBeforeProxy(t *testing.T) {
	for _, mode := range []string{"config_mismatch", "existing_output"} {
		t.Run(mode, func(t *testing.T) {
			f := newTraceCaptureFixture(t, mode)
			if mode == "existing_output" {
				if err := os.WriteFile(f.output, []byte("keep-private-existing-output"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout bytes.Buffer
			err := run(context.Background(), f.args(), &stdout)
			if err == nil || stdout.Len() != 0 || f.entered.Load() != 0 {
				t.Fatal("capture bypassed local binding/exclusivity admission")
			}
			if mode == "config_mismatch" {
				if !errors.Is(err, control.ErrConfigurationMismatch) {
					t.Fatalf("configuration mismatch lost its sentinel: %v", err)
				}
				if _, err := os.Stat(f.output); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("configuration mismatch created an output")
				}
			} else if data, readErr := os.ReadFile(f.output); readErr != nil || string(data) != "keep-private-existing-output" {
				t.Fatal("existing output was overwritten")
			}
		})
	}
}

func TestTraceCaptureFlagsFailClosedWithoutAccessingDaemon(t *testing.T) {
	base := []string{
		"--config", "PRIVATE_MISSING_CONFIG.toml", "--output", filepath.Join(t.TempDir(), "capture.json"),
		"--session-id", "17", "--source-id", "PRIVATE_SOURCE", "--acknowledge-unverified-source",
	}
	for _, invalid := range [][]string{
		{"--session-id", "0"}, {"--session-id", "2147483648"}, {"--session-id", "+17"},
		{"--session-id", "017"}, {"--source-id", ""}, {"--source-id", "bad\nlabel"},
		{"--acknowledge-unverified-source=false"}, {"--source-kind", "synthetic_fixture"},
		{"--source-kind", "snapshot"}, {"--image-sha256", "PRIVATE_BAD_DIGEST"},
		{"--snapshot-sha256", strings.Repeat("a", 64)}, {"--start", "01"}, {"--start", "+1"},
		{"--start", "18446744073709551615"}, {"--count", "0"}, {"--count", "4097"},
		{"--core-id", "-1"}, {"--core-id", "01"}, {"--output", "relative.json"},
		{"--config", ""}, {"--unknown", "PRIVATE_BAD_FLAG"},
	} {
		args := append(append([]string{}, base...), invalid...)
		if _, err := traceCaptureFlags(args); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatalf("invalid trace options were accepted or exposed: %v", invalid)
		}
	}
	options, err := traceCaptureFlags(base)
	if err != nil || options.count != 4096 || options.start != 0 || options.open.CoreID != nil {
		t.Fatalf("capture defaults = %#v, %v", options, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer
	if err := runTraceCapture(ctx, base, &stdout); !errors.Is(err, context.Canceled) || stdout.Len() != 0 {
		t.Fatalf("cancelled capture = %v", err)
	}
}
