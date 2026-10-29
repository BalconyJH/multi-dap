// Package tracebridge owns one private native TimeMachine helper process.
// It provides transport and process-lifetime boundaries, not target policy.
package tracebridge

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	ProtocolVersion  = 1
	ProtocolName     = "multi-dap.timemachine-helper.v1"
	MaxRequestBytes  = 16 << 10
	MaxResponseBytes = 1 << 20
	MaxPacketBatch   = 4096
	maxNotifications = 4096
)

var (
	ErrPoisoned           = errors.New("tracebridge: helper is poisoned")
	ErrClosed             = errors.New("tracebridge: helper is closed")
	ErrStaleGeneration    = errors.New("tracebridge: stale trace generation")
	ErrInvalidOptions     = errors.New("tracebridge: invalid helper options")
	ErrInvalidArguments   = errors.New("tracebridge: invalid arguments")
	ErrNotOpen            = errors.New("tracebridge: session is not open")
	ErrAlreadyOpen        = errors.New("tracebridge: session is already open")
	ErrNotAuthorized      = errors.New("tracebridge: trace control is not authorized")
	ErrProtocol           = errors.New("tracebridge: invalid helper protocol")
	ErrTransport          = errors.New("tracebridge: helper transport failed")
	ErrExited             = errors.New("tracebridge: helper exited")
	ErrCleanupUnconfirmed = errors.New("tracebridge: helper cleanup is unconfirmed")
)

// Error keeps diagnostics fixed while retaining a cause for errors.Is.
// It deliberately does not expose subprocess stderr, paths, or raw frames.
type Error struct {
	Code     string
	cause    error
	poisoned bool
}

func (e *Error) Error() string { return "tracebridge: " + e.Code }

func (e *Error) Unwrap() error {
	if e.poisoned {
		return errors.Join(ErrPoisoned, e.cause)
	}
	return e.cause
}

// RemoteError contains only a member of the protocol's fixed error-code set.
// Only a stale_generation response to Read is recoverable.
type RemoteError struct {
	Code        string
	recoverable bool
}

func (e *RemoteError) Error() string { return "tracebridge: remote " + e.Code }
func (e *RemoteError) Unwrap() error {
	if e.recoverable {
		return ErrStaleGeneration
	}
	return ErrPoisoned
}

type OpenArgs struct {
	Installation string `json:"installation"`
	MultiVersion string `json:"multi_version"`
	DLLSHA256    string `json:"dll_sha256"`
	SessionID    uint32 `json:"session_id"`
}

type ReadArgs struct {
	Start      uint64 `json:"start,string"`
	Count      int    `json:"count"`
	Generation uint64 `json:"generation,string"`
}

type Notifications struct {
	Events                []string `json:"events"`
	Dropped               uint64   `json:"dropped"`
	IgnoredCallbacks      uint64   `json:"ignored_callbacks"`
	CallbackFailed        bool     `json:"callback_failed"`
	GenerationInvalidated bool     `json:"generation_invalidated"`
}

type Status struct {
	InfoHex       string        `json:"info_hex"`
	PacketCount   uint64        `json:"packet_count,string"`
	Generation    uint64        `json:"generation,string"`
	TraceState    string        `json:"trace_state"`
	Notifications Notifications `json:"notifications"`
}

type ReadResult struct {
	Generation uint64   `json:"generation,string"`
	Start      uint64   `json:"start,string"`
	Count      int      `json:"count"`
	PacketsHex []string `json:"packets_hex"`
}

type ControlResult struct {
	Accepted   bool   `json:"accepted"`
	Confirmed  bool   `json:"confirmed"`
	Generation uint64 `json:"generation,string"`
	TraceState string `json:"trace_state"`
}

type ready struct {
	Version     int    `json:"v"`
	Event       string `json:"event"`
	Protocol    string `json:"protocol"`
	PythonMajor int    `json:"python_major"`
	PythonMinor int    `json:"python_minor"`
	PointerBits int    `json:"pointer_bits"`
}

