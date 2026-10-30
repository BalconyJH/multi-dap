package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

const traceCaptureMaxMessages = 128

type traceCaptureOptions struct {
	config, controlDir, output string
	open                       dap.NativeTraceOpenArguments
	start                      uint64
	count                      int
}

type traceCaptureError struct {
	stage               string
	cause               error
	reserved            bool
	nativeAttempted     bool
	disconnectConfirmed bool
}

func (e *traceCaptureError) Error() string {
	text := "trace capture: " + e.stage + " failed"
	if e.nativeAttempted {
		if e.disconnectConfirmed {
			text += "; native disconnect confirmed"
		} else {
			text += "; native state unconfirmed"
		}
		text += "; collection may continue"
	}
	if e.reserved {
		text += "; private output may be incomplete"
	}
	return text
}

func (e *traceCaptureError) Unwrap() error { return e.cause }

func traceCaptureFlags(args []string) (traceCaptureOptions, error) {
	var options traceCaptureOptions
	flags := flag.NewFlagSet("trace capture", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.config, "config", "", "existing daemon's project configuration")
	flags.StringVar(&options.controlDir, "control-dir", "", "private daemon control directory")
	flags.StringVar(&options.output, "output", "", "new absolute private capture path")
	flags.StringVar(&options.open.SourceKind, "source-kind", "live", "declared source kind: live or snapshot")
	flags.StringVar(&options.open.SourceID, "source-id", "", "operator-declared source identity")
	flags.StringVar(&options.open.ImageSHA256, "image-sha256", "", "optional declared image digest")
	flags.StringVar(&options.open.SnapshotSHA256, "snapshot-sha256", "", "required snapshot source digest")
	flags.BoolVar(&options.open.AcknowledgeUnverifiedSource, "acknowledge-unverified-source", false, "acknowledge unverified native source binding")
	sessionID := flags.String("session-id", "", "explicit nonzero native session ID")
	coreID := flags.String("core-id", "", "optional declared configured core ID")
	start := flags.String("start", "0", "canonical decimal first packet index")
	flags.IntVar(&options.count, "count", dap.MaxNativeTracePackets, "maximum packets in this single batch")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return options, usageError("invalid trace capture options")
	}
	if strings.TrimSpace(options.config) == "" {
		return options, usageError("trace capture requires --config for an already-running daemon")
	}
	if !filepath.IsAbs(options.output) {
		return options, usageError("trace capture --output requires a new absolute private file")
	}
	id, err := traceCaptureUint(*sessionID, 31)
	if err != nil || id == 0 {
		return options, usageError("trace capture --session-id must be an explicit integer in 1..2147483647")
	}
	options.open.SessionID = uint32(id)
	if *coreID != "" {
		id, err := traceCaptureUint(*coreID, 32)
		if err != nil {
			return options, usageError("trace capture --core-id must be a canonical uint32")
		}
		value := uint32(id)
		options.open.CoreID = &value
	}
	options.start, err = traceCaptureUint(*start, 64)
	if err != nil {
		return options, usageError("trace capture --start must be a canonical uint64 decimal string")
	}
	if err := dap.ValidateNativeTraceAction(dap.Action{Kind: dap.ActionTraceOpen, TraceOpen: options.open}); err != nil {
		return options, usageError("trace capture requires valid source declarations and --acknowledge-unverified-source")
	}
	if err := dap.ValidateNativeTraceAction(dap.Action{
		Kind: dap.ActionTraceRead, TraceRead: dap.NativeTraceReadArguments{
			Handle: 1, Start: options.start, Generation: 1, Count: options.count,
		},
	}); err != nil {
		return options, usageError("trace capture --count must be in 1..4096 without overflowing --start")
	}
	return options, nil
}

func traceCaptureUint(text string, bits int) (uint64, error) {
	value, err := strconv.ParseUint(text, 10, bits)
	if err != nil || strconv.FormatUint(value, 10) != text {
		return 0, errors.New("invalid canonical unsigned integer")
	}
	return value, nil
}

