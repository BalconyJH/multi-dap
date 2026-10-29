package perf

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Tacrolimus/multi-dap/internal/dap"
)

type stream struct {
	header      []byte
	body        []byte
	bodyLength  int
	readingBody bool
	disabled    bool
}

func (s *stream) partial() bool { return len(s.header) != 0 || s.readingBody }

func (s *stream) clear() {
	clear(s.header)
	clear(s.body)
	s.header = nil
	s.body = nil
	s.bodyLength = 0
	s.readingBody = false
}

func (r *Recorder) disable(direction *stream) {
	direction.clear()
	direction.disabled = true
	r.meta.FramingErrors++
}

func (r *Recorder) observe(direction *stream, data []byte, client bool) {
	for len(data) > 0 && !direction.disabled {
		if !direction.readingBody {
			if direction.header == nil {
				direction.header = make([]byte, 0, dap.DefaultMaxHeaderLength)
			}
			direction.header = append(direction.header, data[0])
			data = data[1:]
			if bytes.HasSuffix(direction.header, []byte("\r\n\r\n")) {
				length, valid := contentLength(direction.header)
				if !valid {
					r.disable(direction)
					return
				}
				clear(direction.header)
				direction.header = direction.header[:0]
				direction.bodyLength = length
				direction.body = make([]byte, 0, length)
				direction.readingBody = true
			} else if len(direction.header) == dap.DefaultMaxHeaderLength {
				r.disable(direction)
				return
			} else {
				continue
			}
		}
		take := min(len(data), direction.bodyLength-len(direction.body))
		direction.body = append(direction.body, data[:take]...)
		data = data[take:]
		if len(direction.body) != direction.bodyLength {
			continue
		}
		message, valid := decodeMessage(direction.body)
		clear(direction.body)
		direction.body = nil
		direction.readingBody = false
		direction.bodyLength = 0
		if !valid {
			r.disable(direction)
			return
		}
		timestamp := r.timestamp()
		switch {
		case client && message.kind == "request":
			r.request(message, timestamp)
		case !client && message.kind == "response":
			r.response(message, timestamp)
		case !client && message.kind == "event":
			r.event(message, timestamp)
		default:
			// Reverse requests are not part of this adapter's supported
			// vocabulary. Forward them unchanged but flag incomplete coverage.
			r.meta.IgnoredMessages++
		}
	}
}

func contentLength(header []byte) (int, bool) {
	if len(header) > dap.DefaultMaxHeaderLength {
		return 0, false
	}
	lines := bytes.Split(header[:len(header)-4], []byte("\r\n"))
	found := false
	length := 0
	for _, line := range lines {
		if bytes.ContainsAny(line, "\r\n") {
			return 0, false
		}
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok || !strings.EqualFold(string(name), "Content-Length") || found {
			return 0, false
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 {
			return 0, false
		}
		for _, b := range value {
			if b < '0' || b > '9' {
				return 0, false
			}
		}
		parsed, err := strconv.ParseUint(string(value), 10, 64)
		if err != nil || parsed > dap.DefaultMaxContentLength {
			return 0, false
		}
		length = int(parsed)
		found = true
	}
	return length, found
}

type message struct {
	seq        int64
	kind       string
	command    string
	requestSeq int64
	success    bool
	event      string
}

func decodeMessage(body []byte) (message, bool) {
	// Payload fields are deliberately absent. The JSON decoder validates them
	// syntactically but never copies them into the recorder's retained state.
	var envelope struct {
		Seq        int64  `json:"seq"`
		Type       string `json:"type"`
		Command    string `json:"command"`
		RequestSeq int64  `json:"request_seq"`
		Success    *bool  `json:"success"`
		Event      string `json:"event"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return message{}, false
	}
	validSeq := func(value int64) bool { return value > 0 && value <= 1<<31-1 }
	if !validSeq(envelope.Seq) {
		return message{}, false
	}
	result := message{
		seq: envelope.Seq, kind: envelope.Type, command: envelope.Command,
		requestSeq: envelope.RequestSeq, event: envelope.Event,
	}
	switch envelope.Type {
	case "request":
		if envelope.Command == "" {
			return message{}, false
		}
	case "response":
		if envelope.Command == "" || !validSeq(envelope.RequestSeq) || envelope.Success == nil {
			return message{}, false
		}
		result.success = *envelope.Success
	case "event":
		if envelope.Event == "" {
			return message{}, false
		}
	default:
		return message{}, false
	}
	return result, true
}
