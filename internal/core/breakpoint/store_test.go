package breakpoint

import (
	"fmt"
	"testing"

	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

// srcFor builds a source.Identity for a bare test file name, canonicalizing it
// the same way the real frontend boundary would (architecture.md 6.6).
func srcFor(path string) source.Identity {
	return source.Identity{
		ClientPath: path,
		DebugPath:  path,
		Key:        source.Canonicalize(path),
	}
}

// okResults builds single-core, fully-successful PhysicalResults, one per
// requested line, for tests that only care about the happy path.
func okResults(lines ...int) []PhysicalResult {
	out := make([]PhysicalResult, 0, len(lines))
	for i, line := range lines {
		out = append(out, PhysicalResult{
			Core:          0,
			RequestedLine: line,
			ActualLine:    line,
			MULTIHandle:   fmt.Sprintf("h%d", i),
			OK:            true,
		})
	}
	return out
}

func TestDiffIsReplacementNotIncremental(t *testing.T) {
	s := NewStore()
	src := srcFor("foo.c")
	s.Commit(s.Diff(src, []int{12, 20}), okResults(12, 20))

	plan := s.Diff(src, []int{12, 30})
	if len(plan.Add) != 1 || plan.Add[0] != 30 {
		t.Fatalf("plan.Add = %v, want [30]", plan.Add)
	}
	if len(plan.Remove) != 1 || plan.Remove[0].RequestedLine != 20 {
		t.Fatalf("plan.Remove = %v, want the line-20 breakpoint", plan.Remove)
	}
	if len(plan.Keep) != 1 || plan.Keep[0] != 12 {
		t.Fatalf("plan.Keep = %v, want [12]", plan.Keep)
	}
}

func TestPartialFailureRollsBackTheWholeLogicalBreakpoint(t *testing.T) {
	s := NewStore()
	src := srcFor("foo.c")
	plan := s.Diff(src, []int{12})
	results := []PhysicalResult{
		{Core: 0, RequestedLine: 12, ActualLine: 12, MULTIHandle: "h1", OK: true},
		{Core: 4, RequestedLine: 12, OK: false, Err: "no such line"},
	}
	logicals, err := s.Commit(plan, results)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if logicals[0].Verified {
		t.Fatal("Verified = true, want false when one core failed")
	}
	if logicals[0].Message == "" {
		t.Fatal("Message is empty; a failed breakpoint must explain itself")
	}
	if got := len(logicals[0].RollbackNeeded()); got != 1 {
		t.Fatalf("RollbackNeeded() = %d physical breakpoints, want 1", got)
	}
}

func TestDivergentActualLinesAreNotVerified(t *testing.T) {
	s := NewStore()
	src := srcFor("foo.c")
	plan := s.Diff(src, []int{12})
	results := []PhysicalResult{
		{Core: 0, RequestedLine: 12, ActualLine: 12, MULTIHandle: "h1", OK: true},
		{Core: 4, RequestedLine: 12, ActualLine: 14, MULTIHandle: "h2", OK: true},
	}
	logicals, _ := s.Commit(plan, results)
	if logicals[0].Verified {
		t.Fatal("Verified = true, want false when cores resolved to different lines")
	}
}

func TestOwnershipSurvivesAFailedCommit(t *testing.T) {
	s := NewStore()
	src := srcFor("foo.c")
	plan := s.Diff(src, []int{12})
	results := []PhysicalResult{
		{Core: 0, RequestedLine: 12, ActualLine: 12, MULTIHandle: "h1", OK: true},
		{Core: 4, RequestedLine: 12, OK: false, Err: "refused"},
	}
	s.Commit(plan, results)
	if s.DAPOwnedCount() == 0 {
		t.Fatal("DAPOwnedCount() = 0; a failed commit must not make multi-dap forget what it created")
	}
}

func TestHintTokensAreUniqueWithinASession(t *testing.T) {
	s := NewStore()
	seen := map[uint32]bool{}
	for i := 0; i < 1000; i++ {
		tok := s.NewHintToken()
		if seen[tok] {
			t.Fatalf("hint token %#x was issued twice", tok)
		}
		seen[tok] = true
	}
}
