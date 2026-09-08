package multi

import (
	"strconv"
	"strings"
)

// ProcessInfo is the typed view of the undocumented dictionary returned by
// GetCurPrInfo(""). Raw deliberately retains every field exactly as MULTI
// supplied it, including space-padded keys. The accessors below are the only
// interface Debugger Core needs for the fields established in M0.
type ProcessInfo struct {
	Raw     map[string]string
	trimmed map[string]string
}

// ParseProcessInfo retains all raw fields and builds a second, whitespace-free
// key index for the handful of known fields with padded names. GetCurPrInfo is
// undocumented MULTI output; state_test.go is its contract.
func ParseProcessInfo(raw map[string]string) ProcessInfo {
	p := ProcessInfo{
		Raw:     make(map[string]string, len(raw)),
		trimmed: make(map[string]string, len(raw)),
	}
	for key, value := range raw {
		p.Raw[key] = value
		trimmedKey := strings.TrimSpace(key)
		if _, exists := p.trimmed[trimmedKey]; !exists {
			p.trimmed[trimmedKey] = value
		}
	}
	return p
}

// Field returns a raw field by its exact key, or by its whitespace-free key
// when MULTI has padded the original key with trailing spaces.
func (p ProcessInfo) Field(key string) (string, bool) {
	if value, ok := p.Raw[key]; ok {
		return value, true
	}
	value, ok := p.trimmed[strings.TrimSpace(key)]
	return value, ok
}

func (p ProcessInfo) StopStamp() (uint64, bool) { return p.uintField("stopStamp") }
func (p ProcessInfo) ContCount() (uint64, bool) { return p.uintField("contCount") }
func (p ProcessInfo) PC() (uint64, bool)        { return p.uintField("pc") }
func (p ProcessInfo) PCLast() (uint64, bool)    { return p.uintField("pcLast") }
func (p ProcessInfo) PID() (uint64, bool)       { return p.uintField("pid") }
func (p ProcessInfo) StackDepth() (int64, bool) { return p.intField("stackdepth") }
func (p ProcessInfo) Line() (int64, bool)       { return p.intField("iln") }
func (p ProcessInfo) RomStart() (uint64, bool)  { return p.uintField("adrRomStart") }
func (p ProcessInfo) RomEnd() (uint64, bool)    { return p.uintField("adrRomEnd") }
func (p ProcessInfo) RamStart() (uint64, bool)  { return p.uintField("adrRamStart") }
func (p ProcessInfo) RamEnd() (uint64, bool)    { return p.uintField("adrRamEnd") }

func (p ProcessInfo) File() string {
	value, _ := p.Field("file")
	return value
}

func (p ProcessInfo) Function() string {
	value, _ := p.Field("proc")
	return value
}

func (p ProcessInfo) StoppedOnException() bool { return p.flag("fStoppedOnException") }
func (p ProcessInfo) Downloaded() bool         { return p.flag("fDownloaded") }

func (p ProcessInfo) flag(key string) bool {
	value, ok := p.Field(key)
	return ok && strings.TrimSpace(value) == "1"
}

func (p ProcessInfo) uintField(key string) (uint64, bool) {
	value, ok := p.Field(key)
	if !ok {
		return 0, false
	}
	return parseUint(value)
}

func (p ProcessInfo) intField(key string) (int64, bool) {
	value, ok := p.Field(key)
	if !ok {
		return 0, false
	}
	return parseInt(value)
}

// parseUint accepts the mixed decimal and prefixed-radix spelling captured
// from MULTI. Decimal is intentionally parsed as base 10 even with leading
// zeroes; Go's base-0 parser would incorrectly treat such values as octal.
func parseUint(value string) (uint64, bool) {
	value = strings.TrimSpace(value)
	base := 10
	if hasRadixPrefix(value) {
		base = 0
	}
	parsed, err := strconv.ParseUint(value, base, 64)
	return parsed, err == nil
}

func parseInt(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	base := 10
	unsigned := strings.TrimPrefix(strings.TrimPrefix(value, "+"), "-")
	if hasRadixPrefix(unsigned) {
		base = 0
	}
	parsed, err := strconv.ParseInt(value, base, 64)
	return parsed, err == nil
}

func hasRadixPrefix(value string) bool {
	return len(value) >= 2 && value[0] == '0' && (value[1] == 'x' || value[1] == 'X' || value[1] == 'b' || value[1] == 'B' || value[1] == 'o' || value[1] == 'O')
}
