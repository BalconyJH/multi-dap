package source

import "testing"

func TestCanonicalizeUnifiesSeparators(t *testing.T) {
	a := Canonicalize(`C:\proj\src\main.c`)
	b := Canonicalize("C:/proj/src/main.c")
	if a != b {
		t.Fatalf("Canonicalize disagreed: %q vs %q", a, b)
	}
}

func TestCanonicalizeIsCaseInsensitiveOnWindowsPaths(t *testing.T) {
	a := Canonicalize("C:/Proj/Src/Main.c")
	b := Canonicalize("c:/proj/src/main.c")
	if a != b {
		t.Fatalf("Canonicalize disagreed on case: %q vs %q", a, b)
	}
}

func TestCanonicalizeResolvesDotSegments(t *testing.T) {
	a := Canonicalize("C:/proj/src/../src/main.c")
	b := Canonicalize("C:/proj/src/main.c")
	if a != b {
		t.Fatalf("Canonicalize disagreed on dot segments: %q vs %q", a, b)
	}
}

func TestIdentityKeyDrivesComparison(t *testing.T) {
	x := Identity{ClientPath: `C:\proj\src\main.c`, DebugPath: "/build/proj/src/main.c"}
	x.Key = Canonicalize(x.ClientPath)
	y := Identity{ClientPath: "C:/proj/src/main.c"}
	y.Key = Canonicalize(y.ClientPath)
	if x.Key != y.Key {
		t.Fatalf("keys differ: %q vs %q", x.Key, y.Key)
	}
}
