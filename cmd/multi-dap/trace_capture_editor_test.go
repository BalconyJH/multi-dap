package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

const editorCaptureID = "0123456789abcdef0123456789abcdef"

type editorCaptureExchange struct {
	receipt  traceEditorResult
	commands []string
	err      error
}

func editorCaptureArgs(f *traceCaptureFixture) []string {
	args := f.args()
	args[1] = "capture-editor"
	return append(args, "--capture-id", editorCaptureID)
}

func exerciseEditorCapture(t *testing.T, f *traceCaptureFixture, args []string, mode string) (editorCaptureExchange, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if mode == "timeout" || mode == "no_eof" {
		data, err := os.ReadFile(f.config)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.ReplaceAll(data, []byte(`rpc_deadline = "3s"`), []byte(`rpc_deadline = "100ms"`))
		if err := os.WriteFile(f.config, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input, replies := io.Pipe()
	requests, output := io.Pipe()
	defer input.Close()
	defer replies.Close()
	defer requests.Close()
	defer output.Close()
	serverDone := make(chan editorCaptureExchange, 1)
	go func() {
		serverDone <- serveEditorCapture(f, requests, replies, mode)
	}()
	err := runWithIO(ctx, args, input, output)
	_ = output.Close()
	select {
	case exchange := <-serverDone:
		if exchange.err != nil {
			t.Fatalf("synthetic editor: %v", exchange.err)
		}
		if f.entered.Load() != 0 || f.statusCalls.Load() != 0 || f.shutdowns.Load() != 0 {
			t.Fatal("editor capture contacted the separate control frontend")
		}
		return exchange, err
	case <-time.After(time.Second):
		t.Fatal("synthetic editor did not exit")
		return editorCaptureExchange{}, err
	}
}

func serveEditorCapture(f *traceCaptureFixture, input io.Reader, replies *io.PipeWriter, mode string) editorCaptureExchange {
	defer replies.Close()
	var exchange editorCaptureExchange
	reader := bufio.NewReader(input)
	next := func() (map[string]json.RawMessage, error) {
		line, err := readTraceEditorLine(reader)
		if err != nil {
			return nil, err
		}
		var frame map[string]json.RawMessage
		err = json.Unmarshal(line, &frame)
		return frame, err
	}
	ready, err := next()
	if err != nil || string(ready["type"]) != `"ready"` || len(ready) != 3 {
		exchange.err = errors.New("missing ready frame")
		return exchange
	}
	if info, err := os.Stat(f.output); err != nil || info.Size() != 0 {
		exchange.err = errors.New("private output was not reserved before ready")
		return exchange
	}
	var source dap.NativeTraceOpenArguments
	var sequence int64
	generation := uint64(1)
	if mode == "large_uint" {
		generation = ^uint64(0) - 1
	}
	status := func() tracecontrol.Status {
		return tracecontrol.Status{
			Handle: 7, State: "open", NativeGeneration: generation, PacketCount: 3, PacketCountKnown: true,
			Timebase: "picoseconds", TraceState: "unknown", SourceBinding: "operator_declared",
			LastReceipt: tracecontrol.Receipt{Operation: "status", Outcome: "observed", CollectionMayContinue: true},
		}
	}
	for {
		frame, err := next()
		if err != nil {
			if err != io.EOF {
				exchange.err = err
			}
			return exchange
		}
		if string(frame["type"]) == `"result"` {
			raw, _ := json.Marshal(frame)
			exchange.err = json.Unmarshal(raw, &exchange.receipt)
			if _, err := next(); err != io.EOF {
				exchange.err = errors.New("extra output after terminal receipt")
			}
			return exchange
		}
		sequence++
		var command string
		var id int64
		if json.Unmarshal(frame["command"], &command) != nil ||
			json.Unmarshal(frame["id"], &id) != nil || id != sequence ||
			string(frame["protocol"]) != `"`+traceEditorProtocol+`"` ||
			string(frame["captureId"]) != `"`+editorCaptureID+`"` ||
			string(frame["type"]) != `"request"` || len(frame) != 6 || !traceEditorCommand(command) {
			exchange.err = errors.New("unexpected editor request")
			return exchange
		}
		exchange.commands = append(exchange.commands, command)
		var body any
		success := true
		switch command {
		case "multi-dap/trace/status":
			current := tracecontrol.Status{State: "idle", SourceBinding: "operator_declared"}
			if len(exchange.commands) > 1 {
				current = status()
				if mode != "read_failure_healthy" {
					current.State, current.NeedsReconciliation = "faulted", true
					current.LastReceipt.NativeStateUncertain = true
				}
			} else if mode == "occupied" || mode == "faulted" {
				current = status()
				if mode == "faulted" {
					current.State, current.NeedsReconciliation = "faulted", true
					current.LastReceipt.NativeStateUncertain = true
				}
			}
			body = map[string]any{"status": current}
		case "multi-dap/trace/open":
			if json.Unmarshal(frame["arguments"], &source) != nil || !source.AcknowledgeUnverifiedSource {
				exchange.err = errors.New("source admission was lost")
				return exchange
			}
			body = tracecontrol.Result{Status: status(), Receipt: tracecontrol.Receipt{Operation: "open", Outcome: "observed"}}
		case "multi-dap/trace/poll":
			body = tracecontrol.Result{Status: status(), Receipt: tracecontrol.Receipt{Operation: "status", Outcome: "observed"}}
		case "multi-dap/trace/read":
			var request dap.NativeTraceReadArguments
			if json.Unmarshal(frame["arguments"], &request) != nil || request.Generation != generation || request.Handle != 7 {
				exchange.err = errors.New("integer or handle identity was lost")
				return exchange
			}
			if mode == "timeout" {
				continue
			}
			if strings.HasPrefix(mode, "read_failure") {
				success, body = false, map[string]any{}
				break
			}
			capture := syntheticNativeCapture(3)
			capture["source"] = timemachine.Source{
				Kind: source.SourceKind, SessionID: source.SessionID, SourceID: source.SourceID, CoreID: source.CoreID,
				ImageSHA256: source.ImageSHA256, SnapshotSHA256: source.SnapshotSHA256,
			}
			g := capture["generations"].([]any)[0].(map[string]any)
			g["id"], g["first_index"] = strconv.FormatUint(generation, 10), strconv.FormatUint(request.Start, 10)
			if request.Start != 0 {
				g["reason"] = "packet_gap"
			}
			switch mode {
			case "wrong_source":
				other := capture["source"].(timemachine.Source)
				other.SourceID = "PRIVATE_OTHER_SOURCE"
				capture["source"] = other
			case "wrong_generation":
				g["id"] = "2"
			case "wrong_index":
				g["first_index"] = "1"
			case "too_many":
				packets := g["packets_hex"].([]string)
				g["packets_hex"] = append(packets, packets[0])
			}
			body = map[string]any{
				"status": status(), "batch": capture,
				"receipt": tracecontrol.Receipt{Operation: "read", Outcome: "read"},
			}
		case "multi-dap/trace/close":
			current := status()
			current.State = "closed"
			receipt := tracecontrol.Receipt{Operation: "close", Outcome: "closed", NativeDisconnectConfirmed: true}
			if mode == "contradictory_close" {
				receipt.NativeStateUncertain = true
			} else if mode == "contradictory_status" {
				current.LastReceipt.NativeStateUncertain = true
			} else if mode == "qualified_close" {
				current.HardwareQualified = true
			}
			body = tracecontrol.Result{Status: current, Receipt: receipt}
		default:
			exchange.err = errors.New("ordinary or mutating DAP request")
			return exchange
		}
		response := map[string]any{
			"protocol": traceEditorProtocol, "type": "response", "captureId": editorCaptureID,
			"id": id, "command": command, "success": success, "body": body,
		}
		if command == "multi-dap/trace/read" {
			switch mode {
			case "wrong_id":
				response["id"] = id + 1
			case "wrong_command":
				response["command"] = "multi-dap/trace/start"
			case "wrong_capture":
				response["captureId"] = strings.Repeat("a", 32)
			case "unknown_field":
				response["PRIVATE_UNEXPECTED"] = "PRIVATE_TEXT"
			}
		}
		data, _ := json.Marshal(response)
		if mode == "duplicate_key" && command == "multi-dap/trace/read" {
			data = append([]byte(`{"id":1,`), data[1:]...)
		}
		if mode == "oversize" && command == "multi-dap/trace/read" {
			data = bytes.Repeat([]byte{' '}, traceEditorMaxLine+1)
		}
		if mode == "invalid_utf8" && command == "multi-dap/trace/read" {
			data = append(data, 0xff)
		}
		data = append(data, '\n')
		if mode == "trailing" && command == "multi-dap/trace/close" {
			data = append(data, []byte("{}\n")...)
		}
		if mode == "fragmented" {
			for _, b := range data {
				if _, err := replies.Write([]byte{b}); err != nil {
					return exchange
				}
			}
		} else if _, err := replies.Write(data); err != nil {
			// A size/protocol rejection can close the input before an oversized
			// write finishes. The result still needs to be read below.
			if mode != "oversize" {
				return exchange
			}
		}
		if command == "multi-dap/trace/close" && mode != "no_eof" {
			_ = replies.Close()
		}
	}
}

func TestTraceCaptureEditorUsesExistingFrontendAndPrivateWriter(t *testing.T) {
	for _, mode := range []string{"happy", "fragmented", "large_uint", "snapshot"} {
		t.Run(mode, func(t *testing.T) {
			f := newTraceCaptureFixture(t, "")
			args := editorCaptureArgs(f)
			if mode == "large_uint" {
				args = append(args, "--start", "18446744073709551600")
			}
			if mode == "snapshot" {
				args = append(args, "--source-kind", "snapshot", "--snapshot-sha256", strings.Repeat("a", 64))
			}
			got, err := exerciseEditorCapture(t, f, args, mode)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"multi-dap/trace/status", "multi-dap/trace/open", "multi-dap/trace/poll", "multi-dap/trace/read", "multi-dap/trace/close"}
			if !reflect.DeepEqual(got.commands, want) {
				t.Fatalf("editor commands = %v", got.commands)
			}
			if !got.receipt.CaptureWritten || !got.receipt.NativeDisconnectConfirmed ||
				!got.receipt.NativeAttempted || got.receipt.NeedsReconciliation ||
				got.receipt.FailureStage != "none" || got.receipt.NativeRuntimeQualified || got.receipt.HardwareQualified {
				t.Fatalf("receipt = %#v", got.receipt)
			}
			file, err := os.Open(f.output)
			if err != nil {
				t.Fatal(err)
			}
			capture, err := timemachine.DecodeCapture(file)
			_ = file.Close()
			if err != nil || len(capture.Generations) != 1 || len(capture.Generations[0].PacketsHex) != 3 {
				t.Fatalf("capture = %#v, %v", capture, err)
			}
		})
	}
}

func TestTraceCaptureEditorRejectsUncertainAndMalformedResults(t *testing.T) {
	for _, mode := range []string{
		"occupied", "faulted", "wrong_source", "wrong_generation", "wrong_index", "too_many",
		"wrong_id", "wrong_command", "wrong_capture", "unknown_field", "duplicate_key",
		"invalid_utf8", "contradictory_close", "contradictory_status", "qualified_close", "trailing", "no_eof", "timeout",
		"read_failure_faulted", "read_failure_healthy",
	} {
		t.Run(mode, func(t *testing.T) {
			f := newTraceCaptureFixture(t, "")
			got, err := exerciseEditorCapture(t, f, editorCaptureArgs(f), mode)
			if err == nil || got.receipt.CaptureWritten || got.receipt.FailureStage == "none" {
				t.Fatalf("unsafe success: %#v, %v", got.receipt, err)
			}
			if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), f.output) {
				t.Fatalf("private diagnostics escaped: %v", err)
			}
			data, readErr := os.ReadFile(f.output)
			if readErr != nil || len(data) != 0 {
				t.Fatalf("failed capture persisted a batch: %d, %v", len(data), readErr)
			}
			closed := mode == "read_failure_healthy" || mode == "trailing" || mode == "no_eof"
			if got.receipt.NativeDisconnectConfirmed != closed {
				t.Fatalf("close confirmation = %v, want %v", got.receipt.NativeDisconnectConfirmed, closed)
			}
			if mode == "occupied" || mode == "faulted" {
				if len(got.commands) != 1 || got.receipt.NativeAttempted {
					t.Fatal("preflight took ownership of an occupied source")
				}
			} else if !closed && !got.receipt.NeedsReconciliation {
				t.Fatal("uncertain native operation lost its reconciliation receipt")
			}
		})
	}
}

