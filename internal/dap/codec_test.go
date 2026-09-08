package dap

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

type chunkReader struct {
	data []byte
	size int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.size
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestContentLengthRoundTripWithPartialReads(t *testing.T) {
	message := Envelope{Seq: 7, Type: TypeRequest, Command: "initialize", Arguments: []byte(`{"adapterID":"multi-dap"}`)}
	var wire bytes.Buffer
	if err := NewEncoder(&wire).Encode(message); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 2, 3, 7} {
		decoder := NewDecoder(&chunkReader{data: append([]byte(nil), wire.Bytes()...), size: size})
		got, err := decoder.Decode()
		if err != nil {
			t.Fatalf("chunk size %d: %v", size, err)
		}
		if got.Seq != message.Seq || got.Type != TypeRequest || got.Command != message.Command || string(got.Arguments) != string(message.Arguments) {
			t.Fatalf("chunk size %d: got %#v", size, got)
		}
	}
}

func TestDecoderRejectsInvalidFraming(t *testing.T) {
	validBody := `{"seq":1,"type":"request","command":"threads"}`
	tests := []struct {
		name string
		wire string
		max  int
	}{
		{"missing length", "Content-Type: application/vscode-jsonrpc\r\n\r\n" + validBody, DefaultMaxContentLength},
		{"duplicate length", "Content-Length: 1\r\nContent-Length: 1\r\n\r\n{}", DefaultMaxContentLength},
		{"unsupported header", "Content-Length: 2\r\nContent-Type: application/json\r\n\r\n{}", DefaultMaxContentLength},
		{"negative length", "Content-Length: -1\r\n\r\n", DefaultMaxContentLength},
		{"non decimal length", "Content-Length: 0x10\r\n\r\n", DefaultMaxContentLength},
		{"bare LF", "Content-Length: 2\n\n{}", DefaultMaxContentLength},
		{"truncated headers", "Content-Length: 2\r\n", DefaultMaxContentLength},
		{"truncated body", "Content-Length: 4\r\n\r\n{}", DefaultMaxContentLength},
		{"oversize body", "Content-Length: 3\r\n\r\n{} ", 2},
		{"missing envelope fields", "Content-Length: 2\r\n\r\n{}", DefaultMaxContentLength},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder := NewDecoder(strings.NewReader(test.wire))
			decoder.MaxContentLength = test.max
			if _, err := decoder.Decode(); err == nil {
				t.Fatal("Decode() succeeded, want framing error")
			}
		})
	}
}

func TestDecoderRejectsOutOfRangeSequenceNumbers(t *testing.T) {
	for _, body := range []string{
		`{"seq":0,"type":"request","command":"threads"}`,
		`{"seq":2147483648,"type":"request","command":"threads"}`,
		`{"seq":1,"type":"response","request_seq":0,"success":true,"command":"threads"}`,
		`{"seq":1,"type":"response","request_seq":2147483648,"success":true,"command":"threads"}`,
	} {
		wire := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
		if _, err := NewDecoder(strings.NewReader(wire)).Decode(); err == nil {
			t.Fatalf("Decode(%s) succeeded, want sequence-range error", body)
		}
	}
}

func TestDecoderEOFBetweenFrames(t *testing.T) {
	decoder := NewDecoder(strings.NewReader(""))
	if _, err := decoder.Decode(); err != io.EOF {
		t.Fatalf("Decode() error = %v, want EOF", err)
	}
}
