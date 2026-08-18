package mbp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// DefaultMaxFrameSize is the maximum single-line frame size a Decoder
// accepts before a handshake (architecture.md §7.2) has had a chance to
// raise it via max_message_size. 1 MiB comfortably covers any request or
// response this protocol currently defines, while still bounding how much
// memory a single malformed line can force the daemon to buffer before a
// handshake has even happened.
const DefaultMaxFrameSize = 1 << 20 // 1 MiB

// Encoder writes frames as NDJSON: one compact JSON object per line,
// terminated by exactly one "\n", with no embedded newlines. That is the
// entire framing contract, so Encode has nothing to do beyond marshal-then-
// write-a-newline — there is no length prefix or other framing layer.
type Encoder struct {
	w io.Writer
}

// NewEncoder returns an Encoder that writes frames to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// Encode marshals v to JSON and writes it as a single NDJSON line. v is
// typically a *Request, *Response, *Event, or *Handshake. json.Marshal
// never produces an embedded newline for any of these types (JSON strings
// escape a newline byte as the two characters backslash-n), so the framing
// invariant holds for any value that round-trips through encoding/json.
func (e *Encoder) Encode(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("mbp: encode: %w", err)
	}
	b = append(b, '\n')
	_, err = e.w.Write(b)
	return err
}

// Decoder reads NDJSON frames from an underlying io.Reader, one Decode call
// per line.
type Decoder struct {
	r *bufio.Reader

	// MaxFrameSize bounds the size, in bytes, of a single line the Decoder
	// will accept. It starts at DefaultMaxFrameSize. It is a plain exported
	// field rather than a constructor argument or a fixed constant because
	// the limit is not really fixed: architecture.md §7.2's handshake
	// carries max_message_size, and once Debugger Core has read that
	// handshake it raises this field before decoding any larger frame the
	// bridge might legitimately send. Checking it fresh on every Decode
	// call (rather than baking it into a fixed-size scan buffer at
	// construction time) is what makes that raise-after-construction
	// pattern work.
	MaxFrameSize int
}

// NewDecoder returns a Decoder that reads frames from r, one per line, up
// to MaxFrameSize bytes each.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		r:            bufio.NewReader(r),
		MaxFrameSize: DefaultMaxFrameSize,
	}
}

// readLine returns one line, including neither the trailing "\n" nor a
// preceding "\r", or an error. It aborts as soon as the accumulated line
// exceeds MaxFrameSize, without waiting to find the terminating newline, so
// that an oversize or unterminated line cannot force unbounded buffering.
func (d *Decoder) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := d.r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > d.MaxFrameSize {
			return nil, fmt.Errorf("mbp: frame exceeds max frame size of %d bytes", d.MaxFrameSize)
		}
		switch err {
		case nil:
			line = line[:len(line)-1] // drop the trailing \n
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
			return line, nil
		case bufio.ErrBufferFull:
			continue // no newline yet within this chunk; keep reading
		case io.EOF:
			if len(chunk) > 0 {
				return nil, fmt.Errorf("mbp: %w: frame not terminated by newline", io.ErrUnexpectedEOF)
			}
			return nil, io.EOF
		default:
			return nil, err
		}
	}
}

// hasKey reports whether raw JSON object fields contains key.
func hasKey(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}

// Decode reads one line and parses it into a Frame.
//
// Frames are discriminated by which of three keys are present, checked in
// this order: "event" first, then "ok", then "method". None of the four
// documented shapes in architecture.md §7.2 is actually ambiguous under
// this scheme — a request has "method" and neither "ok" nor "event"; a
// response has "ok" and neither "event" nor "method"; an event has "event"
// and neither "id" nor "ok" — so in principle the three checks could run in
// any order. The order is fixed anyway, and checked in this priority,
// because "event" is the one key whose presence rules out an "id" ever
// being meaningful on that frame, so ruling it in first keeps the
// response/request checks below it simple ("has ok" / "has method")
// instead of each having to also say "and is not an event". Checking "ok"
// before "method" mirrors the wire's own precedence: a response is the
// steady-state traffic on this protocol (one per request), so classifying
// it needs no more than a single boolean-field lookup, whereas "method" is
// checked last as the residual case. A line with none of the three keys is
// a protocol error, not a silently-ignored frame.
func (d *Decoder) Decode() (Frame, error) {
	line, err := d.readLine()
	if err != nil {
		return nil, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nil, fmt.Errorf("mbp: malformed frame: %w", err)
	}

	switch {
	case hasKey(fields, "event"):
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("mbp: malformed event frame: %w", err)
		}
		return &ev, nil
	case hasKey(fields, "ok"):
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("mbp: malformed response frame: %w", err)
		}
		return &resp, nil
	case hasKey(fields, "method"):
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			return nil, fmt.Errorf("mbp: malformed request frame: %w", err)
		}
		return &req, nil
	default:
		return nil, fmt.Errorf("mbp: protocol error: frame has none of \"event\", \"ok\", or \"method\": %s", line)
	}
}
