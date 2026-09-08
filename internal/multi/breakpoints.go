package multi

import (
	"fmt"
	"regexp"
	"strings"
)

var breakpointLine = regexp.MustCompile(`^\s*(\d+)\s+(.+):\s*(0[xX][0-9a-fA-F]+)\s+AT count:\s*(\d+)(.*)$`)

// Breakpoint is MULTI's currently listed breakpoint. Location is deliberately
// opaque to this layer's callers: source canonicalization and DAP breakpoint
// identity mapping are Debugger Core responsibilities.
type Breakpoint struct {
	Index        uint64
	Location     string
	Address      uint64
	HitCount     uint64
	Reached      bool
	Enabled      bool
	CommandToken HaltCommandToken
	HasCommand   bool
}

// ParseBreakpoints parses B command output captured from MULTI 7.1.6d. The
// undocumented text format is pinned by testdata/breakpoints.txt.
func ParseBreakpoints(output string) ([]Breakpoint, error) {
	if strings.TrimSpace(output) == "No software breakpoints set." {
		return nil, nil
	}
	var breakpoints []Breakpoint
	seenIndexes := make(map[uint64]struct{})
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := breakpointLine.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("parse breakpoints: malformed row")
		}
		index, ok := parseUint(match[1])
		if !ok {
			return nil, fmt.Errorf("parse breakpoints: invalid index")
		}
		if _, duplicate := seenIndexes[index]; duplicate {
			return nil, fmt.Errorf("parse breakpoints: duplicate index")
		}
		seenIndexes[index] = struct{}{}
		address, ok := parseUint(match[3])
		if !ok {
			return nil, fmt.Errorf("parse breakpoints: invalid address")
		}
		hitCount, ok := parseUint(match[4])
		if !ok {
			return nil, fmt.Errorf("parse breakpoints: invalid hit count")
		}
		tail := match[5]
		breakpoint := Breakpoint{
			Index:    index,
			Location: strings.TrimSpace(match[2]),
			Address:  address,
			HitCount: hitCount,
			Reached:  strings.Contains(tail, "reached=1"),
			Enabled:  !strings.Contains(tail, "(inactive)"),
		}
		if start := strings.Index(tail, "<{"); start >= 0 {
			end := strings.LastIndex(tail, "}>")
			if end <= start+1 {
				return nil, fmt.Errorf("parse breakpoints: malformed command list")
			}
			breakpoint.CommandToken = HaltCommandToken(tail[start+2 : end])
			breakpoint.HasCommand = true
		}
		breakpoints = append(breakpoints, breakpoint)
	}
	if len(breakpoints) == 0 {
		return nil, fmt.Errorf("parse breakpoints: no rows")
	}
	return breakpoints, nil
}
