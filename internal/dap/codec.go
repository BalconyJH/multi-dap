package dap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	DefaultMaxContentLength = 1 << 20
	DefaultMaxHeaderLength  = 8 << 10
)

// Encoder writes one DAP Content-Length framed JSON envelope at a time.
type Encoder struct {
	w                io.Writer
	MaxContentLength int
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w, MaxContentLength: DefaultMaxContentLength}
}

func (e *Encoder) Encode(message Envelope) error {
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("dap: encode envelope: %w", err)
	}
	if len(body) > e.MaxContentLength {
		return fmt.Errorf("dap: content length %d exceeds maximum %d", len(body), e.MaxContentLength)
	}
	header := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)))
	if err := writeAll(e.w, header); err != nil {
		return fmt.Errorf("dap: write header: %w", err)
	}
	if err := writeAll(e.w, body); err != nil {
		return fmt.Errorf("dap: write body: %w", err)
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// Decoder reads strict DAP Content-Length frames. It accepts additional
// well-formed headers but requires exactly one Content-Length header.
type Decoder struct {
	r                *bufio.Reader
	MaxContentLength int
	MaxHeaderLength  int
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		r:                bufio.NewReader(r),
		MaxContentLength: DefaultMaxContentLength,
		MaxHeaderLength:  DefaultMaxHeaderLength,
	}
}

func (d *Decoder) Decode() (Envelope, error) {
	length, err := d.readContentLength()
	if err != nil {
		return Envelope{}, err
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(d.r, body); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Envelope{}, fmt.Errorf("dap: truncated body: %w", io.ErrUnexpectedEOF)
		}
		return Envelope{}, fmt.Errorf("dap: read body: %w", err)
	}
	return decodeEnvelope(body)
}

func (d *Decoder) readContentLength() (int, error) {
	var contentLength *int
	total := 0
	for {
		line, err := d.readHeaderLine()
		if err != nil {
			return 0, err
		}
		total += len(line) + 2
		if total > d.MaxHeaderLength {
			return 0, fmt.Errorf("dap: headers exceed maximum %d bytes", d.MaxHeaderLength)
		}
		if len(line) == 0 {
			break
		}
		name, value, found := bytes.Cut(line, []byte(":"))
		if !found || len(name) == 0 {
			return 0, errors.New("dap: malformed header")
		}
		if !strings.EqualFold(string(name), "Content-Length") {
			return 0, fmt.Errorf("dap: unsupported header %q", name)
		}
		if contentLength != nil {
			return 0, errors.New("dap: duplicate Content-Length header")
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 || bytes.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return 0, errors.New("dap: invalid Content-Length header")
		}
		parsed, err := strconv.ParseUint(string(value), 10, 64)
		if err != nil || parsed > uint64(d.MaxContentLength) || parsed > uint64(^uint(0)>>1) {
			return 0, fmt.Errorf("dap: invalid Content-Length header")
		}
		length := int(parsed)
		contentLength = &length
	}
	if contentLength == nil {
		return 0, errors.New("dap: missing Content-Length header")
	}
	return *contentLength, nil
}

func (d *Decoder) readHeaderLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := d.r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > d.MaxHeaderLength {
			return nil, fmt.Errorf("dap: header line exceeds maximum %d bytes", d.MaxHeaderLength)
		}
		switch err {
		case nil:
			if len(line) < 2 || line[len(line)-2] != '\r' {
				return nil, errors.New("dap: headers must use CRLF")
			}
			return line[:len(line)-2], nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("dap: truncated headers: %w", io.ErrUnexpectedEOF)
		default:
			return nil, fmt.Errorf("dap: read header: %w", err)
		}
	}
}

func decodeEnvelope(body []byte) (Envelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return Envelope{}, fmt.Errorf("dap: malformed JSON body: %w", err)
	}
	var envelope Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("dap: malformed envelope: %w", err)
	}
	if !hasField(fields, "seq") || !hasField(fields, "type") {
		return Envelope{}, errors.New("dap: envelope requires seq and type")
	}
	if envelope.Seq < 1 || envelope.Seq > 1<<31-1 {
		return Envelope{}, errors.New("dap: seq must be in 1..2147483647")
	}
	switch envelope.Type {
	case TypeRequest:
		if envelope.Command == "" || !hasField(fields, "command") {
			return Envelope{}, errors.New("dap: request requires command")
		}
	case TypeResponse:
		if envelope.Command == "" || !hasField(fields, "command") || !hasField(fields, "request_seq") || !hasField(fields, "success") {
			return Envelope{}, errors.New("dap: response missing required fields")
		}
		if envelope.RequestSeq < 1 || envelope.RequestSeq > 1<<31-1 {
			return Envelope{}, errors.New("dap: request_seq must be in 1..2147483647")
		}
	case TypeEvent:
		if envelope.Event == "" || !hasField(fields, "event") {
			return Envelope{}, errors.New("dap: event requires event")
		}
	default:
		return Envelope{}, errors.New("dap: invalid envelope type")
	}
	return envelope, nil
}

func hasField(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}
