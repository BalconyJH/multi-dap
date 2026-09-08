package multi

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// These parsers consume untrusted debugger text. Keep their work bounded
	// independently of the bridge transport's configured message limit.
	maxInspectionOutputBytes = 1 << 20
	maxInspectionOutputLines = 4096
	maxInspectionLineBytes   = 16 << 10
)

const normalizedCallsCommand = "calls nopar pos notypes"

var (
	stackLine          = regexp.MustCompile(`^\s*(\d+)([_ ])\s+(.+?)\t\[(.*):(-?\d+),(-?\d+)\]\s*$`)
	unsourcedStackLine = regexp.MustCompile(`^\s*(\d+)([_ ])\s+([A-Za-z_][A-Za-z0-9_]*)\(\)\s*$`)
	programCounterLine = regexp.MustCompile(`^\$pc = 0x([0-9A-Fa-f]{1,16})\n?$`)

	// ErrInspectionFormat marks accepted, complete inspection text whose
	// documented and hardware-captured grammar is not understood. It is not a
	// bridge protocol error and must not make a stopped actor terminal.
	ErrInspectionFormat = errors.New("multi: unsupported inspection text format")
)

// StackFrame is a frame reported by calls. Source paths stay as MULTI emitted
// them; source canonicalization belongs to Debugger Core (architecture §6.6).
type StackFrame struct {
	Index    uint64
	Selected bool
	Function string
	Path     string
	Line     int64
	Column   int64
}

// ParseStack parses the normalized calls output captured from MULTI 7.1.6d.
// Source rows are pinned by testdata/stack.txt. The documented `nopar` form
// also has one captured no-source row: an identifier followed by empty
// parentheses. Any richer source-less text remains unsupported.
func ParseStack(output string) (frames []StackFrame, err error) {
	defer func() { err = inspectionFormatError("stack", err) }()
	lines, err := inspectionOutputLines("stack", output)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := stackLine.FindStringSubmatch(line)
		if match == nil {
			match = unsourcedStackLine.FindStringSubmatch(line)
		}
		if match == nil {
			return nil, fmt.Errorf("parse stack: malformed frame")
		}
		index, ok := parseUint(match[1])
		if !ok {
			return nil, fmt.Errorf("parse stack: invalid frame index")
		}
		frame := StackFrame{Index: index, Selected: match[2] == "_"}
		if len(match) == 4 {
			frame.Function = match[3] + "()"
			frames = append(frames, frame)
			continue
		}
		lineNumber, ok := parseInt(match[5])
		if !ok {
			return nil, fmt.Errorf("parse stack: invalid line")
		}
		column, ok := parseInt(match[6])
		if !ok {
			return nil, fmt.Errorf("parse stack: invalid column")
		}
		frame.Function = strings.TrimSpace(match[3])
		frame.Path = match[4]
		frame.Line = lineNumber
		frame.Column = column
		frames = append(frames, frame)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("parse stack: no frames")
	}
	return frames, nil
}

// ParseProgramCounter parses the exact output captured from MULTI 7.1.6d for
// `print /x $pc`. The result is an address observation, not an expression:
// tolerate only its one optional terminal newline and reject all decoration,
// truncation, and values wider than an unsigned 64-bit address.
func ParseProgramCounter(output string) (address uint64, err error) {
	defer func() { err = inspectionFormatError("program counter", err) }()
	if len(output) > maxInspectionOutputBytes {
		return 0, fmt.Errorf("parse program counter: output exceeds limit")
	}
	match := programCounterLine.FindStringSubmatch(output)
	if match == nil {
		return 0, fmt.Errorf("parse program counter: malformed value")
	}
	address, parseErr := strconv.ParseUint(match[1], 16, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("parse program counter: invalid address")
	}
	return address, nil
}

// ValueState describes liveness annotations without exposing their MULTI text
// spelling to Debugger Core.
type ValueState uint8

const (
	ValueAvailable ValueState = iota
	ValueNotInitialized
	ValueDead
	ValueStateUnknown
)

// NamedValue is a one-level local or evaluated value. Display is intentionally
// presentation-ready because MULTI has no structured value API; Children are
// obtained by issuing print on the value's expression path (§7.3).
type NamedValue struct {
	Name        string
	Display     string
	State       ValueState
	Hidden      bool
	HasChildren bool
}

