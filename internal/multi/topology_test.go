package multi

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestResolveTopologyBindsConfiguredELFsNotPIDOrSlot(t *testing.T) {
	components := componentInventory(
		"fixture.component.19 (debugger.name.not-an-elf)",
		"fixture.component.7 (debugger.name.also-not-an-elf)",
		"fixture.component.3 (debugger.pid.999999)",
		"fixture.component.4 (debugger.pid.1)",
	)
	driver, caller := newTestDriver(t,
		fakeCall{method: "cores", params: map[string]string{}, result: commandText("components", components)},
		fakeCall{method: "run_commands", params: command("route fixture.component.19 P"), result: commandText("raw", processTable(555, 0xdead, StatusRunning, `Z:\Boards\hsm\..\HSM\BETA.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.7 P"), result: commandText("raw", processTable(1, 0x1, StatusStopped, `x:/boards/host/ALPHA.elf`))},
	)
	topology, err := ResolveTopology(context.Background(), driver, []CoreSpec{
		{ID: 19, ELF: `Z:\boards\HSM\beta.elf`},
		{ID: 3, ELF: `X:\boards\host\.\alpha.elf`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := topology.Cores(); len(got) != 2 || got[0].ID != 3 || got[1].ID != 19 {
		t.Fatalf("Topology.Cores() = %#v", got)
	}
	view := topology.Cores()
	view[0].ELF = "mutated"
	if got := topology.Cores()[0].ELF; got == "mutated" {
		t.Fatal("Topology.Cores() exposed mutable backing storage")
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestResolveTopologyRejectsInvalidSpecsBeforeIO(t *testing.T) {
	tests := []struct {
		name  string
		specs []CoreSpec
	}{
		{"empty", nil},
		{"negative ID", []CoreSpec{{ID: -1, ELF: `X:\a.elf`}}},
		{"duplicate ID", []CoreSpec{{ID: 1, ELF: `X:\a.elf`}, {ID: 1, ELF: `X:\b.elf`}}},
		{"duplicate canonical ELF", []CoreSpec{{ID: 1, ELF: `X:\A.elf`}, {ID: 2, ELF: `x:/a.elf`}}},
		{"relative ELF", []CoreSpec{{ID: 1, ELF: `a.elf`}}},
		{"UNC share only", []CoreSpec{{ID: 1, ELF: `\\server\share`}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver, caller := newTestDriver(t)
			if _, err := ResolveTopology(context.Background(), driver, test.specs); err == nil {
				t.Fatal("ResolveTopology() succeeded")
			}
			if len(caller.calls) != 0 {
				t.Fatalf("invalid spec performed I/O: %#v", caller.calls)
			}
		})
	}
}

func TestCanonicalWindowsELF(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{`X:\one\\two\.\three.elf`, `x:\one\two\three.elf`},
		{`x:/one/two/../three.elf`, `x:\one\three.elf`},
		{`\\Server\Share\dir\..\firmware.elf`, `\\server\share\firmware.elf`},
	}
	for _, test := range tests {
		got, err := canonicalWindowsELF(test.value)
		if err != nil || got != test.want {
			t.Fatalf("canonicalWindowsELF(%q) = (%q, %v), want (%q, nil)", test.value, got, err, test.want)
		}
	}
}

func TestResolveTopologyRejectsProgramComponentCardinalityAndDuplicates(t *testing.T) {
	specs := []CoreSpec{{ID: 1, ELF: `X:\one.elf`}}
	tests := []struct {
		name       string
		components string
		calls      []fakeCall
	}{
		{"missing", componentInventory(), nil},
		{"extra", componentInventory("fixture.component.1 (debugger.name.one)", "fixture.component.2 (debugger.name.two)"), nil},
		{"duplicate ID", componentInventory("fixture.component.1 (debugger.name.one)", "fixture.component.1 (debugger.name.two)"), nil},
		{"unsafe ID", componentInventory("9fixture.component.1 (debugger.name.one)"), nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := append([]fakeCall{{method: "cores", params: map[string]string{}, result: commandText("components", test.components)}}, test.calls...)
			driver, caller := newTestDriver(t, calls...)
			if _, err := ResolveTopology(context.Background(), driver, specs); err == nil {
				t.Fatal("ResolveTopology() succeeded")
			}
			if len(caller.calls) != 0 {
				t.Fatalf("unexpected additional I/O: %#v", caller.calls)
			}
		})
	}
}

func TestResolveTopologyFailsClosedForRoutedProcessAnomalies(t *testing.T) {
	components := componentInventory("fixture.component.1 (debugger.name.alias)")
	tests := []struct {
		name   string
		result any
	}{
		{"no selected", commandText("raw", unselectedProcessTable())},
		{"duplicate selected", commandText("raw", duplicateSelectedProcessTable())},
		{"empty program", commandText("raw", processTable(0, 1, StatusStopped, ""))},
		{"unstable status", commandText("raw", processTable(0, 1, StatusExecuting, `X:\one.elf`))},
		{"lossy", map[string]any{"status": 1, "raw": processTable(0, 1, StatusStopped, `X:\one.elf`), "raw_lossy": true}},
		{"refused", map[string]any{"status": 0, "raw": ""}},
		{"unconfigured program", commandText("raw", processTable(0, 1, StatusStopped, `X:\two.elf`))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver, caller := newTestDriver(t,
				fakeCall{method: "cores", params: map[string]string{}, result: commandText("components", components)},
				fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: test.result},
			)
			if _, err := ResolveTopology(context.Background(), driver, []CoreSpec{{ID: 1, ELF: `X:\one.elf`}}); err == nil {
				t.Fatal("ResolveTopology() succeeded")
			}
			if len(caller.calls) != 0 {
				t.Fatalf("unconsumed calls: %#v", caller.calls)
			}
		})
	}
}

func TestResolveTopologyRejectsDuplicateAndMissingConfiguredBindings(t *testing.T) {
	components := componentInventory(
		"fixture.component.1 (debugger.name.one)",
		"fixture.component.2 (debugger.name.two)",
	)
	driver, caller := newTestDriver(t,
		fakeCall{method: "cores", params: map[string]string{}, result: commandText("components", components)},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: commandText("raw", processTable(0, 1, StatusStopped, `X:\one.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.2 P"), result: commandText("raw", processTable(17, 2, StatusRunning, `X:\one.elf`))},
	)
	if _, err := ResolveTopology(context.Background(), driver, []CoreSpec{{ID: 1, ELF: `X:\one.elf`}, {ID: 2, ELF: `X:\two.elf`}}); err == nil {
		t.Fatal("ResolveTopology() accepted duplicate program binding")
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestTopologyRoutedRejectsProgramDriftBeforeActualCommand(t *testing.T) {
	components := componentInventory("fixture.component.1 (debugger.name.alias)")
	driver, caller := newTestDriver(t,
		fakeCall{method: "cores", params: map[string]string{}, result: commandText("components", components)},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: commandText("raw", processTable(0, 1, StatusStopped, `X:\one.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: commandText("raw", processTable(789, 0xfed, StatusRunning, `X:\two.elf`))},
	)
	topology, err := ResolveTopology(context.Background(), driver, []CoreSpec{{ID: 42, ELF: `X:\one.elf`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := topology.routed(context.Background(), driver, 42, "calls"); err == nil || !strings.Contains(err.Error(), "drifted") {
		t.Fatalf("Topology.routed() error = %v, want drift failure", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("drift issued an actual command: %#v", caller.calls)
	}
}

func TestTopologyRoutedRevalidatesThenRunsCommand(t *testing.T) {
	components := componentInventory("fixture.component.1 (debugger.name.alias)")
	driver, caller := newTestDriver(t,
		fakeCall{method: "cores", params: map[string]string{}, result: commandText("components", components)},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: commandText("raw", processTable(0, 1, StatusStopped, `X:\one.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: commandText("raw", processTable(1234, 0xbeef, StatusRunning, `x:/ONE.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 calls"), result: commandText("raw", "0_ fixture_entry\t[X:/fixture.c:1,1]\n")},
	)
	topology, err := ResolveTopology(context.Background(), driver, []CoreSpec{{ID: 42, ELF: `X:\one.elf`}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := topology.routed(context.Background(), driver, 42, "calls")
	if err != nil || result.Raw == "" {
		t.Fatalf("Topology.routed() = (%#v, %v)", result, err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func componentInventory(rows ...string) string {
	if len(rows) == 0 {
		return "The currently registered components are:\n"
	}
	return "The currently registered components are:\n" + strings.Join(rows, "\n") + "\n"
}

func command(value string) map[string]string { return map[string]string{"commands": value} }

func commandText(field, value string) map[string]any {
	return map[string]any{"status": 1, field: value}
}

func processTable(slot, pid uint64, status Status, program string) string {
	return "     # PID        PPID       Status        CBEFITDHR Name and Arguments\n" +
		">>   " + strconv.FormatUint(slot, 10) + " 0x" + strconv.FormatUint(pid, 16) + " N/A        " + status.String() + "       011111101 " + program + "\n"
}

func unselectedProcessTable() string {
	return "     # PID        PPID       Status        CBEFITDHR Name and Arguments\n" +
		"     0 0x1 N/A        Stopped       011111101 X:\\one.elf\n"
}

func duplicateSelectedProcessTable() string {
	return "     # PID        PPID       Status        CBEFITDHR Name and Arguments\n" +
		">>   0 0x1 N/A        Stopped       011111101 X:\\one.elf\n" +
		">>   1 0x2 N/A        Running       011111101 X:\\one.elf\n"
}
