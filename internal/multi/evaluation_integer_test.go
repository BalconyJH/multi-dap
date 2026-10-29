package multi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/inspection"
)

func TestEvaluationBareIntegerIsDisplayOnly(t *testing.T) {
	for _, output := range []string{golden(t, "value_bare_integer.txt"), "2\n", "2\r\n", "-2", "0002", "18446744073709551616000000000000000000001"} {
		t.Run(output, func(t *testing.T) {
			driver, caller := newTestDriver(t, fakeCall{method: "run_commands", params: command("print 1 + 1"), result: commandText("raw", output)})
			value, err := driver.Evaluate(context.Background(), "1 + 1")
			want := strings.TrimSuffix(strings.TrimSuffix(output, "\n"), "\r")
			if err != nil || value.Name != "1 + 1" || value.Display != want || value.HasChildren || value.Hidden || value.State != ValueAvailable {
				t.Fatalf("Evaluate() = (%#v, %v)", value, err)
			}
			if len(caller.calls) != 0 {
				t.Fatalf("unconsumed calls: %#v", caller.calls)
			}
			if _, err := ParseValue(output); err == nil {
				t.Fatal("bare integer broadened ParseValue")
			}
			if _, err := ParseLocals(output); err == nil {
				t.Fatal("bare integer broadened ParseLocals")
			}
		})
	}
}

func TestEvaluationBareIntegerRejectsOtherShapes(t *testing.T) {
	for _, output := range []string{"", " ", " 2", "2 ", "2\n3", "2\n\n", "\n2", "2\r", "2\x00", "+2", "0x2", "2.0", "2;", "error: 2", "2 errors", "Cannot evaluate expression", "int {", strings.Repeat("2", maxInspectionLineBytes+1)} {
		t.Run(output[:min(len(output), 40)], func(t *testing.T) {
			driver, _ := newTestDriver(t, fakeCall{method: "run_commands", params: command("print 1 + 1"), result: commandText("raw", output)})
			if _, err := driver.Evaluate(context.Background(), "1 + 1"); !errors.Is(err, ErrInspectionFormat) {
				t.Fatalf("Evaluate() error = %v, want inspection format error", err)
			}
		})
	}
	driver, _ := newTestDriver(t, fakeCall{method: "run_commands", params: command("print 1 + 1"), result: map[string]any{"status": 1, "raw": "2", "raw_lossy": true}})
	if _, err := driver.Evaluate(context.Background(), "1 + 1"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("lossy Evaluate() error = %v", err)
	}
}

func TestInspectionRunnerEvaluationBareInteger(t *testing.T) {
	expression, err := inspection.NewExpression("1 + 1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, output     string
		lossy, wantError bool
	}{
		{"observed integer", golden(t, "value_bare_integer.txt"), false, false},
		{"large integer", "18446744073709551616000000000000000000001", false, false},
		{"diagnostic", "Cannot evaluate expression", false, true},
		{"multiline", "2\n3", false, true},
		{"lossy", "2", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewInspectionRunner(fixtureInspectionTopology())
			driver, caller := newTestDriver(t,
				fakeCall{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
				fakeCall{method: "run_commands", params: command("route fixture.component.4 print 1 + 1"), result: map[string]any{"status": 1, "raw": tt.output, "raw_lossy": tt.lossy}},
			)
			value, err := runner.evaluate(context.Background(), driver, inspection.CoreID(4), expression)
			if (err != nil) != tt.wantError {
				t.Fatalf("evaluate() = (%#v, %v)", value, err)
			}
			if !tt.wantError && (value.Name != "1 + 1" || value.Value != tt.output || value.Type != "" || value.HasChildren || value.NamedChildren != 0 || value.IndexedChildren != 0 || value.Access.Kind != inspection.AccessRoot || value.Availability != inspection.AvailabilityAvailable) {
				t.Fatalf("inferred non-display metadata: %#v", value)
			}
			if len(caller.calls) != 0 {
				t.Fatalf("unconsumed calls: %#v", caller.calls)
			}
		})
	}
}

func TestInspectionRunnerEvaluationKeepsNamedAndAggregateResults(t *testing.T) {
	expression, err := inspection.NewExpression("fixture_value")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		fixture, name, display string
		children               bool
	}{
		{"value.txt", "fixture_value", "42", false},
		{"aggregate.txt", "fixture_value", "volatile struct FixtureAggregate_t", true},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			runner := NewInspectionRunner(fixtureInspectionTopology())
			driver, _ := newTestDriver(t,
				fakeCall{method: "run_commands", params: command("route fixture.component.4 P"), result: commandText("raw", processTable(9, 0x44, StatusStopped, `X:\fixture\core4.elf`))},
				fakeCall{method: "run_commands", params: command("route fixture.component.4 print fixture_value"), result: commandText("raw", golden(t, tt.fixture))},
			)
			value, err := runner.evaluate(context.Background(), driver, inspection.CoreID(4), expression)
			if err != nil || value.Name != tt.name || value.Value != tt.display || value.HasChildren != tt.children {
				t.Fatalf("evaluate() = (%#v, %v)", value, err)
			}
		})
	}
}