var livenessAnnotation = regexp.MustCompile(`\s+<<\s*(.*?)\s*>>\s*$`)

// ParseLocals parses l output captured from MULTI 7.1.6d. The format is
// undocumented and pinned by testdata/locals.txt.
func ParseLocals(output string) (values []NamedValue, err error) {
	defer func() { err = inspectionFormatError("locals", err) }()
	// MULTI returns an exact empty string for an accepted current frame with no
	// locals or parameters. Whitespace-only text is not that sentinel.
	if output == "" {
		return []NamedValue{}, nil
	}
	lines, err := inspectionOutputLines("locals", output)
	if err != nil {
		return nil, err
	}
	for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
		line := lines[lineIndex]
		if indentation(line) != 0 {
			return nil, fmt.Errorf("parse locals: unexpected nested line")
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		value, err := parseNamedValue(line)
		if err != nil {
			return nil, err
		}
		if !isAggregateStart(value.Display) {
			values = append(values, value)
			continue
		}
		// `l` renders an aggregate across several physical lines.  Its nested
		// members are not locals themselves, so consume and structurally check
		// the complete record before proceeding with the next top-level value.
		end, err := assignedAggregateEnd(lines, lineIndex, value.Name)
		if err != nil {
			return nil, err
		}
		value.Display = strings.TrimSpace(strings.TrimSuffix(value.Display, "{"))
		value.HasChildren = true
		values = append(values, value)
		lineIndex = end
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("parse locals: no values")
	}
	return values, nil
}

func parseNamedValue(line string) (NamedValue, error) {
	const hiddenMarker = "*** Caution: variable is hidden or out of scope ***"
	hidden := strings.Contains(line, hiddenMarker)
	line = strings.TrimSpace(strings.ReplaceAll(line, hiddenMarker, ""))
	name, display, found := strings.Cut(line, " = ")
	name = strings.TrimSpace(name)
	display = strings.TrimSpace(display)
	if !found || name == "" || display == "" {
		return NamedValue{}, fmt.Errorf("parse locals: malformed value")
	}
	value := NamedValue{Name: name, Display: display, State: ValueAvailable, Hidden: hidden}
	if annotation := livenessAnnotation.FindStringSubmatch(display); annotation != nil {
		value.Display = strings.TrimSpace(strings.TrimSuffix(display, annotation[0]))
		switch annotation[1] {
		case "Not Initialized":
			value.State = ValueNotInitialized
		case "Dead":
			value.State = ValueDead
		default:
			value.State = ValueStateUnknown
		}
	}
	if strings.HasSuffix(value.Display, ";") {
		value.Display = strings.TrimSpace(strings.TrimSuffix(value.Display, ";"))
		if value.Display == "" {
			return NamedValue{}, fmt.Errorf("parse locals: empty scalar display")
		}
	}
	return value, nil
}

func isAggregateStart(display string) bool {
	return strings.HasSuffix(strings.TrimSpace(display), "{") && strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(display), "{")) != ""
}

// assignedAggregateEnd returns the last line of an `l` aggregate.  The root
// wrapper emitted by MULTI repeats the assigned local name after its closing
// brace.  Count braces only in this constrained structural grammar; anything
// else fails closed rather than accidentally promoting a nested member.
func assignedAggregateEnd(lines []string, start int, name string) (int, error) {
	if _, _, _, ok := parseAggregateMember(name); !ok {
		return 0, fmt.Errorf("parse locals: unsafe aggregate name")
	}
	depth := 1
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || indentation(line) < 0 {
			return 0, fmt.Errorf("parse locals: malformed aggregate")
		}
		trimmed := strings.TrimSpace(line)
		opens, closes := strings.Count(trimmed, "{"), strings.Count(trimmed, "}")
		if opens > 0 && (opens != 1 || !strings.HasSuffix(trimmed, "{") || closes != 0) {
			return 0, fmt.Errorf("parse locals: malformed aggregate braces")
		}
		if closes > 0 {
			if closes != 1 || opens != 0 {
				return 0, fmt.Errorf("parse locals: malformed aggregate braces")
			}
			depth--
			if depth < 0 {
				return 0, fmt.Errorf("parse locals: malformed aggregate braces")
			}
			if depth == 0 {
				if trimmed != "} "+name {
					return 0, fmt.Errorf("parse locals: mismatched aggregate wrapper")
				}
				return i, nil
			}
			if trimmed != "};" {
				return 0, fmt.Errorf("parse locals: malformed nested aggregate")
			}
		}
		depth += opens
	}
	return 0, fmt.Errorf("parse locals: unterminated aggregate")
}