type frame struct {
	event  string
	seq    int64
	ok     bool
	result json.RawMessage
	code   string
	ready  ready
}

func decodeFrame(data []byte) (frame, error) {
	if !utf8.Valid(data) || validateJSON(data) != nil {
		return frame{}, ErrProtocol
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return frame{}, ErrProtocol
	}
	var version int
	if json.Unmarshal(fields["v"], &version) != nil || version != ProtocolVersion {
		return frame{}, ErrProtocol
	}
	if rawEvent, exists := fields["event"]; exists {
		var event string
		if json.Unmarshal(rawEvent, &event) != nil {
			return frame{}, ErrProtocol
		}
		switch event {
		case "ready":
			if !exactFields(fields, "v", "event", "protocol", "python_major", "python_minor", "pointer_bits") {
				return frame{}, ErrProtocol
			}
			var greeting ready
			if json.Unmarshal(data, &greeting) != nil ||
				greeting.Protocol != ProtocolName || greeting.PythonMajor != 3 ||
				greeting.PythonMinor < 8 || greeting.PythonMinor > 100 ||
				(greeting.PointerBits != 32 && greeting.PointerBits != 64) {
				return frame{}, ErrProtocol
			}
			return frame{event: event, ready: greeting}, nil
		case "fatal":
			if !exactFields(fields, "v", "event", "error") {
				return frame{}, ErrProtocol
			}
			code, err := decodeCode(fields["error"])
			if err != nil {
				return frame{}, err
			}
			return frame{event: event, code: code}, nil
		default:
			return frame{}, ErrProtocol
		}
	}
	var sequence int64
	var success bool
	if json.Unmarshal(fields["seq"], &sequence) != nil || sequence <= 0 ||
		json.Unmarshal(fields["ok"], &success) != nil {
		return frame{}, ErrProtocol
	}
	if success {
		if !exactFields(fields, "v", "seq", "ok", "result") {
			return frame{}, ErrProtocol
		}
		if _, err := decodeObject(fields["result"]); err != nil {
			return frame{}, err
		}
		return frame{seq: sequence, ok: true, result: fields["result"]}, nil
	}
	if !exactFields(fields, "v", "seq", "ok", "error") {
		return frame{}, ErrProtocol
	}
	code, err := decodeCode(fields["error"])
	if err != nil {
		return frame{}, err
	}
	return frame{seq: sequence, code: code}, nil
}

func decodeCode(data json.RawMessage) (string, error) {
	fields, err := decodeObject(data)
	if err != nil || !exactFields(fields, "code") {
		return "", ErrProtocol
	}
	var code string
	if json.Unmarshal(fields["code"], &code) != nil || !knownCode(code) {
		return "", ErrProtocol
	}
	return code, nil
}

func knownCode(code string) bool {
	switch code {
	case "invalid_request", "invalid_arguments", "unsupported_operation",
		"already_open", "not_open", "stale_generation", "invalid_range",
		"native_failure", "native_interrupted", "generation_exhausted",
		"reader_failed", "writer_failed", "input_overflow", "output_overflow",
		"response_too_large", "request_too_large", "request_truncated", "writer_timeout":
		return true
	default:
		return false
	}
}

func decodeStatus(data json.RawMessage, expectedInfoSize int) (Status, error) {
	fields, err := decodeObject(data)
	if err != nil || !exactFields(fields, "info_hex", "packet_count", "generation", "trace_state", "notifications") {
		return Status{}, ErrProtocol
	}
	notifications, err := decodeObject(fields["notifications"])
	if err != nil || !exactFields(notifications, "events", "dropped", "ignored_callbacks", "callback_failed", "generation_invalidated") {
		return Status{}, ErrProtocol
	}
	var result Status
	if json.Unmarshal(data, &result) != nil ||
		!fixedHex(result.InfoHex, expectedInfoSize) ||
		!canonicalUint(fields["packet_count"], result.PacketCount) || result.PacketCount == math.MaxUint64 ||
		!canonicalUint(fields["generation"], result.Generation) || result.Generation == 0 ||
		!traceState(result.TraceState) || len(result.Notifications.Events) > maxNotifications {
		return Status{}, ErrProtocol
	}
	for _, event := range result.Notifications.Events {
		switch event {
		case "trace_start", "trace_stop", "select_packet", "data_clear", "new_data_available",
			"packet_count_regressed", "decoder_replacement":
		default:
			return Status{}, ErrProtocol
		}
	}
	return result, nil
}

