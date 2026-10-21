package multi

import "testing"

func TestParseComponentsMultiAliasCapture(t *testing.T) {
	components, err := ParseComponents(golden(t, "components_multi_alias.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 5 {
		t.Fatalf("got %d components, want 5", len(components))
	}
	for index, processID := range []uint64{1, 2} {
		component := components[index+3]
		if component.Role != ComponentPIDAlias || component.ProcessID != processID || component.Name != "" {
			t.Fatalf("process component = %#v", component)
		}
	}
}

func TestParseComponentsAliasLists(t *testing.T) {
	tests := []struct {
		name       string
		descriptor string
		role       ComponentRole
		processID  uint64
		program    string
	}{
		{"PID last", "debugger.core.0, debugger.core.850eserv2.0, debugger.pid.1", ComponentPIDAlias, 1, ""},
		{"PID middle", "debugger.core.0,debugger.pid.2,debugger.core.850eserv2.0", ComponentPIDAlias, 2, ""},
		{"cores only", "debugger.core.0, debugger.core.850eserv2.0", ComponentUnknown, 0, ""},
		{"program and PID", "debugger.pid.7, debugger.core.0, debugger.name.X:/fixture/one.elf", ComponentProgram, 7, "X:/fixture/one.elf"},
		{"comma in program", "debugger.name.X:/fixture/one,two.elf", ComponentProgram, 0, "X:/fixture/one,two.elf"},
		{"comma and spaces in program", "debugger.pid.7, debugger.name.X:/fixture/one, two.elf", ComponentProgram, 7, "X:/fixture/one, two.elf"},
		{"unknown debugger prefix in program", "debugger.name.X:/fixture/one,debugger.trace.elf", ComponentProgram, 0, "X:/fixture/one,debugger.trace.elf"},
		{"unknown debugger prefix with spaces", "debugger.name.X:/fixture/one, debugger.foo.elf", ComponentProgram, 0, "X:/fixture/one, debugger.foo.elf"},
		{"unknown debugserver prefix in program", "debugger.name.X:/fixture/one,debugserver.trace.elf", ComponentProgram, 0, "X:/fixture/one,debugserver.trace.elf"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			components, err := ParseComponents("component.1 (" + test.descriptor + ")\n")
			if err != nil {
				t.Fatal(err)
			}
			got := components[0]
			if got.Role != test.role || got.ProcessID != test.processID || got.Name != test.program {
				t.Fatalf("component = %#v", got)
			}
		})
	}
}

func TestParseComponentsRejectsInvalidAliasLists(t *testing.T) {
	descriptors := []string{
		"debugger.pid.0, debugger.core.0",
		"debugger.pid.-1, debugger.core.0",
		"debugger.core.0, debugger.pid.",
		"debugger.core.0, debugger.pid.invalid",
		"debugger.core.0, debugger.pid.18446744073709551616",
		"debugger.pid.1, debugger.pid.1",
		"debugger.pid.1, debugger.pid.2",
		"debugger.name.one, debugger.pid.1",
		"debugger.name.one, debugger.pid.invalid",
		"debugger.name.one, debugger.core.0",
		"debugger.name.one, debugger.name.one",
		"debugger.name.one, debugger.name.two",
		"debugger.pid.1, debugserver.name.server",
		"debugserver.name.server, debugger.pid.1",
		"debugger.name.one, debugserver.name.server",
		"debugserver.name.server, debugger.name.one",
		"debugger.pid.1, debugger.name.",
		"debugger.pid.1, , debugger.core.0",
		"debugger.pid.1,",
	}
	for _, descriptor := range descriptors {
		t.Run(descriptor, func(t *testing.T) {
			if _, err := ParseComponents("component.1 (" + descriptor + ")\n"); err == nil {
				t.Fatal("ParseComponents() accepted invalid or ambiguous aliases")
			}
		})
	}
}