// ParseValue parses one print or eval output line with the same representation
// as ParseLocals. The golden contract is testdata/value.txt.
func ParseValue(output string) (value NamedValue, err error) {
	defer func() { err = inspectionFormatError("value", err) }()
	values, err := ParseLocals(output)
	if err != nil {
		return NamedValue{}, err
	}
	if len(values) != 1 {
		return NamedValue{}, fmt.Errorf("parse value: expected one value")
	}
	return values[0], nil
}

// Aggregate is one print result whose type header and braces prove that MULTI
// expanded precisely one aggregate level. It deliberately retains only the
// display text and safe child-path metadata; neither is a MULTI handle.
type Aggregate struct {
	Display  string
	Children []AggregateChild
}

// AggregateChild is a directly printed aggregate member. Member and optional
// Index are parsed independently from the display so the runner never turns a
// display string into a command operand.
type AggregateChild struct {
	Name        string
	Display     string
	Member      string
	Index       uint64
	HasIndex    bool
	HasChildren bool
}

// ParseAggregate parses the one-level aggregate output captured by M0-4's
// `print *mHsm_IPC_SharedMemPtr`. The format is undocumented and pinned by
// testdata/aggregate.txt. Any layout beyond that captured grammar is rejected
// rather than being guessed into an expression path.
func ParseAggregate(output string) (aggregate Aggregate, err error) {
	defer func() { err = inspectionFormatError("aggregate", err) }()
	lines, err := inspectionOutputLines("aggregate", output)
	if err != nil {
		return Aggregate{}, err
	}
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 2 || indentation(lines[0]) != 0 {
		return Aggregate{}, fmt.Errorf("parse aggregate: malformed header")
	}
	display, wrapperName, assigned, ok := parseAggregateHeader(lines[0])
	if !ok {
		return Aggregate{}, fmt.Errorf("parse aggregate: malformed header")
	}
	result := Aggregate{Display: display}
	directIndent := -1
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return Aggregate{}, fmt.Errorf("parse aggregate: unexpected blank line")
		}
		indent := indentation(line)
		if indent < 0 {
			return Aggregate{}, fmt.Errorf("parse aggregate: tabs are not supported")
		}
		if trimmed == "}" || (assigned && trimmed == "} "+wrapperName) {
			if indent != 0 || i != len(lines)-1 || (assigned && trimmed != "} "+wrapperName) || (!assigned && trimmed != "}") {
				return Aggregate{}, fmt.Errorf("parse aggregate: malformed closing brace")
			}
			return result, nil
		}
		if directIndent < 0 {
			if indent == 0 {
				return Aggregate{}, fmt.Errorf("parse aggregate: missing member indentation")
			}
			directIndent = indent
		}
		if indent != directIndent {
			return Aggregate{}, fmt.Errorf("parse aggregate: unexpected nested line")
		}
		child, nested, err := parseAggregateChild(trimmed)
		if err != nil {
			return Aggregate{}, err
		}
		result.Children = append(result.Children, child)
		if !nested {
			continue
		}
		i++
		nestedIndent := -1
		for ; i < len(lines); i++ {
			nestedLine := lines[i]
			nestedTrimmed := strings.TrimSpace(nestedLine)
			indent := indentation(nestedLine)
			if nestedTrimmed == "" || indent < 0 {
				return Aggregate{}, fmt.Errorf("parse aggregate: malformed nested aggregate")
			}
			if indent == directIndent && nestedTrimmed == "};" {
				break
			}
			if indent <= directIndent {
				return Aggregate{}, fmt.Errorf("parse aggregate: malformed nested aggregate")
			}
			if nestedIndent < 0 {
				nestedIndent = indent
			}
			if indent != nestedIndent {
				return Aggregate{}, fmt.Errorf("parse aggregate: nested aggregate exceeds one level")
			}
			if _, nested, err := parseAggregateChild(nestedTrimmed); err != nil || nested {
				return Aggregate{}, fmt.Errorf("parse aggregate: nested aggregate exceeds one level")
			}
		}
		if i == len(lines) {
			return Aggregate{}, fmt.Errorf("parse aggregate: unterminated nested aggregate")
		}
	}
	return Aggregate{}, fmt.Errorf("parse aggregate: missing closing brace")
}

