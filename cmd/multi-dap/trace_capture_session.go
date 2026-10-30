package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

// The transport owns frontend lifetime. This transaction owns only the exact
// native handle it opened, so an editor capture cannot detach its DAP session.
type traceCaptureTransport interface {
	request(string, any, time.Duration) (json.RawMessage, error)
	transportHealthy() bool
	invalidateTransport()
}

type traceCaptureSession struct {
	transport       traceCaptureTransport
	rpcTimeout      time.Duration
	openTimeout     time.Duration
	stage           string
	handle          uint64
	nativeAttempted bool
	nativeHealthy   bool
	closeAttempted  bool
	nativeClosed    bool
}

func (c *traceCaptureSession) capture(options traceCaptureOptions) (timemachine.Capture, error) {
	fail := func(err error) (timemachine.Capture, error) { return timemachine.Capture{}, err }
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

func (c *traceCaptureSession) nativeRequest(operation string, arguments any, timeout time.Duration) (traceCaptureNativeResult, error) {
	body, err := c.transport.request("multi-dap/trace/"+operation, arguments, timeout)
	if err != nil {
		c.nativeHealthy = false
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

func (c *traceCaptureSession) invalidNative() error {
	c.transport.invalidateTransport()
	c.nativeHealthy = false
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

func (c *traceCaptureSession) cleanup() error {
	if !c.transport.transportHealthy() {
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
	if c.transport.transportHealthy() && c.nativeHealthy && !c.closeAttempted {
		c.closeAttempted, c.stage = true, "native close"
		closed, err := c.nativeRequest("close", dap.NativeTraceHandleArguments{Handle: c.handle}, c.rpcTimeout)
		if err == nil {
			if closed.Status.Handle != c.handle || closed.Status.State != "closed" ||
				closed.Status.Busy || closed.Status.NeedsReconciliation || len(closed.Batch) != 0 ||
				closed.Status.SourceBinding != "operator_declared" ||
				closed.Status.NativeRuntimeQualified || closed.Status.HardwareQualified ||
				closed.Status.LastReceipt.NativeStateUncertain || closed.Status.LastReceipt.HelperAbortRequested ||
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
	if cleanupErr == nil {
		c.stage = originalStage
	} else {
		c.stage = cleanupStage
	}
	return cleanupErr
}
