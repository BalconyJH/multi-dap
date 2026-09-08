package multi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ConsoleOutput is the incremental text captured from MULTI's Target and I/O
// debug panes. Server is the Target pane, retained under its DAP-neutral name
// so a caller can decide how to surface it.
type ConsoleOutput struct {
	Server    string
	IO        string
	RawLossy  bool
	Truncated bool
}

// Console obtains one bounded incremental console snapshot. The bridge writes
// panes to private files because savedebugpane is silent on MULTI's Python API.
func (d *Driver) Console(ctx context.Context) (ConsoleOutput, error) {
	result, err := d.callCommand(ctx, "console_read", map[string]string{})
	if err != nil {
		return ConsoleOutput{}, err
	}
	lossy, truncated, err := requireConsoleTextFields(result)
	if err != nil {
		return ConsoleOutput{}, protocolError("console_read", err)
	}
	server, err := decodeString(result["server"])
	if err != nil {
		return ConsoleOutput{}, protocolError("console_read server", err)
	}
	io, err := decodeString(result["io"])
	if err != nil {
		return ConsoleOutput{}, protocolError("console_read io", err)
	}
	return ConsoleOutput{
		Server: sanitizeConsoleText(server), IO: sanitizeConsoleText(io),
		RawLossy: lossy, Truncated: truncated,
	}, nil
}

// ResetConsole forgets only the bridge's incremental console cursors. The
// following Console call therefore returns the current bounded pane contents.
// It never sends a MULTI command or changes target execution state.
func (d *Driver) ResetConsole(ctx context.Context) error {
	result, err := d.callCommand(ctx, "console_reset", map[string]string{})
	if err != nil {
		return err
	}
	if len(result) != 1 {
		return protocolError("console_reset", fmt.Errorf("must contain exactly %q", "reset"))
	}
	reset, ok := result["reset"]
	if !ok {
		return protocolError("console_reset", fmt.Errorf("is missing %q", "reset"))
	}
	var confirmed bool
	if err := json.Unmarshal(reset, &confirmed); err != nil {
		return protocolError("console_reset reset", fmt.Errorf("must be a boolean: %w", err))
	}
	if !confirmed {
		return protocolError("console_reset reset", fmt.Errorf("must be true"))
	}
	return nil
}

func requireConsoleTextFields(fields map[string]json.RawMessage) (bool, bool, error) {
	for name := range fields {
		if name != "server" && name != "io" && name != "raw_lossy" && name != "truncated" {
			return false, false, fmt.Errorf("contains unknown field %q", name)
		}
	}
	if _, ok := fields["server"]; !ok {
		return false, false, fmt.Errorf("is missing %q", "server")
	}
	if _, ok := fields["io"]; !ok {
		return false, false, fmt.Errorf("is missing %q", "io")
	}
	lossy, err := optionalBoolean(fields, "raw_lossy", false)
	if err != nil {
		return false, false, err
	}
	truncated, err := optionalBoolean(fields, "truncated", true)
	if err != nil {
		return false, false, err
	}
	return lossy, truncated, nil
}

func optionalBoolean(fields map[string]json.RawMessage, name string, requireTrue bool) (bool, error) {
	raw, ok := fields[name]
	if !ok {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	if requireTrue && !value {
		return false, fmt.Errorf("%s must be true when present", name)
	}
	return value, nil
}

func sanitizeConsoleText(value string) string {
	const hex = "0123456789abcdef"
	var output strings.Builder
	for _, r := range value {
		if (r >= 0 && r <= 0x1f && r != '\t' && r != '\r' && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			output.WriteString(`\x`)
			output.WriteByte(hex[(r>>4)&0xf])
			output.WriteByte(hex[r&0xf])
			continue
		}
		output.WriteRune(r)
	}
	return output.String()
}
