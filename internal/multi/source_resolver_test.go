package multi

import (
	"context"
	"reflect"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

func TestSourceResolverUsesConfiguredTopologyWithoutCrossRequestCache(t *testing.T) {
	topology := &Topology{cores: []TopologyCore{{ID: 0, ELF: `X:\fixture\a.elf`}, {ID: 4, ELF: `X:\fixture\b.elf`}}, routes: map[int]topologyRoute{
		0: {component: "fixture.component.1", elf: `x:\fixture\a.elf`}, 4: {component: "fixture.component.2", elf: `x:\fixture\b.elf`},
	}}
	driver, caller := newTestDriver(t)
	calls := []int{}
	resolver, err := NewSourceResolver(topology, SourceProbeFunc(func(_ context.Context, got *Driver, gotTopology *Topology, core int, _ source.Identity) (source.Presence, error) {
		if got != driver {
			t.Fatal("probe driver mismatch")
		}
		if gotTopology != topology {
			t.Fatal("probe topology mismatch")
		}
		if core == 0 {
			calls = append(calls, 0)
			return source.PresencePresent, nil
		}
		if core == 4 {
			calls = append(calls, 4)
			return source.PresenceAbsent, nil
		}
		t.Fatalf("unexpected core %d", core)
		return source.PresenceUnknown, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	identity := source.Identity{Key: "x:/fixture/src/unit.c", DebugPath: "X:/fixture/src/unit.c"}
	got, err := resolver.resolve(context.Background(), driver, identity)
	want := []source.Observation{{Core: 0, Presence: source.PresencePresent}, {Core: 4, Presence: source.PresenceAbsent}}
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(calls, []int{0, 4}) {
		t.Fatalf("resolve() = (%#v, %v), calls=%v", got, err, calls)
	}
	got, err = resolver.resolve(context.Background(), driver, identity)
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(calls, []int{0, 4, 0, 4}) {
		t.Fatalf("second resolve() = (%#v, %v), calls=%v", got, err, calls)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unconsumed calls: %#v", caller.calls)
	}
}

func TestSourceResolverRejectsMismatchedTargetIdentityBeforeProbe(t *testing.T) {
	topology := &Topology{cores: []TopologyCore{{ID: 0, ELF: `X:\fixture\a.elf`}}, routes: map[int]topologyRoute{0: {component: "fixture.component.1", elf: `x:\fixture\a.elf`}}}
	called := false
	resolver, err := NewSourceResolver(topology, SourceProbeFunc(func(context.Context, *Driver, *Topology, int, source.Identity) (source.Presence, error) {
		called = true
		return source.PresencePresent, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	driver, caller := newTestDriver(t)
	_, err = resolver.resolve(context.Background(), driver, source.Identity{Key: "x:/fixture/a.c", DebugPath: `X:\fixture\b.c`})
	if err == nil || called || len(caller.calls) != 0 {
		t.Fatalf("resolve mismatch = (%v, probe=%v, remaining=%#v)", err, called, caller.calls)
	}
}

func TestDeferredSourceResolverBindingIsExactAndOneShot(t *testing.T) {
	resolver, err := NewDeferredSourceResolver([]CoreSpec{{ID: 4, ELF: `X:\fixture\core4.elf`}}, SourceProbeFunc(func(context.Context, *Driver, *Topology, int, source.Identity) (source.Presence, error) {
		return source.PresenceUnknown, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	matching := &Topology{cores: []TopologyCore{{ID: 4, ELF: `X:\fixture\core4.elf`}}, routes: map[int]topologyRoute{4: {component: "fixture.component.4", elf: `x:\fixture\core4.elf`}}}
	if err := resolver.BindMULTITopology(matching); err != nil {
		t.Fatal(err)
	}
	if err := resolver.BindMULTITopology(matching); err != nil {
		t.Fatalf("idempotent same topology bind: %v", err)
	}
	mismatched := &Topology{cores: []TopologyCore{{ID: 5, ELF: `X:\fixture\core4.elf`}}, routes: map[int]topologyRoute{5: {component: "fixture.component.5", elf: `x:\fixture\core4.elf`}}}
	if err := resolver.BindMULTITopology(mismatched); err == nil {
		t.Fatal("second different topology bind succeeded")
	}
}