func TestTraceCaptureEditorAdmissionDoesNotOpenOutputOrTransport(t *testing.T) {
	f := newTraceCaptureFixture(t, "")
	base := editorCaptureArgs(f)
	for _, extra := range [][]string{
		{"--capture-id", "PRIVATE_NONCE"},
		{"--capture-id", strings.Repeat("A", 32)},
		{"--acknowledge-unverified-source=false"},
		{"--core-id", "8"},
		{"--count", "4097"},
		{"--start", "18446744073709551615"},
	} {
		var output bytes.Buffer
		err := runWithIO(context.Background(), append(append([]string(nil), base...), extra...), strings.NewReader(""), &output)
		if err == nil || output.Len() != 0 {
			t.Fatalf("admission %v = %v, output %q", extra, err, output.String())
		}
		if _, err := os.Stat(f.output); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid options reserved output")
		}
	}
	if err := os.WriteFile(f.output, []byte("preserve existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runWithIO(context.Background(), base, strings.NewReader(""), &output); err == nil || output.Len() != 0 {
		t.Fatal("existing output entered editor protocol")
	}
	data, _ := os.ReadFile(f.output)
	if string(data) != "preserve existing" || f.entered.Load() != 0 {
		t.Fatal("output exclusivity or frontend ownership was bypassed")
	}
}

func TestTraceEditorTransportBoundsAndAllowlist(t *testing.T) {
	for _, command := range []string{"initialize", "attach", "disconnect", "continue", "multi-dap/trace/start", "multi-dap/trace/stop"} {
		var output bytes.Buffer
		c := newTraceEditorTransport(context.Background(), editorCaptureID, strings.NewReader(""), &output)
		_, err := c.request(command, struct{}{}, time.Second)
		c.close()
		if err == nil || output.Len() != 0 {
			t.Fatalf("forbidden request %q reached editor", command)
		}
	}
	for _, line := range []string{
		strings.Repeat(" ", traceEditorMaxLine+1) + "\n",
		"{}\xff\n", "{}",
	} {
		if _, err := readTraceEditorLine(bufio.NewReader(strings.NewReader(line))); err == nil {
			t.Fatalf("invalid frame length %d accepted", len(line))
		}
	}
	for _, body := range []string{`{"x":1,"x":2}`, `{"x":{"y":1,"y":2}}`, strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70)} {
		if traceEditorUniqueJSON([]byte(body)) {
			t.Fatal("ambiguous/deep JSON was accepted")
		}
	}
	if !strings.Contains(usage(), "trace capture-editor") {
		t.Fatal("editor helper is absent from usage")
	}
}

