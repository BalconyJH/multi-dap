package source

import "testing"

func TestCacheRetainsOnlyDefinitivePerCoreObservations(t *testing.T) {
	cache := NewCache()
	identity := Identity{Key: "src/main.c"}
	if err := cache.Record(identity, 0, PresencePresent); err != nil {
		t.Fatal(err)
	}
	if err := cache.Record(identity, 4, PresenceAbsent); err != nil {
		t.Fatal(err)
	}
	if got, ok := cache.Lookup(identity, 0); !ok || got != PresencePresent {
		t.Fatalf("Lookup(core 0) = (%v, %t), want (present, true)", got, ok)
	}
	if got, ok := cache.Lookup(identity, 4); !ok || got != PresenceAbsent {
		t.Fatalf("Lookup(core 4) = (%v, %t), want (absent, true)", got, ok)
	}
	if _, ok := cache.Lookup(Identity{Key: "src/other.c"}, 0); ok {
		t.Fatal("Lookup found an unrelated source")
	}
}

func TestCacheRejectsUncertainAndInvalidObservations(t *testing.T) {
	cache := NewCache()
	identity := Identity{Key: "src/main.c"}
	for _, test := range []struct {
		identity Identity
		core     int
		presence Presence
	}{
		{identity: identity, core: 0, presence: PresenceUnknown},
		{identity: identity, core: -1, presence: PresencePresent},
		{identity: Identity{}, core: 0, presence: PresencePresent},
		{identity: identity, core: 0, presence: Presence(99)},
	} {
		if err := cache.Record(test.identity, test.core, test.presence); err == nil {
			t.Fatalf("Record(%#v, %d, %v) succeeded", test.identity, test.core, test.presence)
		}
	}
}