func inspectionFormatError(operation string, err error) error {
	if err == nil || errors.Is(err, ErrInspectionFormat) {
		return err
	}
	return fmt.Errorf("%w: %s: %v", ErrInspectionFormat, operation, err)
}

func parseAggregateHeader(line string) (display, wrapperName string, assigned, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !isAggregateStart(trimmed) {
		return "", "", false, false
	}
	header := strings.TrimSpace(strings.TrimSuffix(trimmed, "{"))
	name, value, hasAssignment := strings.Cut(header, " = ")
	if !hasAssignment {
		return header, "", false, !strings.Contains(header, "=")
	}
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !isIdentifier(name) || value == "" || strings.Contains(value, "=") {
		return "", "", false, false
	}
	return value, name, true, true
}

func parseAggregateChild(line string) (AggregateChild, bool, error) {
	label, display, found := strings.Cut(line, " = ")
	if !found {
		return AggregateChild{}, false, fmt.Errorf("parse aggregate: malformed member")
	}
	member, index, hasIndex, ok := parseAggregateMember(label)
	if !ok {
		return AggregateChild{}, false, fmt.Errorf("parse aggregate: unsafe member label")
	}
	display = strings.TrimSpace(display)
	if strings.HasSuffix(display, ";") {
		display = strings.TrimSpace(strings.TrimSuffix(display, ";"))
		if display == "" {
			return AggregateChild{}, false, fmt.Errorf("parse aggregate: empty scalar display")
		}
		return AggregateChild{Name: strings.TrimSpace(label), Display: display, Member: member, Index: index, HasIndex: hasIndex}, false, nil
	}
	if !strings.HasSuffix(display, "{") {
		return AggregateChild{}, false, fmt.Errorf("parse aggregate: member is neither scalar nor aggregate")
	}
	display = strings.TrimSpace(strings.TrimSuffix(display, "{"))
	if display == "" {
		return AggregateChild{}, false, fmt.Errorf("parse aggregate: empty aggregate display")
	}
	return AggregateChild{Name: strings.TrimSpace(label), Display: display, Member: member, Index: index, HasIndex: hasIndex, HasChildren: true}, true, nil
}

func parseAggregateMember(label string) (string, uint64, bool, bool) {
	label = strings.TrimSpace(label)
	if isIdentifier(label) {
		return label, 0, false, true
	}
	member, suffix, found := strings.Cut(label, "[")
	if !found || !strings.HasSuffix(suffix, "]") || !isIdentifier(member) {
		return "", 0, false, false
	}
	indexText := strings.TrimSuffix(suffix, "]")
	if indexText == "" || strings.ContainsAny(indexText, "+- ") {
		return "", 0, false, false
	}
	index, err := strconv.ParseUint(indexText, 10, 64)
	if err != nil {
		return "", 0, false, false
	}
	return member, index, true, true
}

func indentation(line string) int {
	for i, r := range line {
		if r == ' ' {
			continue
		}
		if r == '\t' {
			return -1
		}
		return i
	}
	return len(line)
}

func isIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if index == 0 {
			if r != '_' && r != '$' && !isASCIILetter(r) {
				return false
			}
			continue
		}
		if r != '_' && r != '$' && !isASCIILetter(r) && !isASCIIDigit(r) {
			return false
		}
	}
	return true
}

func inspectionOutputLines(operation, output string) ([]string, error) {
	if len(output) > maxInspectionOutputBytes {
		return nil, fmt.Errorf("parse %s: output exceeds limit", operation)
	}
	lines := strings.Split(output, "\n")
	if len(lines) > maxInspectionOutputLines {
		return nil, fmt.Errorf("parse %s: output has too many lines", operation)
	}
	for _, line := range lines {
		if len(line) > maxInspectionLineBytes {
			return nil, fmt.Errorf("parse %s: line exceeds limit", operation)
		}
	}
	return lines, nil
}

func isASCIILetter(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }
