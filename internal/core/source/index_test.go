package source

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildIndexMapsFilesToCores(t *testing.T) {
	idx, err := BuildIndex(map[int]string{0: "testdata/fixture.elf"})
	if err != nil {
		t.Fatalf("BuildIndex() error = %v", err)
	}
	var found bool
	for _, key := range idx.Keys() {
		if strings.HasSuffix(key, "fixture.go") {
			found = true
			if got := idx.CoresFor(key); len(got) != 1 || got[0] != 0 {
				t.Fatalf("CoresFor(%q) = %v, want [0]", key, got)
			}
		}
	}
	if !found {
		t.Fatal("index did not contain the fixture source file")
	}
}

func TestCoverageRetainsPositiveEvidenceAndMarksIncompleteCoresUnknown(t *testing.T) {
	idx, err := BuildIndex(map[int]string{
		0: "testdata/fixture.elf",
		1: "testdata/does-not-exist.elf",
	})
	if err != nil {
		t.Fatalf("BuildIndex() error = %v", err)
	}
	var key string
	for _, candidate := range idx.Keys() {
		if strings.HasSuffix(candidate, "fixture.go") {
			key = candidate
			break
		}
	}
	if key == "" {
		t.Fatal("index did not contain fixture.go")
	}
	want := []Observation{
		{Core: 0, Presence: PresencePresent},
		{Core: 1, Presence: PresenceUnknown},
	}
	if got := idx.Coverage(key); !reflect.DeepEqual(got, want) {
		t.Fatalf("Coverage(%q) = %#v, want %#v", key, got, want)
	}
	if got := idx.Coverage("src/not-in-any-line-table.c"); !reflect.DeepEqual(got, []Observation{
		{Core: 0, Presence: PresenceUnknown},
		{Core: 1, Presence: PresenceUnknown},
	}) {
		t.Fatalf("Coverage(missing) = %#v, errors = %v", got, idx.Errors)
	}
	if len(idx.Errors) == 0 {
		t.Fatal("BuildIndex did not retain the unavailable-DWARF error")
	}
}

func TestCoverageMarksNonHitsAbsentOnlyAfterCompleteScan(t *testing.T) {
	idx := &Index{
		files:      map[string]map[int]struct{}{"src/known.c": {0: {}}},
		configured: map[int]struct{}{0: {}, 1: {}},
		complete:   map[int]bool{0: true, 1: false},
	}
	if got, want := idx.Coverage("src/known.c"), []Observation{
		{Core: 0, Presence: PresencePresent},
		{Core: 1, Presence: PresenceUnknown},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Coverage(known) = %#v, want %#v", got, want)
	}
	if got, want := idx.Coverage("src/missing.c"), []Observation{
		{Core: 0, Presence: PresenceAbsent},
		{Core: 1, Presence: PresenceUnknown},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Coverage(missing) = %#v, want %#v", got, want)
	}
}

func TestRecordKnownSupportsTheNonDWARFPrimaryPath(t *testing.T) {
	idx := NewIndex()
	identity := Identity{ClientPath: `C:\work\main.c`, DebugPath: `C:/build/main.c`, Key: Canonicalize(`C:\work\main.c`)}
	idx.RecordKnown(4, identity)
	idx.RecordKnown(0, identity)
	idx.RecordKnown(4, identity)
	if got := idx.CoresFor(identity.Key); !reflect.DeepEqual(got, []int{0, 4}) {
		t.Fatalf("CoresFor() = %v, want [0 4]", got)
	}
}
