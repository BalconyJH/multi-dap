package multi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// HaltReason is MULTI's observed halt cause, normalized for Debugger Core.
// It must be corroborated with ProcessInfo because MULTI can retain an old
// breakpoint cause after an explicit halt (architecture.md §6.3).
type HaltReason uint8

const (
	HaltReasonUnknown HaltReason = iota
	HaltReasonUserRequest
	HaltReasonBreakpoint
	HaltReasonNotRunning
)

// HaltCommandToken is an opaque breakpoint-command-list identity. Its
// representation is intentionally private to this package; callers may use it
// only for equality with a token they stored from a breakpoint result.
type HaltCommandToken string

// HaltInfo is the typed result of the undocumented H command output.
type HaltInfo struct {
	Reason       HaltReason
	CommandToken HaltCommandToken
	HasCommand   bool
}

var placerHintCommand = regexp.MustCompile(`^mprintf\("HIT 0x([0-9A-F]{8})\\n"\)$`)

// HintToken extracts a multi-dap breakpoint hint from H only when the command
// list has the byte-exact form emitted by BreakpointPlacer. Arbitrary MULTI
// command lists stay opaque: this method does not expose or interpret their
// text. The zero token is rejected because the breakpoint store never issues
// it and reserves it as an absent sentinel.
func (info HaltInfo) HintToken() (uint32, bool) {
	if !info.HasCommand {
		return 0, false
	}
	match := placerHintCommand.FindStringSubmatch(string(info.CommandToken))
	if len(match) != 2 {
		return 0, false
	}
	value, err := strconv.ParseUint(match[1], 16, 32)
	if err != nil || value == 0 {
		return 0, false
	}
	return uint32(value), true
}

// ParseHaltInfo parses the H command output observed on MULTI 7.1.6d. The
// output contract is pinned by testdata/halt_breakpoint.txt.
func ParseHaltInfo(output string) (HaltInfo, error) {
	var info HaltInfo
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch line {
		case "Halted by user request.":
			if info.Reason != HaltReasonUnknown {
				return HaltInfo{}, fmt.Errorf("parse halt info: multiple causes")
			}
			info.Reason = HaltReasonUserRequest
		case "Halted for breakpoint.":
			if info.Reason != HaltReasonUnknown {
				return HaltInfo{}, fmt.Errorf("parse halt info: multiple causes")
			}
			info.Reason = HaltReasonBreakpoint
		case "Process not running.":
			if info.Reason != HaltReasonUnknown {
				return HaltInfo{}, fmt.Errorf("parse halt info: multiple causes")
			}
			info.Reason = HaltReasonNotRunning
		case "":
			continue
		default:
			if !strings.HasPrefix(line, "Command list was:") {
				return HaltInfo{}, fmt.Errorf("parse halt info: unrecognized line")
			}
		}
		if strings.HasPrefix(line, "Command list was:") {
			if info.HasCommand {
				return HaltInfo{}, fmt.Errorf("parse halt info: multiple command lists")
			}
			token := strings.TrimSpace(strings.TrimPrefix(line, "Command list was:"))
			if len(token) < 2 || token[0] != '{' || token[len(token)-1] != '}' {
				return HaltInfo{}, fmt.Errorf("parse halt info: malformed command list")
			}
			info.CommandToken = HaltCommandToken(token[1 : len(token)-1])
			info.HasCommand = true
		}
	}
	if info.Reason == HaltReasonUnknown {
		return HaltInfo{}, fmt.Errorf("parse halt info: unrecognized cause")
	}
	return info, nil
}