func TestTraceEditorTransportRejectsOversizedBodiesArgumentsAndBudgets(t *testing.T) {
	response := func(body json.RawMessage) string {
		data, err := json.Marshal(map[string]any{
			"protocol": traceEditorProtocol, "type": "response", "captureId": editorCaptureID,
			"id": 1, "command": "multi-dap/trace/status", "success": true, "body": body,
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	hugeBody := json.RawMessage(`{"padding":"` + strings.Repeat("a", dap.MaxNativeTraceBodyLength) + `"}`)
	for _, test := range []struct {
		name       string
		input      string
		args       any
		messages   int
		wantOutput bool
	}{
		{"body", response(hugeBody), struct{}{}, 0, true},
		{"line", strings.Repeat(" ", traceEditorMaxLine+1) + "\n", struct{}{}, 0, true},
		{"arguments", "", map[string]string{"padding": strings.Repeat("a", dap.MaxNativeTraceArguments)}, 0, false},
		{"null_arguments", "", nil, 0, false},
		{"message_budget", response(json.RawMessage(`{}`)), struct{}{}, traceCaptureMaxMessages - 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			c := newTraceEditorTransport(context.Background(), editorCaptureID, strings.NewReader(test.input), &output)
			defer c.close()
			c.messages = test.messages
			_, err := c.request("multi-dap/trace/status", test.args, time.Second)
			if err == nil || c.transportHealthy() {
				t.Fatal("oversized or exhausted editor channel remained healthy")
			}
			if (output.Len() != 0) != test.wantOutput {
				t.Fatalf("unexpected output admission: %d bytes", output.Len())
			}
		})
	}
}

func TestTraceEditorTransportCancellationFencesLateReplies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, replies := io.Pipe()
	requests, output := io.Pipe()
	c := newTraceEditorTransport(ctx, editorCaptureID, input, output)
	defer c.close()
	defer requests.Close()
	defer output.Close()
	defer replies.Close()
	done := make(chan error, 1)
	go func() {
		_, err := c.request("multi-dap/trace/open", struct{}{}, time.Second)
		done <- err
	}()
	if _, err := readTraceEditorLine(bufio.NewReader(requests)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || c.transportHealthy() {
			t.Fatalf("cancelled request = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled helper request did not converge")
	}
	if _, err := c.request("multi-dap/trace/status", struct{}{}, time.Second); err == nil {
		t.Fatal("cancelled channel issued recovery RPC")
	}
	c.close()
	late := make(chan error, 1)
	go func() {
		_, err := replies.Write([]byte("{}\n"))
		late <- err
	}()
	select {
	case err := <-late:
		if err == nil {
			t.Fatal("closed capture input accepted a late generation")
		}
	case <-time.After(time.Second):
		t.Fatal("late response write remained stranded")
	}
}

func TestTraceCaptureEditorCancelledContextNeverReservesOutput(t *testing.T) {
	f := newTraceCaptureFixture(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := runWithIO(ctx, editorCaptureArgs(f), strings.NewReader(""), &output)
	if !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("cancelled admission = %v, output %q", err, output.String())
	}
	if _, err := os.Stat(f.output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled admission reserved an output")
	}
}

func TestTraceCaptureEditorStaticConfigDoesNotRequireControlRecord(t *testing.T) {
	f := newTraceCaptureFixture(t, "")
	args := append(editorCaptureArgs(f), "--control-dir", filepath.Join(t.TempDir(), "never-created"))
	got, err := exerciseEditorCapture(t, f, args, "happy")
	if err != nil || !got.receipt.CaptureWritten {
		t.Fatalf("capture required a separate control record: %v", err)
	}
}

func TestTraceEditorResultHasOnlyFixedMetadata(t *testing.T) {
	data, err := json.Marshal(traceEditorResult{Protocol: traceEditorProtocol, Type: "result", CaptureID: editorCaptureID, FailureStage: "none"})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := traceEditorFields(data, []string{
		"protocol", "type", "captureId", "capture_written", "native_attempted", "native_disconnect_confirmed",
		"needs_reconciliation", "collection_may_continue", "native_runtime_qualified", "hardware_qualified", "failure_stage",
	})
	if err != nil || len(fields) != 11 {
		t.Fatal(fmt.Errorf("terminal schema: %w", err))
	}
}