func runTraceCapture(ctx context.Context, args []string, stdout io.Writer) (runErr error) {
	options, err := traceCaptureFlags(args)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return &traceCaptureError{stage: "admission", cause: err}
	}
	validated, digest, store, err := controlCommandFlags("trace capture", []string{
		"--config", options.config, "--control-dir", options.controlDir,
	})
	if err != nil {
		return &traceCaptureError{stage: "configuration binding", cause: err}
	}
	if options.open.CoreID != nil {
		found := false
		for _, core := range validated.Cores {
			found = found || uint64(core.ID) == uint64(*options.open.CoreID)
		}
		if !found {
			return usageError("trace capture --core-id must name a configured core; this does not verify native binding")
		}
	}
	record, err := store.LoadForConfig(validated.Probe.ID, digest)
	if err != nil {
		return &traceCaptureError{stage: "existing configuration-bound daemon lookup", cause: err}
	}
	file, err := createPerfFile(options.output)
	if err != nil {
		return &traceCaptureError{stage: "exclusive private output creation", cause: err}
	}
	fileClosed := false
	defer func() {
		if !fileClosed {
			if err := file.Close(); err != nil {
				runErr = errors.Join(runErr, &traceCaptureError{stage: "output close", cause: err, reserved: true})
			}
		}
	}()
	client := newTraceCaptureDAP(ctx, record)
	defer client.abortAndWait()
	client.rpcTimeout = traceCaptureDeadline(validated.Timing.RPCDeadline)
	client.openTimeout = traceCaptureDeadline(validated.Timing.StartupDeadline)
	capture, captureErr := client.capture(options)
	captureStage := client.stage
	cleanupErr := client.cleanup()
	if err := errors.Join(captureErr, cleanupErr); err != nil {
		stage := client.stage
		if captureErr != nil {
			stage = captureStage
		}
		return &traceCaptureError{
			stage: stage, cause: err, reserved: true,
			nativeAttempted: client.nativeAttempted, disconnectConfirmed: client.nativeClosed,
		}
	}
	if err := ctx.Err(); err != nil {
		return &traceCaptureError{
			stage: "output admission", cause: err, reserved: true,
			nativeAttempted: client.nativeAttempted, disconnectConfirmed: client.nativeClosed,
		}
	}
	err = finishAnalysisReport(file, capture)
	fileClosed = true
	if err != nil {
		return &traceCaptureError{
			stage: "output finalization", cause: err, reserved: true,
			nativeAttempted: client.nativeAttempted, disconnectConfirmed: client.nativeClosed,
		}
	}
	if err := json.NewEncoder(stdout).Encode(struct {
		Event                     string `json:"event"`
		CaptureWritten            bool   `json:"capture_written"`
		NativeDisconnectConfirmed bool   `json:"native_disconnect_confirmed"`
		CollectionMayContinue     bool   `json:"collection_may_continue"`
		NativeRuntimeQualified    bool   `json:"native_runtime_qualified"`
		HardwareQualified         bool   `json:"hardware_qualified"`
	}{
		Event: "native_trace_captured", CaptureWritten: true,
		NativeDisconnectConfirmed: client.nativeClosed, CollectionMayContinue: true,
	}); err != nil {
		return &traceCaptureError{stage: "receipt write", cause: err, nativeAttempted: true, disconnectConfirmed: client.nativeClosed}
	}
	return nil
}

func traceCaptureDeadline(configured time.Duration) time.Duration {
	// Configuration can permit arbitrarily long debugger calls. This small
	// client remains bounded independently, including unsolicited events.
	if configured >= 29*time.Second {
		return 30 * time.Second
	}
	return configured + time.Second
}

type traceCaptureDAP struct {
	ctx         context.Context
	cancel      context.CancelFunc
	input       *io.PipeWriter
	output      *io.PipeReader
	encoder     *dap.Encoder
	decoder     *dap.Decoder
	done        chan error
	stopOnce    sync.Once
	proxyWaited bool
	proxyErr    error
	rpcTimeout  time.Duration
	openTimeout time.Duration
	sequence    int64
	serverSeq   int64
	messages    int
	stage       string
	healthy     bool
	attached    bool

	handle          uint64
	nativeAttempted bool
	nativeHealthy   bool
	closeAttempted  bool
	nativeClosed    bool
}

func newTraceCaptureDAP(ctx context.Context, record control.Record) *traceCaptureDAP {
	proxyContext, cancel := context.WithCancel(ctx)
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	c := &traceCaptureDAP{
		ctx: proxyContext, cancel: cancel, input: writer, output: reader, done: make(chan error, 1),
		encoder: dap.NewEncoder(writer), decoder: dap.NewDecoder(reader), healthy: true,
	}
	go func() {
		err := control.Proxy(proxyContext, record, input, output)
		_ = input.CloseWithError(err)
		_ = output.CloseWithError(err)
		c.done <- err
	}()
	return c
}

func (c *traceCaptureDAP) stop() {
	c.stopOnce.Do(func() {
		c.cancel()
		_ = c.input.Close()
		_ = c.output.Close()
	})
}

func (c *traceCaptureDAP) abortAndWait() {
	c.stop()
	if !c.proxyWaited {
		select {
		case c.proxyErr = <-c.done:
			c.proxyWaited = true
		case <-time.After(time.Second):
		}
	}
}

func (c *traceCaptureDAP) phase(timeout time.Duration, action func() error) error {
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	stop := context.AfterFunc(ctx, c.stop)
	err := action()
	stop()
	if ctx.Err() != nil {
		err, c.healthy = ctx.Err(), false
	}
	cancel()
	return err
}

func (c *traceCaptureDAP) send(command string, arguments any) (int64, error) {
	body, err := json.Marshal(arguments)
	if err != nil {
		return 0, err
	}
	c.sequence++
	if err := c.encoder.Encode(dap.Envelope{
		Seq: c.sequence, Type: dap.TypeRequest, Command: command, Arguments: body,
	}); err != nil {
		c.healthy = false
		return 0, err
	}
	return c.sequence, nil
}

func (c *traceCaptureDAP) next() (dap.Envelope, error) {
	message, err := c.decoder.Decode()
	if err != nil {
		c.healthy = false
		return dap.Envelope{}, err
	}
	c.messages++
	if c.messages > traceCaptureMaxMessages || message.Seq <= c.serverSeq ||
		(message.Type != dap.TypeEvent && message.Type != dap.TypeResponse) ||
		message.Event == "terminated" || message.Event == "exited" {
		c.healthy = false
		return dap.Envelope{}, errors.New("invalid or expired DAP stream")
	}
	c.serverSeq = message.Seq
	return message, nil
}

func (c *traceCaptureDAP) response(sequence int64, command string) (json.RawMessage, error) {
	for {
		message, err := c.next()
		if err != nil {
			return nil, err
		}
		if message.Type == dap.TypeEvent {
			continue
		}
		if message.RequestSeq != sequence || message.Command != command || message.Success == nil {
			c.healthy = false
			return nil, errors.New("unexpected DAP response identity")
		}
		if !*message.Success {
			c.nativeHealthy = false
			if message.Message == "another DAP client is already active" {
				c.stage = "capture admission (another DAP client is already active)"
			}
			return nil, errors.New("DAP request rejected")
		}
		return message.Body, nil
	}
}

func (c *traceCaptureDAP) request(command string, arguments any, timeout time.Duration) (json.RawMessage, error) {
	var body json.RawMessage
	err := c.phase(timeout, func() error {
		sequence, err := c.send(command, arguments)
		if err == nil {
			body, err = c.response(sequence, command)
		}
		return err
	})
	return body, err
}

