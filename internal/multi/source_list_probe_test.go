package multi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

func TestParseSourceFileListingIsExactAndFailClosed(t *testing.T) {
	valid := sourceListHeader + "\n    0: C:\\fixture\\src\\unit.c\n    1: D:\\fixture\\src\\other.hpp\n"
	files, err := parseSourceFileListing(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := files[source.Canonicalize(`c:\fixture\src\unit.c`)]; !found {
		t.Fatal("canonical source membership was not exact")
	}
	var allWidths strings.Builder
	allWidths.WriteString(sourceListHeader + "\n")
	for index := 0; index <= 100; index++ {
		fmt.Fprintf(&allWidths, "%5d: C:\\fixture\\src\\unit%d.c\n", index, index)
	}
	if files, err := parseSourceFileListing(allWidths.String()); err != nil || len(files) != 101 {
		t.Fatalf("1/2/3 digit rows = (%d files, %v)", len(files), err)
	}
	for name, raw := range map[string]string{
		"singular header":   "--------  File name  --------\n0: C:\\fixture\\src\\unit.c\n",
		"index gap":         sourceListHeader + "\n    1: C:\\fixture\\src\\unit.c\n",
		"duplicate index":   sourceListHeader + "\n    0: C:\\fixture\\src\\unit.c\n    0: D:\\fixture\\src\\other.c\n",
		"unindented":        sourceListHeader + "\n0: C:\\fixture\\src\\unit.c\n",
		"wrong field width": sourceListHeader + "\n   0: C:\\fixture\\src\\unit.c\n",
		"relative":          sourceListHeader + "\n    0: relative\\unit.c\n",
		"UNC":               sourceListHeader + "\n    0: \\server\\share\\unit.c\n",
		"forward slash":     sourceListHeader + "\n    0: C:/fixture/unit.c\n",
		"mixed separator":   sourceListHeader + "\n    0: C:\\fixture/src\\unit.c\n",
		"embedded relative": sourceListHeader + "\n    0: C:\\fixture\\..\\unit.c\n",
	} {
		if _, err := parseSourceFileListing(raw); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := parseSourceFileListing(strings.Repeat("x", maxSourceListBytes+1)); err == nil {
		t.Fatal("oversize listing accepted")
	}
	for _, raw := range []string{
		sourceListHeader + "\n    0: C:\\fixture\\src\\zero.c\n   10: C:\\fixture\\src\\ten.c\n",
		sourceListHeader + "\n    0: C:\\fixture\\src\\zero.c\n  100: C:\\fixture\\src\\hundred.c\n",
	} {
		if _, err := parseSourceFileListing(raw); !errors.Is(err, ErrSourceListFormat) {
			t.Fatalf("width mismatch error = %v, want ErrSourceListFormat", err)
		}
	}
	caseFoldDuplicate := sourceListHeader + "\n    0: C:\\fixture\\src\\unit.c\n    1: c:\\fixture\\src\\UNIT.c\n"
	files, err = parseSourceFileListing(caseFoldDuplicate)
	if err != nil || len(files) != 1 {
		t.Fatalf("case-fold duplicate = (%d files, %v)", len(files), err)
	}
}

func TestSourceListProbeTreatsLossyAndMalformedRepliesAsUnknown(t *testing.T) {
	topology := &Topology{cores: []TopologyCore{{ID: 0, ELF: `X:\fixture\a.elf`}}, routes: map[int]topologyRoute{0: {component: "fixture.component.1", elf: `x:\fixture\a.elf`}}}
	for name, reply := range map[string]map[string]any{
		"lossy":     {"status": 1, "raw": sourceListHeader + "\n    0: C:\\fixture\\unit.c\n", "raw_lossy": true},
		"malformed": commandText("raw", "unrecognized"),
	} {
		t.Run(name, func(t *testing.T) {
			driver, caller := newTestDriver(t,
				fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: commandText("raw", processTable(0, 1, StatusStopped, `X:\fixture\a.elf`))},
				fakeCall{method: "run_commands", params: command("route fixture.component.1 l f"), result: reply},
			)
			got, err := NewSourceListProbe().ProbeSource(context.Background(), driver, topology, 0, source.Identity{Key: "c:/fixture/unit.c", DebugPath: `C:\fixture\unit.c`})
			want := ErrSourceListFormat
			if name == "lossy" {
				want = ErrSourceListLossy
			}
			if !errors.Is(err, want) || got != source.PresenceUnknown || len(caller.calls) != 0 {
				t.Fatalf("ProbeSource() = (%v, %v), remaining=%#v", got, err, caller.calls)
			}
			if strings.Contains(err.Error(), "unrecognized") || strings.Contains(err.Error(), "fixture") {
				t.Fatalf("ProbeSource() exposed target text: %v", err)
			}
		})
	}
}

func TestSourceListProbeRejectsMismatchedTargetIdentityBeforeIO(t *testing.T) {
	topology := &Topology{cores: []TopologyCore{{ID: 0, ELF: `X:\fixture\a.elf`}}, routes: map[int]topologyRoute{0: {component: "fixture.component.1", elf: `x:\fixture\a.elf`}}}
	driver, caller := newTestDriver(t)
	got, err := NewSourceListProbe().ProbeSource(context.Background(), driver, topology, 0, source.Identity{Key: "c:/fixture/a.c", DebugPath: `C:\fixture\b.c`})
	if err == nil || got != source.PresenceUnknown || len(caller.calls) != 0 {
		t.Fatalf("ProbeSource mismatch = (%v, %v), remaining=%#v", got, err, caller.calls)
	}
}

func TestSourceResolverRefreshesEachResolveWithOneListingPerCore(t *testing.T) {
	topology := &Topology{cores: []TopologyCore{{ID: 0, ELF: `X:\fixture\a.elf`}}, routes: map[int]topologyRoute{0: {component: "fixture.component.1", elf: `x:\fixture\a.elf`}}}
	first := sourceListHeader + "\n    0: C:\\fixture\\unit.c\n"
	second := sourceListHeader + "\n    0: C:\\fixture\\other.c\n"
	driver, caller := newTestDriver(t,
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: commandText("raw", processTable(0, 1, StatusStopped, `X:\fixture\a.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 l f"), result: commandText("raw", first)},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 P"), result: commandText("raw", processTable(0, 1, StatusStopped, `X:\fixture\a.elf`))},
		fakeCall{method: "run_commands", params: command("route fixture.component.1 l f"), result: commandText("raw", second)},
	)
	resolver, err := NewSourceResolver(topology, NewSourceListProbe())
	if err != nil {
		t.Fatal(err)
	}
	identity := source.Identity{Key: "c:/fixture/unit.c", DebugPath: `C:\fixture\unit.c`}
	for _, want := range []source.Presence{source.PresencePresent, source.PresenceAbsent} {
		got, err := resolver.resolve(context.Background(), driver, identity)
		if err != nil || len(got) != 1 || got[0].Presence != want {
			t.Fatalf("resolve() = (%#v, %v), want %v", got, err, want)
		}
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}
