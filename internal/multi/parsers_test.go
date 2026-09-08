package multi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden fixture %q: %v", name, err)
	}
	return string(data)
}

func TestParseHaltInfo(t *testing.T) {
	info, err := ParseHaltInfo(golden(t, "halt_breakpoint.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Reason != HaltReasonBreakpoint {
		t.Fatalf("Reason = %v, want breakpoint", info.Reason)
	}
	if !info.HasCommand || info.CommandToken != HaltCommandToken(`mprintf("HIT fixture-token\n")`) {
		t.Fatalf("command token = (%q, %v), want scrubbed token and true", info.CommandToken, info.HasCommand)
	}
}

func TestHaltInfoHintTokenOnlyAcceptsPlacerCommand(t *testing.T) {
	info, err := ParseHaltInfo("Halted for breakpoint.\nCommand list was: {mprintf(\"HIT 0x00C0FFEE\\n\")}\n")
	if err != nil {
		t.Fatal(err)
	}
	if token, ok := info.HintToken(); !ok || token != 0xc0ffee {
		t.Fatalf("HintToken() = (%#x, %t), want (0x00c0ffee, true)", token, ok)
	}

	for _, command := range []HaltCommandToken{
		`mprintf("HIT 0x00c0ffee\n")`,
		`mprintf("HIT 0x00C0FFEE\n"); halt`,
		`mprintf("HIT 0x00000000\n")`,
		`print fixture_value`,
		`mprintf("HIT 0x00C0FFEE\n") `,
	} {
		if token, ok := (HaltInfo{HasCommand: true, CommandToken: command}).HintToken(); ok || token != 0 {
			t.Fatalf("HintToken(%q) = (%#x, %t), want (0, false)", command, token, ok)
		}
	}
	if token, ok := (HaltInfo{CommandToken: `mprintf("HIT 0x00C0FFEE\n")`}).HintToken(); ok || token != 0 {
		t.Fatalf("HintToken without HasCommand = (%#x, %t)", token, ok)
	}
}

func TestProcessInfoMixedRadixAndLeadingZeroDecimal(t *testing.T) {
	info := ParseProcessInfo(map[string]string{
		"stopStamp": "0X1a",
		"contCount": "0008",
		"iln":       "-0x2",
	})
	if got, ok := info.StopStamp(); !ok || got != 26 {
		t.Fatalf("StopStamp = (%d, %v), want (26, true)", got, ok)
	}
	if got, ok := info.ContCount(); !ok || got != 8 {
		t.Fatalf("ContCount = (%d, %v), want (8, true)", got, ok)
	}
	if got, ok := info.Line(); !ok || got != -2 {
		t.Fatalf("Line = (%d, %v), want (-2, true)", got, ok)
	}
}

func TestParseHaltInfoOtherCauses(t *testing.T) {
	for input, want := range map[string]HaltReason{
		"Halted by user request.\n": HaltReasonUserRequest,
		"Process not running.\n":    HaltReasonNotRunning,
	} {
		got, err := ParseHaltInfo(input)
		if err != nil || got.Reason != want {
			t.Fatalf("ParseHaltInfo(%q) = (%v, %v), want (%v, nil)", input, got, err, want)
		}
	}
}

func TestParseProcesses(t *testing.T) {
	processes, err := ParseProcesses(golden(t, "processes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) != 3 {
		t.Fatalf("got %d processes, want 3", len(processes))
	}
	first := processes[0]
	if first.Slot != 0 || first.PID != 0x2a || first.Status != StatusStopped || !first.Selected {
		t.Fatalf("first process = %#v", first)
	}
	if processes[1].Program != "X:/fixture/bin/beta.elf" {
		t.Fatalf("program = %q", processes[1].Program)
	}
	if processes[2].Status != StatusRunning || processes[2].Program != "" {
		t.Fatalf("third process = %#v", processes[2])
	}
}

func TestParseComponents(t *testing.T) {
	components, err := ParseComponents(golden(t, "components.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 7 {
		t.Fatalf("got %d components, want 7", len(components))
	}
	if got := components[2]; got.Role != ComponentProgram || got.Name != "X:/fixture/bin/alpha.elf" {
		t.Fatalf("program component = %#v", got)
	}
	if got := components[5]; got.Role != ComponentPIDAlias || got.ProcessID != 1 {
		t.Fatalf("process alias component = %#v", got)
	}
}

func TestParseComponentsRejectsDuplicateIDAcrossRoles(t *testing.T) {
	_, err := ParseComponents("fixture.route (debugger.name.X:/fixture/core.elf)\nfixture.route (debugger.pid.1)\n")
	if err == nil || !strings.Contains(err.Error(), "duplicate component ID") {
		t.Fatalf("ParseComponents() error = %v", err)
	}
}

func TestParseStack(t *testing.T) {
	frames, err := ParseStack(golden(t, "stack.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	if got := frames[0]; !got.Selected || got.Function != "fixture_entry()" || got.Path != "X:/fixture/src/fixture_unit.c" || got.Line != 137 || got.Column != 2 {
		t.Fatalf("first frame = %#v", got)
	}
}

func TestParseStackAcceptsCapturedUnsourcedNormalizedFrame(t *testing.T) {
	frames, err := ParseStack("0_ fixture_reset()\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %#v", frames)
	}
	if got := frames[0]; !got.Selected || got.Function != "fixture_reset()" || got.Path != "" || got.Line != 0 || got.Column != 0 {
		t.Fatalf("unsourced frame = %#v", got)
	}
	for _, malformed := range []string{
		"0_ fixture_reset(0, 0)\n",
		"0_ fixture_reset() 0xDEADBEEF\n",
		"0_ fixture::reset()\n",
	} {
		if _, err := ParseStack(malformed); !errors.Is(err, ErrInspectionFormat) {
			t.Fatalf("ParseStack(%q) error = %v, want ErrInspectionFormat", malformed, err)
		}
	}
}

func TestParseProgramCounter(t *testing.T) {
	address, err := ParseProgramCounter("$pc = 0x00000000\n")
	if err != nil || address != 0 {
		t.Fatalf("ParseProgramCounter() = (%#x, %v), want (0, nil)", address, err)
	}
	address, err = ParseProgramCounter("$pc = 0x1a440780")
	if err != nil || address != 0x1a440780 {
		t.Fatalf("ParseProgramCounter() = (%#x, %v)", address, err)
	}
	for _, malformed := range []string{
		"$pc = 0x1\nextra\n", "$pc = 0x\n", "$pc = 0x10000000000000000\n",
		" pc = 0x1\n", "$pc = 1\n", "$pc = 0x1 truncated\n",
	} {
		if _, err := ParseProgramCounter(malformed); !errors.Is(err, ErrInspectionFormat) {
			t.Fatalf("ParseProgramCounter(%q) error = %v, want ErrInspectionFormat", malformed, err)
		}
	}
}

func TestParseLocals(t *testing.T) {
	values, err := ParseLocals(golden(t, "locals.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 4 {
		t.Fatalf("got %d values, want 4", len(values))
	}
	if got := values[0]; got.Name != "fixture_uninitialized" || got.Display != "17" || got.State != ValueNotInitialized {
		t.Fatalf("first local = %#v", got)
	}
	if got := values[2]; got.Display != "31337 (not defined for enum )" || got.State != ValueDead {
		t.Fatalf("third local = %#v", got)
	}
	if got := values[3]; !got.Hidden || got.Display != "7" {
		t.Fatalf("hidden local = %#v", got)
	}
}

func TestParseLocalsAcceptsCapturedEmptyOutput(t *testing.T) {
	values, err := ParseLocals("")
	if err != nil || len(values) != 0 {
		t.Fatalf("ParseLocals(empty) = %#v, %v", values, err)
	}
	if _, err := ParseLocals("\n"); !errors.Is(err, ErrInspectionFormat) {
		t.Fatalf("ParseLocals(whitespace) error = %v, want ErrInspectionFormat", err)
	}
}

func TestParseLocalsTrimsRenderedScalarTerminator(t *testing.T) {
	values, err := ParseLocals("badKey[0] = 2;\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Name != "badKey[0]" || values[0].Display != "2" {
		t.Fatalf("array element local = %#v", values)
	}
}

func TestParseLocalsAcceptsIndexedAggregateRoot(t *testing.T) {
	values, err := ParseLocals("items[0] = struct Item {\n    value = 7;\n} items[0]\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Name != "items[0]" || values[0].Display != "struct Item" || !values[0].HasChildren {
		t.Fatalf("indexed aggregate local = %#v", values)
	}
}

func TestParseLocalsGroupsAssignedAggregateRecords(t *testing.T) {
	values, err := ParseLocals(golden(t, "locals_aggregate.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Fatalf("got %d values, want 3: %#v", len(values), values)
	}
	if got := values[0]; got.Name != "fixture_aggregate" || got.Display != "volatile struct AggregateResult" || !got.HasChildren || got.State != ValueAvailable || got.Hidden {
		t.Fatalf("aggregate local = %#v", got)
	}
	if got := rawValue(values[0]); !got.HasChildren || got.NamedChildren != -1 || got.IndexedChildren != -1 {
		t.Fatalf("aggregate raw value = %#v", got)
	}
	if got := values[1]; got.Name != "fixture_dead" || got.State != ValueDead || got.HasChildren {
		t.Fatalf("dead local = %#v", got)
	}
	if got := values[2]; got.Name != "fixture_hidden" || !got.Hidden || got.HasChildren {
		t.Fatalf("hidden local = %#v", got)
	}

	for _, malformed := range []string{
		"items[0]; resume = struct Item {\n    value = 7;\n} items[0]; resume\n",
		"root = struct Root {\n    member = 1;\n} other\n",
		"root = struct Root {\n    member = 1;\n}\n",
		"root = struct Root {\n    member = struct Child {\n        leaf = 1;\n}\n} root\n",
		"root = struct Root {\n    member = 1;\n} root\n    nested = 2\n",
	} {
		if _, err := ParseLocals(malformed); !errors.Is(err, ErrInspectionFormat) {
			t.Fatalf("ParseLocals(%q) error = %v, want ErrInspectionFormat", malformed, err)
		}
	}
}

func TestParseValue(t *testing.T) {
	value, err := ParseValue(golden(t, "value.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if value.Name != "fixture_value" || value.Display != "42" || value.State != ValueAvailable {
		t.Fatalf("value = %#v", value)
	}
}

func TestParseAggregate(t *testing.T) {
	aggregate, err := ParseAggregate(golden(t, "aggregate.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Display != "volatile struct FixtureAggregate_t" || len(aggregate.Children) != 16 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
	if got := aggregate.Children[4]; got.Name != "spares[0]" || got.Member != "spares" || !got.HasIndex || got.Index != 0 || got.HasChildren {
		t.Fatalf("indexed scalar child = %#v", got)
	}
	if got := aggregate.Children[8]; got.Name != "items[0]" || got.Member != "items" || !got.HasIndex || got.Index != 0 || !got.HasChildren || got.Display != "0x20 volatile struct FixtureItem_t" {
		t.Fatalf("indexed aggregate child = %#v", got)
	}
	if _, err := ParseAggregate(golden(t, "aggregate_entry.txt")); err != nil {
		t.Fatalf("ParseAggregate(entry) = %v", err)
	}
	assigned := "g_eccEelTestResult = volatile struct EccEelTestResult_t {\n    status = 0;\n} g_eccEelTestResult\n"
	if got, err := ParseAggregate(assigned); err != nil || got.Display != "volatile struct EccEelTestResult_t" || len(got.Children) != 1 {
		t.Fatalf("ParseAggregate(assigned) = (%#v, %v)", got, err)
	}

	fixture := golden(t, "aggregate.txt")
	for _, malformed := range []string{
		strings.Replace(fixture, "sampleTag = 101;", "sampleTag = 101", 1),
		strings.Replace(fixture, "spares[0]", "spares[index]", 1),
		strings.Replace(fixture, "spares[0]", "sp\u00e4res[0]", 1),
		strings.Replace(fixture, "    };\n    items[1]", "    }\n    items[1]", 1),
		"struct Root {\n    member = struct Child {\n        nested = struct Grandchild {\n            leaf = 1;\n        };\n    };\n}\n",
		"root = struct Root {\n    member = 1;\n} anotherRoot\n",
		"root = struct Root {\n    member = 1;\n}\n",
	} {
		if _, err := ParseAggregate(malformed); err == nil {
			t.Fatal("ParseAggregate accepted malformed fixture")
		}
	}
}

func TestInspectionParsersRejectOversizedInput(t *testing.T) {
	oversized := strings.Repeat("x", maxInspectionOutputBytes+1)
	parsers := []struct {
		name  string
		parse func(string) error
	}{
		{"stack", func(text string) error { _, err := ParseStack(text); return err }},
		{"locals", func(text string) error { _, err := ParseLocals(text); return err }},
		{"value", func(text string) error { _, err := ParseValue(text); return err }},
		{"aggregate", func(text string) error { _, err := ParseAggregate(text); return err }},
	}
	for _, parser := range parsers {
		t.Run(parser.name, func(t *testing.T) {
			if err := parser.parse(oversized); err == nil {
				t.Fatalf("%s parser accepted oversized output", parser.name)
			}
		})
	}
}

func TestInspectionParsersMarkFormatErrors(t *testing.T) {
	parsers := []struct {
		name  string
		parse func(string) error
		input string
	}{
		{"stack", func(text string) error { _, err := ParseStack(text); return err }, "broken"},
		{"locals", func(text string) error { _, err := ParseLocals(text); return err }, "broken"},
		{"value", func(text string) error { _, err := ParseValue(text); return err }, "a = 1\nb = 2"},
		{"aggregate", func(text string) error { _, err := ParseAggregate(text); return err }, "broken"},
	}
	for _, parser := range parsers {
		t.Run(parser.name, func(t *testing.T) {
			if err := parser.parse(parser.input); !errors.Is(err, ErrInspectionFormat) {
				t.Fatalf("%s error = %v, want ErrInspectionFormat", parser.name, err)
			}
		})
	}
}

func FuzzInspectionParsersFailClosed(f *testing.F) {
	for _, seed := range []string{
		"\xff\xfe\x00",
		"0_ fn\t[path:1,2]\n",
		"name = value\n",
		"struct Root {\n    child = struct Child {\n        leaf = 1;\n    };\n}\n",
		"struct Root {\n    child[18446744073709551616] = 1;\n}\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		_, _ = ParseStack(text)
		_, _ = ParseLocals(text)
		_, _ = ParseValue(text)
		_, _ = ParseAggregate(text)
	})
}

func TestParseBreakpoints(t *testing.T) {
	breakpoints, err := ParseBreakpoints(golden(t, "breakpoints.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(breakpoints) != 3 {
		t.Fatalf("got %d breakpoints, want 3", len(breakpoints))
	}
	if got := breakpoints[0]; !got.Reached || !got.Enabled || got.Address != 0x00c0ffee {
		t.Fatalf("first breakpoint = %#v", got)
	}
	if got := breakpoints[1]; !got.HasCommand || got.CommandToken != HaltCommandToken(`mprintf("HIT fixture\n")`) {
		t.Fatalf("second breakpoint = %#v", got)
	}
	if breakpoints[2].Enabled {
		t.Fatal("inactive breakpoint parsed as enabled")
	}
}

func TestParseBreakpointsNone(t *testing.T) {
	breakpoints, err := ParseBreakpoints("No software breakpoints set.\n")
	if err != nil || breakpoints != nil {
		t.Fatalf("ParseBreakpoints(no breakpoints) = (%#v, %v), want (nil, nil)", breakpoints, err)
	}
}

func TestParseBreakpointsRejectsDuplicateIndex(t *testing.T) {
	_, err := ParseBreakpoints("0 fixture#1:\t0x1 AT count: 0\n0 fixture#2:\t0x2 AT count: 0\n")
	if err == nil || !strings.Contains(err.Error(), "duplicate index") {
		t.Fatalf("ParseBreakpoints() error = %v", err)
	}
}
