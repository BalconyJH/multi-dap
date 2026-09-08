// Package control implements the authenticated, loopback-only daemon control
// plane. It is intentionally separate from DAP: proxy forwards DAP bytes
// without interpreting them, while status and shutdown use this small local
// protocol.
package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ProtocolVersion = 1
	MaxMessageSize  = 16 << 10
)

const (
	MethodStatus   = "status"
	MethodShutdown = "shutdown"
	// MethodProxy upgrades an authenticated control connection into the raw
	// DAP byte stream.  It is deliberately not a DAP command.
	MethodProxy = "proxy"
)

var (
	ErrMalformed    = errors.New("control: malformed message")
	ErrUnauthorized = errors.New("control: unauthorized")
)

type request struct {
	Version int
	Method  string
	Token   string
}

type response struct {
	Version int         `json:"version"`
	OK      bool        `json:"ok"`
	Status  *Status     `json:"status,omitempty"`
	Error   *errorReply `json:"error,omitempty"`
}

type errorReply struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Status is the observation returned by a live daemon. It deliberately
// contains no project connection arguments or control credential.
type Status struct {
	ProbeID        string `json:"probe_id"`
	PID            int    `json:"pid"`
	DAPAddress     string `json:"dap_address"`
	TargetState    string `json:"target_state"`
	ExecutionEpoch uint64 `json:"execution_epoch"`
	StopEpoch      uint64 `json:"stop_epoch"`
	DAPOwned       int    `json:"dap_owned_breakpoints"`
	Pending        int    `json:"pending_breakpoints"`
	Orphaned       int    `json:"orphaned_breakpoints"`
}

func decodeRequest(line []byte) (request, error) {
	fields, err := strictObject(line, map[string]struct{}{
		"version": {}, "method": {}, "token": {},
	})
	if err != nil {
		return request{}, err
	}
	if len(fields) != 3 {
		return request{}, fmt.Errorf("%w: request must contain exactly version, method, and token", ErrMalformed)
	}
	var wire struct {
		Version int    `json:"version"`
		Method  string `json:"method"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(line, &wire); err != nil {
		return request{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if wire.Version != ProtocolVersion {
		return request{}, fmt.Errorf("%w: unsupported protocol version", ErrMalformed)
	}
	if wire.Method != MethodStatus && wire.Method != MethodShutdown && wire.Method != MethodProxy {
		return request{}, fmt.Errorf("%w: unsupported method", ErrMalformed)
	}
	if wire.Token == "" {
		return request{}, fmt.Errorf("%w: token is required", ErrMalformed)
	}
	return request{Version: wire.Version, Method: wire.Method, Token: wire.Token}, nil
}

// strictObject rejects unknown and duplicate keys. encoding/json's ordinary
// struct decoding accepts both, which is unsuitable at an authentication
// boundary because it makes the accepted schema ambiguous.
func strictObject(line []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("%w: expected object", ErrMalformed)
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: object key: %v", ErrMalformed, err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("%w: non-string object key", ErrMalformed)
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: unknown field %q", ErrMalformed, key)
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrMalformed, key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: value for %q: %v", ErrMalformed, key, err)
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("%w: unterminated object", ErrMalformed)
	}
	if token, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing data", ErrMalformed)
	}
	return fields, nil
}
