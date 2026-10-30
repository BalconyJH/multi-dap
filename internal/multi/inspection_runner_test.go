package multi

import (
	"context"
	"errors"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
)

func TestRoutedInspectionCommandUsesConfiguredTopology(t *testing.T) {
	topology := &Topology{cores: []TopologyCore{{ID: 4, ELF: `X:\fixture\core4.elf`}}, routes: map[int]topologyRoute{4: {component: "fixture.component.4", elf: `x:\fixture\core4.elf`}}}
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.4 " + normalizedCallsCommand), result: commandText("raw", "0_ fixture_entry\t[X:/fixture/unit.c:7,1]\n")},
	)
	result, err := routedInspectionCommand(context.Background(), topology, driver, inspection.CoreID(4), normalizedCallsCommand)
	if err != nil || result.Raw == "" {
		t.Fatalf("routedInspectionCommand() = (%#v, %v)", result, err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestRoutedInspectionCommandFailsClosed(t *testing.T) {
	if _, err := routedInspectionCommand(context.Background(), nil, nil, inspection.CoreID(-1), "calls"); err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
}

func TestInspectionRunnerTopologyBindingIsOneShot(t *testing.T) {
	runner := NewInspectionRunner()
	if err := runner.BindMULTITopology(fixtureInspectionTopology()); err != nil {
		t.Fatal(err)
	}
	if err := runner.BindMULTITopology(fixtureInspectionTopology()); err == nil {
		t.Fatal("second topology bind succeeded")
	}
}

func TestInspectionRunnerStackAddsPCOnlyToSelectedTopFrame(t *testing.T) {
	runner := NewInspectionRunner(fixtureInspectionTopology())
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.4 " + normalizedCallsCommand), result: commandText("raw", "0_ fixture_entry\t[X:/fixture/unit.c:7,1]\n1  caller\t[X:/fixture/unit.c:3,1]\n")},
		fakeCall{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.4 " + programCounterCommand), result: commandText("raw", "$pc = 0x00000000\n")},
	)
	frames, err := runner.stack(context.Background(), driver, inspection.CoreID(4))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || !frames[0].HasInstructionAddress || frames[0].InstructionAddress != 0 || frames[1].HasInstructionAddress {
		t.Fatalf("frames = %#v", frames)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestInspectionRunnerStackOmitsPCWithoutSelectedTopFrame(t *testing.T) {
	for _, output := range []string{
		"0  fixture_entry()\n1_ caller()\n",
		"0  fixture_entry()\n1  caller()\n",
	} {
		runner := NewInspectionRunner(fixtureInspectionTopology())
		driver, caller := newTestDriver(t, selectedFrameCalls(output)...)
		frames, err := runner.stack(context.Background(), driver, inspection.CoreID(4))
		if err != nil || len(frames) != 2 {
			t.Fatalf("stack() = (%#v, %v)", frames, err)
		}
		for _, frame := range frames {
			if frame.HasInstructionAddress {
				t.Fatalf("unverified frame context has instruction address: %#v", frame)
			}
		}
		if len(caller.calls) != 0 {
			t.Fatalf("unconsumed calls: %#v", caller.calls)
		}
	}
}

func TestInspectionRunnerTopFrameCommandsRejectUnprovenContext(t *testing.T) {
	for _, command := range []string{"l", "print fixture_value", "print (fixture_value).child"} {
		for _, test := range []struct {
			name, stack string
			want        error
		}{
			{"caller selected", "0  main()\n1_ caller()\n", ErrUnsupported},
			{"no selection", "0  main()\n1  caller()\n", ErrUnsupported},
			{"multiple selections", "0_ main()\n1_ caller()\n", ErrInspectionFormat},
			{"duplicate index", "0_ main()\n0  caller()\n", ErrInspectionFormat},
		} {
			t.Run(command+"/"+test.name, func(t *testing.T) {
				runner := NewInspectionRunner(fixtureInspectionTopology())
				driver, caller := newTestDriver(t, selectedFrameCalls(test.stack)...)
				if _, err := runner.topFrameCommand(context.Background(), driver, inspection.CoreID(4), command); !errors.Is(err, test.want) {
					t.Fatalf("topFrameCommand() error = %v, want %v", err, test.want)
				}
				if len(caller.calls) != 0 {
					t.Fatalf("unconsumed context checks: %#v", caller.calls)
				}
			})
		}
	}
}

func TestInspectionRunnerTopFrameContextIsRecheckedForEachRead(t *testing.T) {
	runner := NewInspectionRunner(fixtureInspectionTopology())
	calls := selectedFrameCalls("0_ main()\n1  caller()\n")
	calls = append(calls,
		fakeCall{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.4 l"), result: commandText("raw", "fixture_value = 42\n")},
	)
	calls = append(calls, selectedFrameCalls("0  main()\n1_ caller()\n")...)
	driver, caller := newTestDriver(t, calls...)
	if result, err := runner.topFrameCommand(context.Background(), driver, inspection.CoreID(4), "l"); err != nil || result.Raw != "fixture_value = 42\n" {
		t.Fatalf("first read = (%#v, %v)", result, err)
	}
	if _, err := runner.topFrameCommand(context.Background(), driver, inspection.CoreID(4), "l"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("second read error = %v, want unsupported frame context", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func selectedFrameCalls(stack string) []fakeCall {
	return []fakeCall{
		{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
		{method: "run_commands", params: command("route fixture.component.4 " + normalizedCallsCommand), result: commandText("raw", stack)},
	}
}

func TestInspectionRunnerStackRejectsLossyPC(t *testing.T) {
	runner := NewInspectionRunner(fixtureInspectionTopology())
	driver, _ := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.4 " + normalizedCallsCommand), result: commandText("raw", "0_ fixture_entry\t[X:/fixture/unit.c:7,1]\n")},
		fakeCall{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.4 " + programCounterCommand), result: map[string]any{"status": 1, "raw": "$pc = 0x00000000\n", "raw_lossy": true}},
	)
	if _, err := runner.stack(context.Background(), driver, inspection.CoreID(4)); err == nil || !errors.Is(err, ErrProtocol) {
		t.Fatalf("Stack() error = %v, want lossy protocol failure", err)
	}
}

func fixtureInspectionTopology() *Topology {
	return &Topology{cores: []TopologyCore{{ID: 4, ELF: `X:\fixture\core4.elf`}}, routes: map[int]topologyRoute{4: {component: "fixture.component.4", elf: `x:\fixture\core4.elf`}}}
}
