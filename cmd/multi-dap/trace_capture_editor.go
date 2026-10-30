package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

const (
	traceEditorProtocol = "multi-dap.editor-trace.v1"
	traceEditorMaxLine  = 1 << 20
)

type traceEditorResult struct {
	Protocol                  string `json:"protocol"`
	Type                      string `json:"type"`
	CaptureID                 string `json:"captureId"`
	CaptureWritten            bool   `json:"capture_written"`
	NativeAttempted           bool   `json:"native_attempted"`
	NativeDisconnectConfirmed bool   `json:"native_disconnect_confirmed"`
	NeedsReconciliation       bool   `json:"needs_reconciliation"`
	CollectionMayContinue     bool   `json:"collection_may_continue"`
	NativeRuntimeQualified    bool   `json:"native_runtime_qualified"`
	HardwareQualified         bool   `json:"hardware_qualified"`
	FailureStage              string `json:"failure_stage"`
}

func runTraceCaptureEditor(ctx context.Context, args []string, input io.Reader, output io.Writer) (runErr error) {
	options, err := parseTraceCaptureFlags(args, true)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return &traceCaptureError{stage: "editor admission", cause: err}
	}
	// This is static admission only. The active editor owns authentication and
	// the DAP frontend; neither a control record nor another proxy is opened.
	validated, err := loadConfig(options.config)
	if err != nil {
		return &traceCaptureError{stage: "editor configuration", cause: err}
	}
	if options.open.CoreID != nil {
		found := false
		for _, core := range validated.Cores {
			found = found || uint64(core.ID) == uint64(*options.open.CoreID)
		}
		if !found {
			return usageError("trace capture-editor --core-id must name a configured core")
		}
	}
	file, err := createPerfFile(options.output)
	if err != nil {
		return &traceCaptureError{stage: "editor private output", cause: err}
	}
	fileClosed := false
	defer func() {
		if !fileClosed {
			if err := file.Close(); err != nil {
				runErr = errors.Join(runErr, &traceCaptureError{stage: "editor output close", cause: err, reserved: true})
			}
		}
	}()

	transport := newTraceEditorTransport(ctx, options.captureID, input, output)
	defer transport.close()
	session := &traceCaptureSession{
		transport: transport, rpcTimeout: traceCaptureDeadline(validated.Timing.RPCDeadline),
		openTimeout: traceCaptureDeadline(validated.Timing.StartupDeadline), stage: "editor admission",
	}
	receipt := traceEditorResult{
		Protocol: traceEditorProtocol, Type: "result", CaptureID: options.captureID, FailureStage: "none",
	}
	finish := func(cause error, stage string) error {
		receipt.NativeAttempted = session.nativeAttempted
		receipt.NativeDisconnectConfirmed = session.nativeClosed
		receipt.NeedsReconciliation = receipt.NeedsReconciliation ||
			session.nativeAttempted && !session.nativeClosed
		receipt.CollectionMayContinue = receipt.CollectionMayContinue || session.nativeAttempted
		if cause != nil {
			receipt.FailureStage = traceEditorFailureStage(stage, cause)
		}
		terminal, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		writeErr := transport.write(terminal, receipt)
		if cause != nil || writeErr != nil {
			return &traceCaptureError{
				stage: "editor " + receipt.FailureStage, cause: errors.Join(cause, writeErr), reserved: true,
				nativeAttempted: session.nativeAttempted, disconnectConfirmed: session.nativeClosed,
			}
		}
		return nil
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, session.rpcTimeout)
	err = transport.write(readyCtx, struct {
		Protocol  string `json:"protocol"`
		Type      string `json:"type"`
		CaptureID string `json:"captureId"`
	}{traceEditorProtocol, "ready", options.captureID})
	readyCancel()
	if err != nil {
		return finish(err, "transport")
	}
	initial, err := session.nativeRequest("status", struct{}{}, session.rpcTimeout)
	if err != nil {
		return finish(err, "status")
	}
	if !traceEditorIdle(initial) {
		receipt.NeedsReconciliation = initial.Status.NeedsReconciliation ||
			initial.Status.LastReceipt.NativeStateUncertain || initial.Status.LastReceipt.HelperAbortRequested
		receipt.CollectionMayContinue = initial.Status.State != "idle"
		return finish(errors.New("native source is unavailable or already occupied"), "admission")
	}

	capture, captureErr := session.capture(options)
	captureStage := session.stage
	cleanupErr := session.cleanup()
	if err := errors.Join(captureErr, cleanupErr); err != nil {
		if captureErr == nil {
			captureStage = session.stage
		}
		return finish(err, captureStage)
	}
	if !session.nativeClosed {
		return finish(errors.New("native disconnect was not confirmed"), "close")
	}
	// Native close is the final RPC in this mode. The editor ends stdin with
	// that response; requiring EOF fences duplicates and trailing replies
	// before the reserved artifact can become a successful capture.
	if err := transport.end(session.rpcTimeout); err != nil {
		return finish(err, "transport")
	}
	if err := ctx.Err(); err != nil {
		return finish(err, "cancelled")
	}
	err = finishAnalysisReport(file, capture)
	fileClosed = true
	if err != nil {
		return finish(err, "finalize")
	}
	receipt.CaptureWritten = true
	return finish(nil, "")
}

