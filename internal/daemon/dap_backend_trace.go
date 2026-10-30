package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Tacrolimus/multi-dap/internal/core/session"
	"github.com/Tacrolimus/multi-dap/internal/dap"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

// Optional: ordinary debugger cores are not required to own a native helper.
// The frontend never bypasses this owner-scoped actor boundary.
type nativeTraceCore interface {
	Trace(context.Context, session.ControllerID, tracecontrol.Request) (tracecontrol.Result, error)
	TraceSnapshot(context.Context, session.ControllerID) (tracecontrol.Status, error)
	TraceEnabled() bool
}

func (b *Backend) nativeTrace(ctx context.Context, action dap.Action) (dap.ActionResult, error) {
	if err := dap.ValidateNativeTraceAction(action); err != nil {
		return dap.ActionResult{}, err
	}
	core, ok := b.core.(nativeTraceCore)
	if !ok || !core.TraceEnabled() {
		return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceUnavailable, nil)
	}
	f, err := b.frontend()
	if err != nil {
		return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceNotOwner, err)
	}
	f.configurationMu.Lock()
	configuring := f.configuring
	f.configurationMu.Unlock()
	if configuring {
		return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceNotOwner, nil)
	}
	if action.Kind == dap.ActionTraceStatus {
		status, err := core.TraceSnapshot(ctx, f.owner)
		if !b.nativeTraceFrontendCurrent(f) {
			return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceStaleOperation, nil)
		}
		if err != nil {
			return dap.ActionResult{}, nativeTraceError(err)
		}
		if !boundedNativeTraceStatus(status) {
			return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceInvalidResponse, nil)
		}
		return b.nativeTraceBody(f, struct {
			Status tracecontrol.Status `json:"status"`
		}{Status: status})
	}
	request := nativeTraceRequest(action)
	if err := tracecontrol.ValidateRequest(request); err != nil {
		return dap.ActionResult{}, nativeTraceError(err)
	}
	result, err := core.Trace(ctx, f.owner, request)
	if !b.nativeTraceFrontendCurrent(f) {
		return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceStaleOperation, nil)
	}
	if err != nil {
		// A failed/expired operation must never forward a partial batch. The
		// current owner can query the separate local status/receipt surface.
		return dap.ActionResult{}, nativeTraceError(err)
	}
	if !boundedNativeTraceStatus(result.Status) || !boundedNativeTraceReceipt(result.Receipt) ||
		!validNativeTraceBatch(action, result.Batch) {
		return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceInvalidResponse, nil)
	}
	return b.nativeTraceBody(f, result)
}

func nativeTraceRequest(action dap.Action) tracecontrol.Request {
	switch action.Kind {
	case dap.ActionTraceOpen:
		args := action.TraceOpen
		var coreID *uint32
		if args.CoreID != nil {
			value := *args.CoreID
			coreID = &value
		}
		return tracecontrol.Request{Op: tracecontrol.OpOpen, Open: tracecontrol.OpenRequest{
			Source: timemachine.Source{
				Kind: args.SourceKind, SessionID: args.SessionID, SourceID: args.SourceID,
				CoreID: coreID, ImageSHA256: args.ImageSHA256, SnapshotSHA256: args.SnapshotSHA256,
			},
			AcknowledgeUnverifiedSource: args.AcknowledgeUnverifiedSource,
		}}
	case dap.ActionTracePoll:
		return tracecontrol.Request{Op: tracecontrol.OpStatus, Handle: action.TraceHandle.Handle}
	case dap.ActionTraceRead:
		args := action.TraceRead
		return tracecontrol.Request{Op: tracecontrol.OpRead, Handle: args.Handle, Read: tracecontrol.ReadRequest{
			Start: args.Start, Generation: args.Generation, Count: args.Count,
		}}
	case dap.ActionTraceStart:
		return tracecontrol.Request{Op: tracecontrol.OpStart, Handle: action.TraceControl.Handle, Authorized: action.TraceControl.Authorized}
	case dap.ActionTraceStop:
		return tracecontrol.Request{Op: tracecontrol.OpStop, Handle: action.TraceControl.Handle, Authorized: action.TraceControl.Authorized}
	case dap.ActionTraceClose:
		return tracecontrol.Request{Op: tracecontrol.OpClose, Handle: action.TraceHandle.Handle}
	default:
		return tracecontrol.Request{}
	}
}