func (c *traceCaptureDAP) capture(options traceCaptureOptions) (timemachine.Capture, error) {
	fail := func(err error) (timemachine.Capture, error) { return timemachine.Capture{}, err }
	c.stage = "DAP initialization"
	body, err := c.request("initialize", dap.InitializeArguments{
		AdapterID: "multi-dap", ClientID: "multi-dap-trace-capture", PathFormat: "path",
		LinesStartAt1: true, ColumnsStartAt1: true,
	}, c.rpcTimeout)
	if err != nil {
		return fail(err)
	}
	var capabilities dap.Capabilities
	if json.Unmarshal(body, &capabilities) != nil || !capabilities.MultiDapSupportsNativeTraceRequests ||
		!capabilities.SupportsConfigurationDoneRequest {
		c.stage = "native trace capability admission"
		return fail(errors.New("native trace capability is unavailable"))
	}
	c.stage = "DAP configuration"
	if err := c.phase(c.rpcTimeout, func() error {
		attach, err := c.send("attach", struct{}{})
		if err != nil {
			return err
		}
		message, err := c.next()
		if err != nil || message.Type != dap.TypeEvent || message.Event != "initialized" {
			c.healthy = false
			return errors.New("attach did not open its configuration barrier")
		}
		configured, err := c.send("configurationDone", struct{}{})
		if err != nil {
			return err
		}
		if _, err := c.response(configured, "configurationDone"); err != nil {
			return err
		}
		if _, err := c.response(attach, "attach"); err != nil {
			return err
		}
		c.attached = true
		return nil
	}); err != nil {
		return fail(err)
	}
	c.stage, c.nativeAttempted = "native open", true
	opened, err := c.nativeRequest("open", options.open, c.openTimeout)
	if err != nil {
		return fail(err)
	}
	if !healthyCaptureStatus(opened.Status, opened.Status.Handle) || len(opened.Batch) != 0 ||
		opened.Receipt.Operation != "open" || opened.Receipt.Outcome != "observed" {
		return fail(c.invalidNative())
	}
	c.handle, c.nativeHealthy = opened.Status.Handle, true
	c.stage = "native poll"
	polled, err := c.nativeRequest("poll", dap.NativeTraceHandleArguments{Handle: c.handle}, c.rpcTimeout)
	if err != nil {
		return fail(err)
	}
	if !healthyCaptureStatus(polled.Status, c.handle) || len(polled.Batch) != 0 ||
		polled.Status.NativeGeneration < opened.Status.NativeGeneration {
		return fail(c.invalidNative())
	}
	c.stage = "native read"
	read, err := c.nativeRequest("read", dap.NativeTraceReadArguments{
		Handle: c.handle, Start: options.start, Generation: polled.Status.NativeGeneration, Count: options.count,
	}, c.rpcTimeout)
	if err != nil {
		return fail(err)
	}
	c.stage = "capture validation"
	capture, err := timemachine.DecodeCapture(bytes.NewReader(read.Batch))
	if err != nil || !healthyCaptureStatus(read.Status, c.handle) ||
		read.Status.NativeGeneration != polled.Status.NativeGeneration ||
		read.Receipt.Operation != "read" || read.Receipt.Outcome != "read" ||
		!captureSourceMatches(capture.Source, options.open) || len(capture.Generations) != 1 {
		return fail(c.invalidNative())
	}
	generation := capture.Generations[0]
	if generation.ID != polled.Status.NativeGeneration || generation.FirstIndex != options.start ||
		len(generation.PacketsHex) > options.count {
		return fail(c.invalidNative())
	}
	return capture, nil
}

type traceCaptureNativeResult struct {
	Status  tracecontrol.Status  `json:"status"`
	Batch   json.RawMessage      `json:"batch,omitempty"`
	Receipt tracecontrol.Receipt `json:"receipt"`
}

func (c *traceCaptureDAP) nativeRequest(operation string, arguments any, timeout time.Duration) (traceCaptureNativeResult, error) {
	body, err := c.request("multi-dap/trace/"+operation, arguments, timeout)
	if err != nil {
		return traceCaptureNativeResult{}, err
	}
	var result traceCaptureNativeResult
	if err := decodeTraceCaptureJSON(body, &result); err != nil {
		return traceCaptureNativeResult{}, c.invalidNative()
	}
	return result, nil
}