func traceEditorIdle(result traceCaptureNativeResult) bool {
	status := result.Status
	if len(result.Batch) != 0 || result.Receipt != (tracecontrol.Receipt{}) ||
		status.Busy || status.NeedsReconciliation || status.NativeRuntimeQualified || status.HardwareQualified ||
		status.SourceBinding != "operator_declared" || status.LastReceipt.NativeStateUncertain ||
		status.LastReceipt.HelperAbortRequested {
		return false
	}
	return status.State == "idle" && status.Handle == 0 ||
		status.State == "closed" && status.Handle > 0 && status.Handle <= math.MaxInt32 &&
			status.LastReceipt.NativeDisconnectConfirmed
}

func traceEditorFailureStage(stage string, cause error) string {
	if errors.Is(cause, context.Canceled) {
		return "cancelled"
	}
	switch stage {
	case "editor admission", "admission":
		return "admission"
	case "editor configuration", "configuration":
		return "configuration"
	case "output":
		return "output"
	case "native open":
		return "open"
	case "native poll":
		return "poll"
	case "native read":
		return "read"
	case "capture validation":
		return "validation"
	case "local native status", "status":
		return "status"
	case "native close", "close":
		return "close"
	case "finalize":
		return "finalize"
	case "cancelled":
		return "cancelled"
	default:
		return "transport"
	}
}

type traceEditorLine struct {
	data []byte
	err  error
}

type traceEditorTransport struct {
	ctx          context.Context
	cancel       context.CancelFunc
	captureID    string
	input        io.Reader
	output       io.Writer
	lines        chan traceEditorLine
	healthy      bool
	writeFailed  bool
	sequence     int64
	messages     int
	closeRequest bool
	closeOnce    sync.Once
}

