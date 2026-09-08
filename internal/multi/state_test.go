package multi

import (
	"math"
	"testing"
)

// rawSample returns a representative (not exhaustive) subset of the ~173
// fields GetCurPrInfo("") returns, including the two documented traps: keys
// with trailing spaces baked into the key itself, and pcLast's all-Fs
// sentinel value.
func rawSample() map[string]string {
	return map[string]string{
		"stopStamp":           "0x2a",
		"contCount":           "0",
		"pc":                  "0x00c0ffee",
		"pcLast":              "0xffffffffffffffff",
		"file":                "fixture_unit.c",
		"iln":                 "137",
		"proc":                "fixture_entry",
		"pid":                 "424242",
		"stackdepth":          "3",
		"fStoppedOnException": "0",
		"fDownloaded":         "1",
		"adrRamBootEnd    ":   "0x13579bdf",
		"adrRomStart      ":   "0x02468ace",
		"adrRomEnd        ":   "0x0badc0de",
		"adrRamStart      ":   "0x01234567",
		"adrRamEnd        ":   "0x07654321",
	}
}

func TestParseProcessInfoKeepsRawVerbatim(t *testing.T) {
	raw := rawSample()
	p := ParseProcessInfo(raw)
	if len(p.Raw) != len(raw) {
		t.Fatalf("Raw has %d entries, want %d - fields must never be discarded", len(p.Raw), len(raw))
	}
	if p.Raw["adrRamStart      "] != "0x01234567" {
		t.Fatal("Raw must keep the original space-padded key untouched")
	}
}

func TestFieldTrimmedKeyLookup(t *testing.T) {
	p := ParseProcessInfo(rawSample())

	// The trap: the raw map's key is "adrRamStart      " (trailing
	// spaces baked into the key), but callers ask for the clean name.
	v, ok := p.Field("adrRamStart")
	if !ok {
		t.Fatal("Field(\"adrRamStart\") not found; trimmed-key lookup is broken")
	}
	if v != "0x01234567" {
		t.Fatalf("Field(\"adrRamStart\") = %q, want 0x01234567", v)
	}

	// The padded form itself must also resolve.
	v2, ok2 := p.Field("adrRamStart      ")
	if !ok2 || v2 != v {
		t.Fatalf("Field with the padded key = (%q, %v), want (%q, true)", v2, ok2, v)
	}
}

func TestFieldMissing(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	if _, ok := p.Field("no_such_field"); ok {
		t.Fatal("Field() reported a field that does not exist")
	}
}

func TestStopStampHex(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	got, ok := p.StopStamp()
	if !ok || got != 0x2a {
		t.Fatalf("StopStamp() = (%d, %v), want (0x2a, true)", got, ok)
	}
}

func TestContCountDecimal(t *testing.T) {
	p := ParseProcessInfo(map[string]string{"contCount": "42"})
	got, ok := p.ContCount()
	if !ok || got != 42 {
		t.Fatalf("ContCount() = (%d, %v), want (42, true)", got, ok)
	}
}

func TestPCHex(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	got, ok := p.PC()
	if !ok || got != 0x00c0ffee {
		t.Fatalf("PC() = (0x%x, %v), want (0x00c0ffee, true)", got, ok)
	}
}

func TestPCLastDoesNotOverflow(t *testing.T) {
	// The trap: pcLast can be the all-Fs sentinel, which must parse as
	// uint64 math.MaxUint64, not error out as an overflow.
	p := ParseProcessInfo(rawSample())
	got, ok := p.PCLast()
	if !ok {
		t.Fatal("PCLast() ok = false, want true")
	}
	if got != math.MaxUint64 {
		t.Fatalf("PCLast() = %d, want math.MaxUint64 (%d)", got, uint64(math.MaxUint64))
	}
}

func TestFileAndFunction(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	if got := p.File(); got != "fixture_unit.c" {
		t.Fatalf("File() = %q, want fixture_unit.c", got)
	}
	if got := p.Function(); got != "fixture_entry" {
		t.Fatalf("Function() = %q, want fixture_entry", got)
	}
}

func TestLineDecimal(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	got, ok := p.Line()
	if !ok || got != 137 {
		t.Fatalf("Line() = (%d, %v), want (137, true)", got, ok)
	}
}

func TestLineNoneSentinel(t *testing.T) {
	p := ParseProcessInfo(map[string]string{"iln": "-1"})
	got, ok := p.Line()
	if !ok {
		t.Fatal("Line() ok = false for a parseable -1, want true")
	}
	if got != -1 {
		t.Fatalf("Line() = %d, want -1 (MULTI's none sentinel)", got)
	}
}

func TestPID(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	got, ok := p.PID()
	if !ok || got != 424242 {
		t.Fatalf("PID() = (%d, %v), want (424242, true)", got, ok)
	}
}

func TestStackDepth(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	got, ok := p.StackDepth()
	if !ok || got != 3 {
		t.Fatalf("StackDepth() = (%d, %v), want (3, true)", got, ok)
	}
}

func TestStackDepthUnknownSentinel(t *testing.T) {
	p := ParseProcessInfo(map[string]string{"stackdepth": "-1"})
	got, ok := p.StackDepth()
	if !ok || got != -1 {
		t.Fatalf("StackDepth() = (%d, %v), want (-1, true)", got, ok)
	}
}

func TestStoppedOnException(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	if p.StoppedOnException() {
		t.Fatal("StoppedOnException() = true for fStoppedOnException=0")
	}
	p2 := ParseProcessInfo(map[string]string{"fStoppedOnException": "1"})
	if !p2.StoppedOnException() {
		t.Fatal("StoppedOnException() = false for fStoppedOnException=1")
	}
}

func TestDownloaded(t *testing.T) {
	p := ParseProcessInfo(rawSample())
	if !p.Downloaded() {
		t.Fatal("Downloaded() = false for fDownloaded=1")
	}
}

func TestMemoryMapAccessorsUseSpacePaddedKeys(t *testing.T) {
	p := ParseProcessInfo(rawSample())

	romStart, ok := p.RomStart()
	if !ok || romStart != 0x02468ace {
		t.Fatalf("RomStart() = (0x%x, %v), want (0x02468ace, true)", romStart, ok)
	}
	romEnd, ok := p.RomEnd()
	if !ok || romEnd != 0x0badc0de {
		t.Fatalf("RomEnd() = (0x%x, %v), want (0x0badc0de, true)", romEnd, ok)
	}
	ramStart, ok := p.RamStart()
	if !ok || ramStart != 0x01234567 {
		t.Fatalf("RamStart() = (0x%x, %v), want (0x01234567, true)", ramStart, ok)
	}
	ramEnd, ok := p.RamEnd()
	if !ok || ramEnd != 0x07654321 {
		t.Fatalf("RamEnd() = (0x%x, %v), want (0x07654321, true)", ramEnd, ok)
	}
}

func TestMissingNumericFieldReportsNotOK(t *testing.T) {
	p := ParseProcessInfo(map[string]string{})
	if _, ok := p.PC(); ok {
		t.Fatal("PC() ok = true for an absent field")
	}
	if _, ok := p.StopStamp(); ok {
		t.Fatal("StopStamp() ok = true for an absent field")
	}
}

func TestMalformedNumericFieldReportsNotOK(t *testing.T) {
	// A malformed value must fail closed (ok=false), never panic and
	// never silently produce a wrong-but-plausible number.
	p := ParseProcessInfo(map[string]string{"pc": "not-a-number"})
	if _, ok := p.PC(); ok {
		t.Fatal("PC() ok = true for a malformed value, want false")
	}
}
