// Package mbp implements the MULTI Bridge Protocol v1 wire format: the
// NDJSON-over-loopback-TCP protocol between the Go daemon and bridge.py,
// defined in docs/architecture.md §7.
//
// This package is deliberately a pure codec with no policy. It knows how to
// turn bytes into one of four frame shapes and back, and nothing about what
// a "bp_set" or a "state" method means. Debugger Core, not this package,
// interprets params and result payloads — see the comment on Request.Params
// below for why that split matters.
package mbp

import "encoding/json"

// FrameKind discriminates the four frame shapes defined by architecture.md
// §7.2. A Frame's Kind is decided once, during decoding, from which of the
// "event", "ok", and "method" keys are present on the wire — see the
// discrimination order documented on Decoder.Decode.
type FrameKind int

const (
	// KindRequest is a call from the daemon to the bridge: {"id","method","params"}.
	KindRequest FrameKind = iota
	// KindResponse is the bridge's reply to a request, success or failure:
	// {"id","ok","result"} or {"id","ok","error"}.
	KindResponse
	// KindEvent is an unsolicited notification from the bridge: {"event","params"}.
	// Events carry no id; they are not replies to anything.
	KindEvent
)

func (k FrameKind) String() string {
	switch k {
	case KindRequest:
		return "request"
	case KindResponse:
		return "response"
	case KindEvent:
		return "event"
	default:
		return "unknown"
	}
}

// Frame is the discriminated union of the three frame shapes that appear on
// the wire (a response covers both the "ok" and "error" cases of §7.2, so
// there are three Go types but four documented shapes). Callers type-switch
// or type-assert on the concrete type after checking Kind.
type Frame interface {
	Kind() FrameKind
}

// Request is a call from the daemon to the bridge.
//
// Params is left as json.RawMessage rather than decoded into a concrete Go
// type here, on purpose: this package has no method table and must not grow
// one. The method table (architecture.md §7.3) belongs to Debugger Core,
// which knows what shape "bp_set" or "eval" params take. Making the codec
// decode params would mean the codec, not Debugger Core, decides that
// vocabulary — exactly the coupling §7.3's "primitives only, no DAP
// vocabulary here" rule is trying to prevent one layer up.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Kind implements Frame.
func (*Request) Kind() FrameKind { return KindRequest }

// Response is the bridge's reply to a Request, carrying either Result (when
// OK is true) or Error (when OK is false). Result is left as
// json.RawMessage for the same reason Request.Params is: this package does
// not know, and must not need to know, the shape of any method's result.
type Response struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Kind implements Frame.
func (*Response) Kind() FrameKind { return KindResponse }

// Error is the error envelope of architecture.md §7.2 and §7.5. Raw is
// MULTI's own output, semantically uninterpreted by the bridge: Debugger
// Core decides how to present it, so this package must not parse, trim, or
// otherwise transform it. Raw is byte-exact unless RawLossy is true, which
// the bridge sets when it had to decode MULTI's output with
// errors="replace" because it produced bytes that are not valid UTF-8 (see
// §7.5) — in that case Raw is still the best available evidence, just not a
// byte-exact copy of what MULTI wrote.
type Error struct {
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Raw      string `json:"raw"`
	RawLossy bool   `json:"raw_lossy,omitempty"`
}

// Event is an unsolicited notification from the bridge. Params is
// json.RawMessage for the same reason as Request.Params.
type Event struct {
	Event  string          `json:"event"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Kind implements Frame.
func (*Event) Kind() FrameKind { return KindEvent }

// Handshake is the first frame exchanged on a new MBP connection
// (architecture.md §7.2). A version mismatch between ProtocolVersion here
// and what the daemon expects is a hard startup failure — no compatibility
// guessing — so this type carries no defaulting logic of its own.
type Handshake struct {
	ProtocolVersion int    `json:"protocol_version"`
	BridgeVersion   string `json:"bridge_version"`
	MaxMessageSize  int    `json:"max_message_size"`
	Encoding        string `json:"encoding"`
}
