package dap

import "errors"

const (
	MaxNativeTraceArguments = 16 << 10
	// A full 4096-packet batch fits here, leaving ample envelope space under
	// the ordinary one-MiB DAP framing limit.
	MaxNativeTraceBodyLength = 512 << 10
	MaxNativeTracePackets    = 4096
)

type NativeTraceOpenArguments struct {
	SourceKind                  string  `json:"sourceKind"`
	SessionID                   uint32  `json:"sessionId"`
	SourceID                    string  `json:"sourceId"`
	AcknowledgeUnverifiedSource bool    `json:"acknowledgeUnverifiedSource"`
	CoreID                      *uint32 `json:"coreId,omitempty"`
	ImageSHA256                 string  `json:"imageSha256,omitempty"`
	SnapshotSHA256              string  `json:"snapshotSha256,omitempty"`
}

type NativeTraceHandleArguments struct {
	Handle uint64 `json:"handle"`
}

type NativeTraceReadArguments struct {
	Handle     uint64 `json:"handle"`
	Start      uint64 `json:"start,string"`
	Generation uint64 `json:"generation,string"`
	Count      int    `json:"count"`
}

type NativeTraceControlArguments struct {
	Handle     uint64 `json:"handle"`
	Authorized bool   `json:"authorized"`
}

type NativeTraceErrorCode uint8

const (
	NativeTraceFailed NativeTraceErrorCode = iota
	NativeTraceUnavailable
	NativeTraceInvalidArguments
	NativeTraceSourceAcknowledgementRequired
	NativeTraceNotOwner
	NativeTraceStaleHandle
	NativeTraceBusy
	NativeTraceStaleOperation
	NativeTraceReconciliationRequired
	NativeTraceGenerationChanged
	NativeTraceCancelled
	NativeTraceTimeout
	NativeTraceInvalidResponse
)

// NativeTraceError is the fixed diagnostic vocabulary allowed through this
// extension. Providers can preserve a programmatic cause without exposing its
// potentially private text to the frontend.
type NativeTraceError struct {
	Code  NativeTraceErrorCode
	cause error
}

func NewNativeTraceError(code NativeTraceErrorCode, cause error) *NativeTraceError {
	return &NativeTraceError{Code: code, cause: cause}
}

func (e *NativeTraceError) Error() string {
	switch e.Code {
	case NativeTraceUnavailable:
		return "native trace requests are unavailable"
	case NativeTraceInvalidArguments:
		return "invalid native trace arguments"
	case NativeTraceSourceAcknowledgementRequired:
		return "native trace requires explicit unverified source acknowledgement"
	case NativeTraceNotOwner:
		return "native trace capture is not owned by this frontend"
	case NativeTraceStaleHandle:
		return "native trace capture handle is stale"
	case NativeTraceBusy:
		return "native trace operation is already in progress"
	case NativeTraceStaleOperation:
		return "native trace operation belongs to an expired frontend"
	case NativeTraceReconciliationRequired:
		return "native trace state requires operator reconciliation"
	case NativeTraceGenerationChanged:
		return "native trace source generation changed"
	case NativeTraceCancelled:
		return "native trace request was cancelled"
	case NativeTraceTimeout:
		return "native trace request timed out"
	case NativeTraceInvalidResponse:
		return "native trace backend returned an invalid response"
	default:
		return "native trace operation failed; inspect local trace status"
	}
}

func (e *NativeTraceError) Unwrap() error { return e.cause }

func nativeTraceErrorMessage(err error) string {
	var native *NativeTraceError
	if errors.As(err, &native) && native != nil {
		return native.Error()
	}
	return (&NativeTraceError{}).Error()
}
