package multi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/breakpoint"
	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

func fixtureBreakpointTopology() *Topology {
	return &Topology{cores: []TopologyCore{{ID: 0, ELF: `X:\fixture\core0.elf`}}, routes: map[int]topologyRoute{0: {component: "fixture.component.1", elf: `x:\fixture\core0.elf`}}}
}

func fixtureSelectedProcess() map[string]any {
	return commandText("raw", processTable(0, 1, StatusStopped, `X:\fixture\core0.elf`))
}

func TestBreakpointPlacerUsesTopologyAndRevalidatesEveryCommand(t *testing.T) {
	const token = `mprintf("HIT 0x00C0FFEE\n")`
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 b X:/fixture/src/unit.c#137 {" + token + "}"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "7 X:/fixture/src/unit.c#137:\t0x00c0ffee AT count: 1 <{"+token+"}>\n")},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	physical, err := placer.Set(context.Background(), driver, breakpoint.PlacementRequest{Core: 0, Source: source.Identity{Key: "x:/fixture/src/unit.c", DebugPath: "X:/fixture/src/unit.c"}, Line: 137, HintToken: 0xc0ffee})
	if err != nil || physical.MULTIHandle != "7" || physical.ActualLine != 137 {
		t.Fatalf("Set() = (%#v, %v)", physical, err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestBreakpointPlacementRejectsMismatchedTargetIdentity(t *testing.T) {
	err := validatePlacement(breakpoint.PlacementRequest{
		Core:   0,
		Source: source.Identity{Key: "x:/fixture/a.c", DebugPath: "X:/fixture/b.c"},
		Line:   1, HintToken: 1,
	})
	if err == nil {
		t.Fatal("mismatched target identity accepted")
	}
}

func TestBreakpointPlacerFailsClosedWithoutTopologyOrCore(t *testing.T) {
	if _, err := NewBreakpointPlacer(nil); err == nil {
		t.Fatal("nil topology accepted")
	}
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	_, err = placer.Set(context.Background(), nil, breakpoint.PlacementRequest{Core: 1, Source: source.Identity{Key: "x:/fixture/src/unit.c", DebugPath: "X:/fixture/src/unit.c"}, Line: 1, HintToken: 1})
	if err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("Set() error = %v", err)
	}
}

func TestBreakpointPlacerReturnsRecoveryIntentAfterConfirmedSetAndUnreadableListing(t *testing.T) {
	const token = `mprintf("HIT 0x00C0FFEE\n")`
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 b X:/fixture/src/unit.c#137 {" + token + "}"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), err: errors.New("bridge disconnected")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", breakpointRow(7, "X:/fixture/src/unit.c#137", token))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 d %7"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	physical, err := placer.Set(context.Background(), driver, breakpoint.PlacementRequest{Core: 0, Source: source.Identity{Key: "x:/fixture/src/unit.c", DebugPath: "X:/fixture/src/unit.c"}, Line: 137, HintToken: 0xc0ffee})
	if err == nil || !physical.PossiblyCommitted || physical.MULTIHandle != "" || physical.HintToken != 0xc0ffee {
		t.Fatalf("Set() = (%#v, %v)", physical, err)
	}
	if err := placer.Clear(context.Background(), driver, physical); err != nil {
		t.Fatalf("Clear(recovery) = %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestBreakpointPlacerRecoversAfterConfirmedSetAndMalformedListing(t *testing.T) {
	const token = `mprintf("HIT 0x00000001\n")`
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 b X:/fixture/src/unit.c#137 {" + token + "}"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "malformed breakpoint output")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", breakpointRow(7, "X:/fixture/src/unit.c#137", token))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 d %7"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	physical, err := placer.Set(context.Background(), driver, breakpoint.PlacementRequest{Core: 0, Source: source.Identity{Key: "x:/fixture/src/unit.c", DebugPath: "X:/fixture/src/unit.c"}, Line: 137, HintToken: 1})
	if err == nil || !physical.PossiblyCommitted || physical.MULTIHandle != "" {
		t.Fatalf("Set() = (%#v, %v)", physical, err)
	}
	if err := placer.Clear(context.Background(), driver, physical); err != nil {
		t.Fatalf("Clear(recovery) = %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestBreakpointPlacerReturnsKnownHandleRecoveryIntentWhenActualLineCannotBeParsed(t *testing.T) {
	const token = `mprintf("HIT 0x00000001\n")`
	driver, _ := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 b X:/fixture/src/unit.c#137 {" + token + "}"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", breakpointRow(7, "X:/fixture/src/unit.c", token))},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	physical, err := placer.Set(context.Background(), driver, breakpoint.PlacementRequest{Core: 0, Source: source.Identity{Key: "x:/fixture/src/unit.c", DebugPath: "X:/fixture/src/unit.c"}, Line: 137, HintToken: 1})
	if err == nil || !physical.PossiblyCommitted || physical.MULTIHandle != "7" {
		t.Fatalf("Set() = (%#v, %v)", physical, err)
	}
}

func TestBreakpointPlacerRejectsHandleReuseWithDifferentToken(t *testing.T) {
	const other = `mprintf("HIT 0x00000002\n")`
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", breakpointRow(7, "X:/fixture/src/unit.c#137", other))},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	err = placer.Clear(context.Background(), driver, breakpoint.Physical{Core: 0, MULTIHandle: "7", HintToken: 1})
	if err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("Clear() error = %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("Clear issued a deletion after handle reuse: %#v", caller.calls)
	}
}

func TestBreakpointPlacerRejectsAmbiguousListingBeforeDeletion(t *testing.T) {
	const token = `mprintf("HIT 0x00000001\n")`
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", breakpointRow(7, "X:/fixture/src/unit.c#137", token)+breakpointRow(7, "X:/fixture/src/other.c#138", ""))},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	err = placer.Clear(context.Background(), driver, breakpoint.Physical{Core: 0, MULTIHandle: "7", HintToken: 1})
	if err == nil || !strings.Contains(err.Error(), "duplicate index") {
		t.Fatalf("Clear() error = %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("Clear issued a deletion after ambiguous listing: %#v", caller.calls)
	}
}

func TestBreakpointPlacerClearsEveryExactTokenForUnknownHandle(t *testing.T) {
	const token = `mprintf("HIT 0x00000001\n")`
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", breakpointRow(7, "X:/fixture/src/unit.c#137", token)+breakpointRow(8, "X:/fixture/src/unit.c#138", token))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 d %7"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", breakpointRow(8, "X:/fixture/src/unit.c#138", token))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 d %8"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	if err := placer.Clear(context.Background(), driver, breakpoint.Physical{Core: 0, HintToken: 1, PossiblyCommitted: true}); err != nil {
		t.Fatalf("Clear() = %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestBreakpointPlacerUnknownHandleAcceptsCompleteTokenAbsence(t *testing.T) {
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	if err := placer.Clear(context.Background(), driver, breakpoint.Physical{Core: 0, HintToken: 1, PossiblyCommitted: true}); err != nil {
		t.Fatalf("Clear() = %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unexpected deletion calls: %#v", caller.calls)
	}
}

func TestBreakpointPlacerDeletesZeroHandleWithBreakpointPrefix(t *testing.T) {
	const token = `mprintf("HIT 0x00000001\n")`
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", breakpointRow(0, "X:/fixture/src/unit.c#137", token))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 d %0"), result: commandText("raw", "")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	if err := placer.Clear(context.Background(), driver, breakpoint.Physical{Core: 0, MULTIHandle: "0", HintToken: 1}); err != nil {
		t.Fatalf("Clear() = %v", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestBreakpointPlacerSetRefusalDoesNotReturnRecoveryIntent(t *testing.T) {
	const token = `mprintf("HIT 0x00000001\n")`
	driver, _ := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 B"), result: commandText("raw", "No software breakpoints set.\n")},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: fixtureSelectedProcess()},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 b X:/fixture/src/unit.c#137 {" + token + "}"), result: map[string]any{"status": 0, "raw": "refused"}},
	)
	placer, err := NewBreakpointPlacer(fixtureBreakpointTopology())
	if err != nil {
		t.Fatal(err)
	}
	physical, err := placer.Set(context.Background(), driver, breakpoint.PlacementRequest{Core: 0, Source: source.Identity{Key: "x:/fixture/src/unit.c", DebugPath: "X:/fixture/src/unit.c"}, Line: 137, HintToken: 1})
	if err == nil || physical.PossiblyCommitted || physical.HintToken != 0 || physical.MULTIHandle != "" {
		t.Fatalf("Set() = (%#v, %v)", physical, err)
	}
}

func breakpointRow(index int, location, token string) string {
	return fmt.Sprintf("%d %s:\t0x00c0ffee AT count: 1 <{%s}>\n", index, location, token)
}