func decodeTraceCaptureJSON(data []byte, destination any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || len(data) > dap.MaxNativeTraceBodyLength || trimmed[0] != '{' {
		return errors.New("invalid native response")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing native response")
	}
	return nil
}

func (c *traceCaptureDAP) invalidNative() error {
	c.healthy, c.nativeHealthy = false, false
	return errors.New("native response failed validation")
}

func healthyCaptureStatus(status tracecontrol.Status, handle uint64) bool {
	return handle > 0 && handle <= math.MaxInt32 && status.Handle == handle &&
		status.State == "open" && !status.Busy && !status.NeedsReconciliation &&
		status.NativeGeneration > 0 && status.PacketCountKnown &&
		status.SourceBinding == "operator_declared" &&
		!status.NativeRuntimeQualified && !status.HardwareQualified &&
		!status.LastReceipt.NativeStateUncertain && !status.LastReceipt.HelperAbortRequested
}

func captureSourceMatches(source timemachine.Source, expected dap.NativeTraceOpenArguments) bool {
	coreMatches := source.CoreID == nil && expected.CoreID == nil ||
		source.CoreID != nil && expected.CoreID != nil && *source.CoreID == *expected.CoreID
	return source.Kind == expected.SourceKind && source.SessionID == expected.SessionID &&
		source.SourceID == expected.SourceID && coreMatches &&
		source.ImageSHA256 == expected.ImageSHA256 && source.SnapshotSHA256 == expected.SnapshotSHA256 &&
		source.InitialContext == nil
}

func (c *traceCaptureDAP) cleanup() error {
	if !c.healthy || !c.attached {
		return nil
	}
	originalStage := c.stage
	var cleanupErr error
	cleanupStage := ""
	recordError := func(stage string, err error) {
		if err != nil {
			if cleanupStage == "" {
				cleanupStage = stage
			}
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if c.handle != 0 && !c.nativeHealthy {
		status, err := c.nativeRequest("status", struct{}{}, c.rpcTimeout)
		if err == nil {
			c.nativeHealthy = healthyCaptureStatus(status.Status, c.handle)
		}
		recordError("local native status", err)
	}
	if c.healthy && c.nativeHealthy && !c.closeAttempted {
		c.closeAttempted, c.stage = true, "native close"
		closed, err := c.nativeRequest("close", dap.NativeTraceHandleArguments{Handle: c.handle}, c.rpcTimeout)
		if err == nil {
			if closed.Status.Handle != c.handle || closed.Status.State != "closed" ||
				closed.Status.Busy || closed.Status.NeedsReconciliation || len(closed.Batch) != 0 ||
				closed.Receipt.Operation != "close" || closed.Receipt.Outcome != "closed" ||
				!closed.Receipt.NativeDisconnectConfirmed || closed.Receipt.NativeStateUncertain ||
				closed.Receipt.HelperAbortRequested {
				err = c.invalidNative()
			} else {
				c.nativeClosed = true
			}
		}
		recordError("native close", err)
	}
	if c.healthy {
		c.stage = "DAP disconnect"
		_, err := c.request("disconnect", dap.DisconnectArguments{
			TerminateDebuggee: new(bool), SuspendDebuggee: new(bool),
		}, c.rpcTimeout)
		recordError("DAP disconnect", err)
		if err == nil {
			c.attached = false
			c.stage = "proxy shutdown"
			err = c.phase(c.rpcTimeout, func() error {
				_ = c.input.Close()
				if _, err := c.decoder.Decode(); err != io.EOF {
					return errors.New("DAP stream did not end after disconnect")
				}
				select {
				case c.proxyErr = <-c.done:
					c.proxyWaited = true
					return c.proxyErr
				case <-c.ctx.Done():
					return c.ctx.Err()
				}
			})
			recordError("proxy shutdown", err)
		}
	}
	if cleanupErr == nil {
		c.stage = originalStage
	} else {
		c.stage = cleanupStage
	}
	return cleanupErr
}
