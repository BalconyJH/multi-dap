package multi

import (
	"context"
	"errors"
	"testing"
)

func TestDriverVerifiedTextOperations(t *testing.T) {
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: map[string]string{"commands": normalizedCallsCommand}, result: map[string]any{"status": 1, "raw": golden(t, "stack.txt")}},
		fakeCall{method: "run_commands", params: map[string]string{"commands": "l"}, result: map[string]any{"status": 1, "raw": golden(t, "locals.txt")}},
		fakeCall{method: "run_commands", params: map[string]string{"commands": "print fixture_value"}, result: map[string]any{"status": 1, "raw": golden(t, "value.txt")}},
		fakeCall{method: "run_commands", params: map[string]string{"commands": "H"}, result: map[string]any{"status": 1, "raw": golden(t, "halt_breakpoint.txt")}},
		fakeCall{method: "run_commands", params: map[string]string{"commands": "P"}, result: map[string]any{"status": 1, "raw": golden(t, "processes.txt")}},
		fakeCall{method: "run_commands", params: map[string]string{"commands": "B"}, result: map[string]any{"status": 1, "raw": golden(t, "breakpoints.txt")}},
	)

	if frames, err := driver.Stack(context.Background()); err != nil || len(frames) != 2 {
		t.Fatalf("Stack() = %#v, %v", frames, err)
	}
	if locals, err := driver.CurrentLocals(context.Background()); err != nil || len(locals) != 4 {
		t.Fatalf("CurrentLocals() = %#v, %v", locals, err)
	}
	if value, err := driver.Evaluate(context.Background(), "fixture_value"); err != nil || value.Display != "42" {
		t.Fatalf("Evaluate() = %#v, %v", value, err)
	}
	if info, err := driver.HaltInfo(context.Background()); err != nil || info.Reason != HaltReasonBreakpoint {
		t.Fatalf("HaltInfo() = %#v, %v", info, err)
	}
	if processes, err := driver.Processes(context.Background()); err != nil || len(processes) != 3 {
		t.Fatalf("Processes() = %#v, %v", processes, err)
	}
	if breakpoints, err := driver.Breakpoints(context.Background()); err != nil || len(breakpoints) != 3 {
		t.Fatalf("Breakpoints() = %#v, %v", breakpoints, err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestDriverStackUsesNormalizedCallsForUnsourcedFrame(t *testing.T) {
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: map[string]string{"commands": normalizedCallsCommand}, result: map[string]any{"status": 1, "raw": "0_ fixture_reset()\n"}},
	)
	frames, err := driver.Stack(context.Background())
	if err != nil || len(frames) != 1 {
		t.Fatalf("Stack() = %#v, %v", frames, err)
	}
	if got := frames[0]; got.Function != "fixture_reset()" || got.Path != "" || got.Line != 0 || got.Column != 0 {
		t.Fatalf("unsourced Stack() frame = %#v", got)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestDriverRejectsLossyOrMalformedText(t *testing.T) {
	tests := []struct {
		name string
		call fakeCall
		run  func(*Driver) error
	}{
		{
			name: "lossy stack", call: fakeCall{method: "run_commands", params: map[string]string{"commands": normalizedCallsCommand}, result: map[string]any{"status": 1, "raw": golden(t, "stack.txt"), "raw_lossy": true}},
			run: func(d *Driver) error { _, err := d.Stack(context.Background()); return err },
		},
		{
			name: "malformed locals", call: fakeCall{method: "run_commands", params: map[string]string{"commands": "l"}, result: map[string]any{"status": 1, "raw": "broken"}},
			run: func(d *Driver) error { _, err := d.CurrentLocals(context.Background()); return err },
		},
		{
			name: "unexpected halt line", call: fakeCall{method: "run_commands", params: map[string]string{"commands": "H"}, result: map[string]any{"status": 1, "raw": "Halted by user request.\nextra"}},
			run: func(d *Driver) error { _, err := d.HaltInfo(context.Background()); return err },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver, _ := newTestDriver(t, test.call)
			want := ErrProtocol
			if test.name == "malformed locals" {
				want = ErrInspectionFormat
			}
			if err := test.run(driver); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func TestEvaluateRejectsCommandSeparators(t *testing.T) {
	for _, expression := range []string{"", " \t", "fixture_value; H", "fixture_value\nH", "fixture_value\rH", "fixture_value\x00H", "fixture_value\vH", "fixture_value\u0085H"} {
		driver, caller := newTestDriver(t)
		if _, err := driver.Evaluate(context.Background(), expression); err == nil {
			t.Fatalf("Evaluate(%q) succeeded", expression)
		}
		if len(caller.calls) != 0 {
			t.Fatalf("Evaluate(%q) made a bridge call", expression)
		}
	}
}

func TestUnsupportedCapabilitiesFailClosed(t *testing.T) {
	driver, caller := newTestDriver(t,
		fakeCall{method: "step_in", params: map[string]string{}, result: map[string]any{"accepted": true}},
		fakeCall{method: "next", params: map[string]string{}, result: map[string]any{"accepted": true}},
	)
	if !driver.Supports(CapabilityStep) || !driver.Supports(CapabilityNext) {
		t.Fatal("verified M4 capabilities are not advertised")
	}
	if err := driver.StepIn(context.Background()); err != nil {
		t.Fatalf("StepIn() = %v", err)
	}
	if err := driver.Next(context.Background()); err != nil {
		t.Fatalf("Next() = %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}

	driver, caller = newTestDriver(t)
	operations := []struct {
		capability Capability
		run        func() error
	}{
		{CapabilityFinish, func() error { return driver.Finish(context.Background()) }},
		{CapabilityRunTo, func() error { return driver.RunTo(context.Background(), 0x00c0ffee) }},
		{CapabilityReadMemory, func() error { _, err := driver.ReadMemory(context.Background(), 0x00c0ffee, 4); return err }},
		{CapabilityWriteMemory, func() error { return driver.WriteMemory(context.Background(), 0x00c0ffee, []byte{1}) }},
		{CapabilityRegisters, func() error { _, err := driver.Registers(context.Background()); return err }},
		{CapabilityDisassembly, func() error { _, err := driver.Disassemble(context.Background(), 0x00c0ffee, 4); return err }},
	}
	for _, operation := range operations {
		if driver.Supports(operation.capability) {
			t.Fatalf("Supports(%s) = true", operation.capability)
		}
		if err := operation.run(); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s error = %v, want ErrUnsupported", operation.capability, err)
		}
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unsupported capability reached bridge: %#v", caller.calls)
	}
}