func (b *Backend) nativeTraceFrontendCurrent(f *frontend) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active == f
}

func (b *Backend) nativeTraceBody(f *frontend, value any) (dap.ActionResult, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body) > dap.MaxNativeTraceBodyLength {
		return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceInvalidResponse, nil)
	}
	if !b.nativeTraceFrontendCurrent(f) {
		return dap.ActionResult{}, dap.NewNativeTraceError(dap.NativeTraceStaleOperation, nil)
	}
	return dap.ActionResult{TraceBody: body}, nil
}

func nativeTraceError(err error) error {
	code := dap.NativeTraceFailed
	switch {
	case errors.Is(err, tracecontrol.ErrUnavailable):
		code = dap.NativeTraceUnavailable
	case errors.Is(err, tracecontrol.ErrInvalidRequest):
		code = dap.NativeTraceInvalidArguments
	case errors.Is(err, tracecontrol.ErrUnverifiedSource):
		code = dap.NativeTraceSourceAcknowledgementRequired
	case errors.Is(err, tracecontrol.ErrNotOwner), errors.Is(err, session.ErrNotLeaseHolder):
		code = dap.NativeTraceNotOwner
	case errors.Is(err, tracecontrol.ErrStaleHandle):
		code = dap.NativeTraceStaleHandle
	case errors.Is(err, tracecontrol.ErrBusy):
		code = dap.NativeTraceBusy
	case errors.Is(err, tracecontrol.ErrStaleOperation):
		code = dap.NativeTraceStaleOperation
	case errors.Is(err, tracecontrol.ErrReconciliationRequired):
		code = dap.NativeTraceReconciliationRequired
	case errors.Is(err, tracecontrol.ErrStaleGeneration):
		code = dap.NativeTraceGenerationChanged
	case errors.Is(err, context.DeadlineExceeded):
		code = dap.NativeTraceTimeout
	case errors.Is(err, context.Canceled):
		code = dap.NativeTraceCancelled
	}
	return dap.NewNativeTraceError(code, err)
}

func boundedNativeTraceStatus(status tracecontrol.Status) bool {
	for _, text := range []string{status.State, status.Timebase, status.TraceState, status.SourceBinding} {
		if len(text) > 128 {
			return false
		}
	}
	return boundedNativeTraceReceipt(status.LastReceipt)
}

func boundedNativeTraceReceipt(receipt tracecontrol.Receipt) bool {
	return len(receipt.Operation) <= 128 && len(receipt.Outcome) <= 128 && len(receipt.Reason) <= 128
}

func validNativeTraceBatch(action dap.Action, capture *timemachine.Capture) bool {
	if action.Kind != dap.ActionTraceRead {
		return capture == nil
	}
	if capture == nil || len(capture.Generations) != 1 ||
		len(capture.Schema) > 64 || len(capture.MultiVersion) > 64 || len(capture.InfoHex) > 128 ||
		len(capture.ABI.ByteOrder) > 16 ||
		len(capture.Source.Kind) > 32 || len(capture.Source.SourceID) > 128 ||
		len(capture.Source.ImageSHA256) > 64 || len(capture.Source.SnapshotSHA256) > 64 ||
		(capture.Source.Kind != "live" && capture.Source.Kind != "snapshot") ||
		capture.Source.InitialContext != nil {
		return false
	}
	generation := capture.Generations[0]
	if generation.ID != action.TraceRead.Generation || generation.FirstIndex != action.TraceRead.Start ||
		len(generation.PacketsHex) > action.TraceRead.Count || len(generation.Reason) > 32 {
		return false
	}
	for _, packet := range generation.PacketsHex {
		if len(packet) != 64 {
			return false
		}
	}
	body, err := json.Marshal(capture)
	if err != nil || len(body) > dap.MaxNativeTraceBodyLength {
		return false
	}
	_, err = timemachine.DecodeCapture(bytes.NewReader(body))
	return err == nil
}
