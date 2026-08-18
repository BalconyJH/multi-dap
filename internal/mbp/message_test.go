package mbp

import (
	"encoding/json"
	"testing"
)

// These tests pin the wire field names of message.go: architecture.md §7.2
// specifies the frame shapes by their literal JSON keys, and a struct tag
// typo here would silently break interop with bridge.py without failing
// any behavioral test in codec_test.go (which only exercises a subset of
// fields). Each test round-trips a value through json.Marshal and checks
// the resulting keys directly, rather than only unmarshaling, because a
// struct tag mistake can produce a valid-but-wrong wire shape that a
// round-trip through the same buggy tags would hide.

func decodeToMap(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", b, err)
	}
	return m
}

func TestRequestFieldNames(t *testing.T) {
	b, err := json.Marshal(&Request{ID: 7, Method: "bp_set", Params: json.RawMessage(`{"line":12}`)})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	m := decodeToMap(t, b)
	for _, key := range []string{"id", "method", "params"} {
		if _, ok := m[key]; !ok {
			t.Errorf("marshaled Request missing key %q in %s", key, b)
		}
	}
}

func TestRequestOmitsParamsWhenNil(t *testing.T) {
	b, err := json.Marshal(&Request{ID: 1, Method: "state"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	m := decodeToMap(t, b)
	if _, ok := m["params"]; ok {
		t.Errorf("marshaled Request has \"params\" key with a nil Params: %s", b)
	}
}

func TestResponseOKFieldNames(t *testing.T) {
	b, err := json.Marshal(&Response{ID: 7, OK: true, Result: json.RawMessage(`{"execution":"stopped"}`)})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	m := decodeToMap(t, b)
	for _, key := range []string{"id", "ok", "result"} {
		if _, ok := m[key]; !ok {
			t.Errorf("marshaled Response missing key %q in %s", key, b)
		}
	}
	if _, ok := m["error"]; ok {
		t.Errorf("marshaled successful Response has an \"error\" key: %s", b)
	}
}

func TestResponseErrorFieldNames(t *testing.T) {
	b, err := json.Marshal(&Response{
		ID: 9, OK: false,
		Error: &Error{Kind: "multi_refused", Message: "no", Raw: "E>", RawLossy: true},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	m := decodeToMap(t, b)
	if _, ok := m["result"]; ok {
		t.Errorf("marshaled error Response has a \"result\" key: %s", b)
	}
	errFields := decodeToMap(t, m["error"])
	for _, key := range []string{"kind", "message", "raw", "raw_lossy"} {
		if _, ok := errFields[key]; !ok {
			t.Errorf("marshaled Error missing key %q in %s", key, m["error"])
		}
	}
}

func TestErrorRawLossyOmittedWhenFalse(t *testing.T) {
	b, err := json.Marshal(&Error{Kind: "multi_refused", Message: "no", Raw: "E>"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	m := decodeToMap(t, b)
	if _, ok := m["raw_lossy"]; ok {
		t.Errorf("marshaled Error has \"raw_lossy\" key when RawLossy is false (the zero value, meaning byte-exact): %s", b)
	}
}

func TestEventFieldNames(t *testing.T) {
	b, err := json.Marshal(&Event{Event: "bridge_closing", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	m := decodeToMap(t, b)
	for _, key := range []string{"event", "params"} {
		if _, ok := m[key]; !ok {
			t.Errorf("marshaled Event missing key %q in %s", key, b)
		}
	}
	if _, ok := m["id"]; ok {
		t.Errorf("marshaled Event has an \"id\" key; events are not replies and carry no id: %s", b)
	}
}

func TestHandshakeFieldNames(t *testing.T) {
	b, err := json.Marshal(&Handshake{
		ProtocolVersion: 1,
		BridgeVersion:   "0.1.0",
		MaxMessageSize:  1 << 20,
		Encoding:        "utf-8",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	m := decodeToMap(t, b)
	for _, key := range []string{"protocol_version", "bridge_version", "max_message_size", "encoding"} {
		if _, ok := m[key]; !ok {
			t.Errorf("marshaled Handshake missing key %q in %s", key, b)
		}
	}
}

func TestFrameKindConstantsAreDistinct(t *testing.T) {
	kinds := map[FrameKind]string{
		KindRequest:  "request",
		KindResponse: "response",
		KindEvent:    "event",
	}
	if len(kinds) != 3 {
		t.Fatalf("KindRequest, KindResponse, KindEvent are not pairwise distinct: %v", kinds)
	}
}

func TestConcreteTypesImplementFrame(t *testing.T) {
	var _ Frame = (*Request)(nil)
	var _ Frame = (*Response)(nil)
	var _ Frame = (*Event)(nil)
}
