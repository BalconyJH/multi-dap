package source

import (
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