func decodeRead(data json.RawMessage, request ReadArgs) (ReadResult, error) {
	fields, err := decodeObject(data)
	if err != nil || !exactFields(fields, "generation", "start", "count", "packets_hex") {
		return ReadResult{}, ErrProtocol
	}
	var result ReadResult
	if json.Unmarshal(data, &result) != nil ||
		!canonicalUint(fields["generation"], result.Generation) ||
		!canonicalUint(fields["start"], result.Start) ||
		result.Generation != request.Generation || result.Start != request.Start ||
		result.Count < 0 || result.Count > request.Count ||
		result.Count != len(result.PacketsHex) || len(result.PacketsHex) > MaxPacketBatch {
		return ReadResult{}, ErrProtocol
	}
	for _, packet := range result.PacketsHex {
		if !fixedHex(packet, 32) {
			return ReadResult{}, ErrProtocol
		}
	}
	return result, nil
}

func decodeControl(data json.RawMessage) (ControlResult, error) {
	fields, err := decodeObject(data)
	if err != nil || !exactFields(fields, "accepted", "confirmed", "generation", "trace_state") {
		return ControlResult{}, ErrProtocol
	}
	var result ControlResult
	if json.Unmarshal(data, &result) != nil ||
		!result.Accepted || result.Confirmed ||
		!canonicalUint(fields["generation"], result.Generation) || result.Generation == 0 ||
		!traceState(result.TraceState) {
		return ControlResult{}, ErrProtocol
	}
	return result, nil
}

func decodeClose(data json.RawMessage) error {
	fields, err := decodeObject(data)
	if err != nil || !exactFields(fields, "closed") || !bytes.Equal(bytes.TrimSpace(fields["closed"]), []byte("true")) {
		return ErrProtocol
	}
	return nil
}

func traceState(state string) bool {
	switch state {
	case "unknown", "running", "stopped", "start_requested", "stop_requested":
		return true
	default:
		return false
	}
}

func fixedHex(value string, count int) bool {
	if len(value) != count*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalUint(raw json.RawMessage, value uint64) bool {
	var text string
	return json.Unmarshal(raw, &text) == nil && text == strconv.FormatUint(value, 10)
}

func decodeObject(data json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, ErrProtocol
	}
	return fields, nil
}

func exactFields(fields map[string]json.RawMessage, keys ...string) bool {
	if len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		raw, exists := fields[key]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	return true
}

func validateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	values := 0
	var visit func(int) error
	visit = func(depth int) error {
		values++
		if depth > 8 || values > MaxPacketBatch+maxNotifications+128 {
			return ErrProtocol
		}
		token, err := decoder.Token()
		if err != nil || token == nil {
			return ErrProtocol
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return ErrProtocol
				}
				key, ok := token.(string)
				if !ok || len(key) > 64 || len(keys) >= 16 {
					return ErrProtocol
				}
				folded := strings.ToLower(key)
				if keys[folded] {
					return ErrProtocol
				}
				keys[folded] = true
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		default:
			return ErrProtocol
		}
		end, err := decoder.Token()
		if err != nil || (delimiter == '{' && end != json.Delim('}')) ||
			(delimiter == '[' && end != json.Delim(']')) {
			return ErrProtocol
		}
		return nil
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrProtocol
	}
	return nil
}
