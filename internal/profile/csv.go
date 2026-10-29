package profile

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"hash"
	"io"
	"unicode/utf8"
)

type countedInput struct {
	reader io.Reader
	hash   hash.Hash
	bytes  int64
}

func (input *countedInput) Read(buffer []byte) (int, error) {
	n, err := input.reader.Read(buffer)
	if n > 0 {
		input.bytes += int64(n)
		_, _ = input.hash.Write(buffer[:n])
	}
	return n, err
}

type csvInput struct {
	reader *bufio.Reader
	input  *countedInput
	line   int
}

func newCSVInput(reader io.Reader, allowBOM bool) (*csvInput, error) {
	if reader == nil {
		return nil, failure("input_read_failed")
	}
	input := &countedInput{reader: io.LimitReader(reader, MaxInputBytes+1), hash: sha256.New()}
	csv := &csvInput{reader: bufio.NewReader(input), input: input, line: 1}
	prefix, err := csv.reader.Peek(3)
	if err != nil && err != io.EOF {
		return nil, failure("input_read_failed")
	}
	if bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		if !allowBOM {
			return nil, failure("utf8_bom_not_allowed")
		}
		_, _ = csv.reader.Discard(3)
	}
	return csv, nil
}

// readRecord accepts comma-separated records with LF or CRLF endings and RFC
// quoting (including quoted newlines and escaped quotes). Unlike encoding/csv,
// it never allocates a complete oversized field before applying the cell cap.
// Blank records are returned, not silently skipped.
func (csv *csvInput) readRecord() ([]string, int, error) {
	const (
		fieldStart = iota
		unquoted
		quoted
		afterQuote
	)
	state, startLine := fieldStart, csv.line
	var cells []string
	cell := make([]byte, 0, 64)
	seen := false
	appendByte := func(b byte) error {
		if len(cell) == MaxCellBytes {
			return failure("cell_size_limit")
		}
		cell = append(cell, b)
		return nil
	}
	finishCell := func() error {
		if len(cells) == MaxColumns {
			return failure("column_limit")
		}
		if !utf8.Valid(cell) || bytes.IndexByte(cell, 0) >= 0 {
			return failure("csv_invalid_utf8")
		}
		cells = append(cells, string(cell))
		cell = cell[:0]
		state = fieldStart
		return nil
	}
	for {
		b, err := csv.reader.ReadByte()
		if csv.input.bytes > MaxInputBytes {
			return nil, startLine, failure("input_size_limit")
		}
		if err != nil {
			if err != io.EOF {
				return nil, startLine, failure("input_read_failed")
			}
			if state == quoted {
				return nil, startLine, failure("csv_syntax")
			}
			if !seen {
				return nil, startLine, io.EOF
			}
			if err := finishCell(); err != nil {
				return nil, startLine, err
			}
			return cells, startLine, nil
		}
		seen = true
		if b == '\n' {
			csv.line++
		}
		if state == quoted {
			if b == '"' {
				state = afterQuote
			} else if err := appendByte(b); err != nil {
				return nil, startLine, err
			}
			continue
		}
		if state == afterQuote && b == '"' {
			if err := appendByte(b); err != nil {
				return nil, startLine, err
			}
			state = quoted
			continue
		}
		if b == ',' {
			if err := finishCell(); err != nil {
				return nil, startLine, err
			}
			continue
		}
		if b == '\r' || b == '\n' {
			if b == '\r' {
				next, err := csv.reader.ReadByte()
				if csv.input.bytes > MaxInputBytes {
					return nil, startLine, failure("input_size_limit")
				}
				if err != nil && err != io.EOF {
					return nil, startLine, failure("input_read_failed")
				}
				if err != nil || next != '\n' {
					return nil, startLine, failure("csv_syntax")
				}
				csv.line++
			}
			if err := finishCell(); err != nil {
				return nil, startLine, err
			}
			return cells, startLine, nil
		}
		if state == afterQuote || b == '"' && state != fieldStart {
			return nil, startLine, failure("csv_syntax")
		}
		if state == fieldStart && b == '"' {
			state = quoted
			continue
		}
		state = unquoted
		if err := appendByte(b); err != nil {
			return nil, startLine, err
		}
	}
}