func newTraceEditorTransport(ctx context.Context, captureID string, input io.Reader, output io.Writer) *traceEditorTransport {
	ctx, cancel := context.WithCancel(ctx)
	c := &traceEditorTransport{
		ctx: ctx, cancel: cancel, captureID: captureID, input: input, output: output,
		lines: make(chan traceEditorLine, 1), healthy: true,
	}
	go func() {
		defer close(c.lines)
		reader := bufio.NewReader(input)
		for {
			line, err := readTraceEditorLine(reader)
			select {
			case c.lines <- traceEditorLine{line, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *traceEditorTransport) close() {
	c.closeOnce.Do(func() {
		c.cancel()
		if input, ok := c.input.(io.Closer); ok {
			_ = input.Close()
		}
	})
}

func (c *traceEditorTransport) transportHealthy() bool { return c.healthy && c.ctx.Err() == nil }
func (c *traceEditorTransport) invalidateTransport()   { c.healthy = false }

func (c *traceEditorTransport) write(ctx context.Context, value any) error {
	if c.writeFailed || c.messages >= traceCaptureMaxMessages {
		return errors.New("editor output is unavailable")
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > traceEditorMaxLine {
		c.writeFailed = true
		return errors.New("invalid editor output")
	}
	c.messages++
	data = append(data, '\n')
	written := make(chan error, 1)
	go func() {
		n, err := c.output.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			c.writeFailed = true
		}
		return err
	case <-ctx.Done():
		// Do not start another write behind an uncertain blocked writer.
		c.writeFailed = true
		return ctx.Err()
	}
}

func (c *traceEditorTransport) next(ctx context.Context) ([]byte, error) {
	select {
	case line, ok := <-c.lines:
		if !ok {
			return nil, io.EOF
		}
		if line.err != nil {
			return nil, line.err
		}
		c.messages++
		if c.messages > traceCaptureMaxMessages {
			return nil, errors.New("editor message budget exceeded")
		}
		return line.data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *traceEditorTransport) request(command string, arguments any, timeout time.Duration) (json.RawMessage, error) {
	if !c.transportHealthy() || c.closeRequest || !traceEditorCommand(command) {
		return nil, errors.New("editor request is unavailable")
	}
	args, err := json.Marshal(arguments)
	if err != nil || len(args) > dap.MaxNativeTraceArguments || !traceEditorJSONObject(args) {
		c.healthy = false
		return nil, errors.New("invalid editor request arguments")
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	c.sequence++
	c.closeRequest = command == "multi-dap/trace/close"
	err = c.write(ctx, struct {
		Protocol  string          `json:"protocol"`
		Type      string          `json:"type"`
		CaptureID string          `json:"captureId"`
		ID        int64           `json:"id"`
		Command   string          `json:"command"`
		Arguments json.RawMessage `json:"arguments"`
	}{traceEditorProtocol, "request", c.captureID, c.sequence, command, args})
	var line []byte
	if err == nil {
		line, err = c.next(ctx)
	}
	if err != nil {
		c.healthy = false
		return nil, err
	}
	fields, err := traceEditorFields(line, []string{"protocol", "type", "captureId", "id", "command", "success", "body"})
	var protocol, kind, captureID, responseCommand string
	var id int64
	var success *bool
	if err != nil || json.Unmarshal(fields["protocol"], &protocol) != nil ||
		json.Unmarshal(fields["type"], &kind) != nil || json.Unmarshal(fields["captureId"], &captureID) != nil ||
		json.Unmarshal(fields["id"], &id) != nil || json.Unmarshal(fields["command"], &responseCommand) != nil ||
		json.Unmarshal(fields["success"], &success) != nil || success == nil ||
		protocol != traceEditorProtocol || kind != "response" || captureID != c.captureID ||
		id != c.sequence || responseCommand != command || len(fields["body"]) > dap.MaxNativeTraceBodyLength ||
		!traceEditorJSONObject(fields["body"]) {
		c.healthy = false
		return nil, errors.New("invalid editor response")
	}
	if err := ctx.Err(); err != nil {
		c.healthy = false
		return nil, err
	}
	if !*success {
		return nil, errors.New("editor native request rejected")
	}
	return fields["body"], nil
}

func (c *traceEditorTransport) end(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	_, err := c.next(ctx)
	if err == io.EOF {
		return nil
	}
	c.healthy = false
	return errors.New("editor stream did not end after native close")
}

func traceEditorCommand(command string) bool {
	switch command {
	case "multi-dap/trace/open", "multi-dap/trace/poll", "multi-dap/trace/read", "multi-dap/trace/status", "multi-dap/trace/close":
		return true
	default:
		return false
	}
}

func readTraceEditorLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > traceEditorMaxLine+1 {
			return nil, errors.New("editor frame exceeds size limit")
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF && len(line) != 0 {
				err = errors.New("editor frame is not newline terminated")
			}
			return nil, err
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		if !utf8.Valid(line) {
			return nil, errors.New("editor frame is not UTF-8")
		}
		return line, nil
	}
}

func traceEditorFields(data []byte, names []string) (map[string]json.RawMessage, error) {
	if !traceEditorJSONObject(data) || !traceEditorUniqueJSON(data) {
		return nil, errors.New("invalid editor envelope")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != len(names) {
		return nil, errors.New("invalid editor envelope fields")
	}
	for _, name := range names {
		if _, found := fields[name]; !found {
			return nil, errors.New("missing editor envelope field")
		}
	}
	return fields, nil
}

func traceEditorJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 1 && trimmed[0] == '{' && utf8.Valid(trimmed) && json.Valid(trimmed)
}

func traceEditorUniqueJSON(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch token {
		case json.Delim('{'):
			keys := make(map[string]bool)
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || keys[key] {
					return false
				}
				keys[key] = true
				if !value(depth + 1) {
					return false
				}
			}
			token, err = decoder.Token()
			return err == nil && token == json.Delim('}')
		case json.Delim('['):
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
			token, err = decoder.Token()
			return err == nil && token == json.Delim(']')
		default:
			return token != json.Delim('}') && token != json.Delim(']')
		}
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
