package breakpoint

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type fakePlacer struct {
	set    func(PlacementRequest) (Physical, error)
	clear  func(Physical) error
	sets   []PlacementRequest
	clears []Physical
}

func (f *fakePlacer) Set(_ context.Context, request PlacementRequest) (Physical, error) {
	f.sets = append(f.sets, request)
	if f.set != nil {
		return f.set(request)
	}
	return Physical{ActualLine: request.Line, MULTIHandle: fmt.Sprintf("%d:%d", request.Core, request.Line)}, nil
}
func (f *fakePlacer) Clear(_ context.Context, physical Physical) error {
	f.clears = append(f.clears, physical)
	if f.clear != nil {
		return f.clear(physical)
	}
	return nil
}

func TestReplacePlacesOneLogicalOnEveryCore(t *testing.T) {
	s := NewStore()
	p := &fakePlacer{}
	result, err := s.Replace(context.Background(), p, srcFor("foo.c"), []int{12}, []int{4, 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Breakpoints) != 1 || !result.Breakpoints[0].Verified || len(result.Breakpoints[0].Physical) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if len(p.sets) != 2 || p.sets[0].Core != 0 || p.sets[1].Core != 4 || p.sets[0].HintToken == p.sets[1].HintToken {
		t.Fatalf("set requests = %#v", p.sets)
	}
	target, ok := s.LookupHint(p.sets[0].HintToken)
	if !ok || target.DAPID != result.Breakpoints[0].DAPID {
		t.Fatalf("live token = %#v, %v", target, ok)
	}
}

func TestReplaceRollsBackPartialPlacementAndRetainsClearFailure(t *testing.T) {
	s := NewStore()
	p := &fakePlacer{set: func(request PlacementRequest) (Physical, error) {
		if request.Core == 4 {
			return Physical{}, errors.New("refused")
		}
		return Physical{ActualLine: request.Line, MULTIHandle: "h"}, nil
	}, clear: func(Physical) error { return errors.New("target running") }}
	result, err := s.Replace(context.Background(), p, srcFor("foo.c"), []int{12}, []int{0, 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.Breakpoints[0].Verified || len(p.clears) != 1 || len(result.Orphans) != 1 || s.DAPOwnedCount() != 1 {
		t.Fatalf("result=%#v clears=%#v owned=%d", result, p.clears, s.DAPOwnedCount())
	}
	if target, ok := s.LookupHint(p.clears[0].HintToken); !ok || target.DAPID != 0 {
		t.Fatalf("orphan token target=%#v ok=%v", target, ok)
	}
}

func TestReplaceRecoversPossiblyCommittedSetErrorBeforeTryingAnotherCore(t *testing.T) {
	s := NewStore()
	p := &fakePlacer{set: func(request PlacementRequest) (Physical, error) {
		return Physical{PossiblyCommitted: true}, errors.New("after-B transport failure")
	}}
	result, err := s.Replace(context.Background(), p, srcFor("foo.c"), []int{12}, []int{0, 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.sets) != 1 || len(p.clears) != 1 {
		t.Fatalf("set=%#v clear=%#v; recovery must stop and rollback immediately", p.sets, p.clears)
	}
	cleared := p.clears[0]
	if !cleared.PossiblyCommitted || cleared.Core != 0 || cleared.RequestedLine != 12 || cleared.HintToken != p.sets[0].HintToken || cleared.MULTIHandle != "" {
		t.Fatalf("recovery physical = %#v", cleared)
	}
	if result.Breakpoints[0].Verified || s.DAPOwnedCount() != 0 || s.OrphanCount() != 0 {
		t.Fatalf("result=%#v owned=%d orphaned=%d", result, s.DAPOwnedCount(), s.OrphanCount())
	}
}

func TestPossiblyCommittedRecoveryFailureIsOrphanedThenCleared(t *testing.T) {
	s := NewStore()
	clearAttempts := 0
	p := &fakePlacer{
		set: func(PlacementRequest) (Physical, error) {
			return Physical{PossiblyCommitted: true}, errors.New("after-B parse failure")
		},
		clear: func(physical Physical) error {
			clearAttempts++
			if clearAttempts == 1 {
				return errors.New("B confirmation refused")
			}
			if !physical.PossiblyCommitted || physical.HintToken == 0 {
				t.Fatalf("unexpected recovery physical: %#v", physical)
			}
			return nil
		},
	}
	result, err := s.Replace(context.Background(), p, srcFor("foo.c"), []int{12}, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Breakpoints[0].Verified || len(result.Orphans) != 1 || s.OrphanCount() != 1 || s.DAPOwnedCount() != 1 {
		t.Fatalf("result=%#v orphaned=%d owned=%d", result, s.OrphanCount(), s.DAPOwnedCount())
	}
	cleanup, err := s.CleanupPending(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Cleared != 1 || s.OrphanCount() != 0 || s.DAPOwnedCount() != 0 || clearAttempts != 2 {
		t.Fatalf("cleanup=%#v orphaned=%d owned=%d clearAttempts=%d", cleanup, s.OrphanCount(), s.DAPOwnedCount(), clearAttempts)
	}
}

func TestReplaceRejectsDivergenceAndRemovesSuccessfulRollbackTokens(t *testing.T) {
	s := NewStore()
	p := &fakePlacer{set: func(request PlacementRequest) (Physical, error) {
		line := request.Line
		if request.Core == 4 {
			line++
		}
		return Physical{ActualLine: line, MULTIHandle: "h"}, nil
	}}
	result, err := s.Replace(context.Background(), p, srcFor("foo.c"), []int{12}, []int{0, 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.Breakpoints[0].Verified || len(p.clears) != 2 || s.DAPOwnedCount() != 0 {
		t.Fatalf("result=%#v clears=%d owned=%d", result, len(p.clears), s.DAPOwnedCount())
	}
	for _, request := range p.sets {
		if _, ok := s.LookupHint(request.HintToken); ok {
			t.Fatalf("rolled-back token %#x remained live", request.HintToken)
		}
	}
}

func TestReplaceRetiresOldTokensAfterSuccessfulRemoval(t *testing.T) {
	s := NewStore()
	p := &fakePlacer{}
	first, err := s.Replace(context.Background(), p, srcFor("foo.c"), []int{12}, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	token := first.Breakpoints[0].Physical[0].HintToken
	if _, err := s.Replace(context.Background(), p, srcFor("foo.c"), []int{30}, []int{0}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LookupHint(token); ok {
		t.Fatal("removed breakpoint token remained live")
	}
}

func TestRequestCleanupRetainsDetachedOwnershipAndDowngradesHint(t *testing.T) {
	s := NewStore()
	p := &fakePlacer{}
	result, err := s.ReplaceForOwner(context.Background(), p, "frontend-1", srcFor("foo.c"), []int{12}, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	physical := result.Breakpoints[0].Physical[0]
	if got := s.RequestCleanup("frontend-1"); got != 1 {
		t.Fatalf("RequestCleanup() = %d, want 1", got)
	}
	if s.DAPOwnedCount() != 1 || s.PendingCount() != 1 || s.OrphanCount() != 0 {
		t.Fatalf("counts = owned=%d pending=%d orphaned=%d", s.DAPOwnedCount(), s.PendingCount(), s.OrphanCount())
	}
	target, ok := s.LookupHint(physical.HintToken)
	if !ok || target.DAPID != 0 || target.Core != physical.Core {
		t.Fatalf("detached hint target = %#v, %v", target, ok)
	}

	if _, err := s.ReplaceForOwner(context.Background(), p, "frontend-2", srcFor("foo.c"), []int{12}, []int{0}); err != nil {
		t.Fatalf("replacement frontend inherited detached line: %v", err)
	}
	cleanup, err := s.CleanupPending(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Cleared != 1 || s.PendingCount() != 0 || s.DAPOwnedCount() != 1 {
		t.Fatalf("cleanup=%#v owned=%d pending=%d", cleanup, s.DAPOwnedCount(), s.PendingCount())
	}
	if _, ok := s.LookupHint(physical.HintToken); ok {
		t.Fatal("cleared detached breakpoint token remained live")
	}
}

func TestCleanupPendingPreservesClearFailureAsOrphan(t *testing.T) {
	s := NewStore()
	p := &fakePlacer{clear: func(Physical) error { return errors.New("refused") }}
	if _, err := s.ReplaceForOwner(context.Background(), p, "frontend-1", srcFor("foo.c"), []int{12}, []int{0}); err != nil {
		t.Fatal(err)
	}
	s.RequestCleanup("frontend-1")
	cleanup, err := s.CleanupPending(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Cleared != 0 || cleanup.Orphaned != 1 || s.PendingCount() != 0 || s.OrphanCount() != 1 || s.DAPOwnedCount() != 1 {
		t.Fatalf("cleanup=%#v owned=%d pending=%d orphaned=%d", cleanup, s.DAPOwnedCount(), s.PendingCount(), s.OrphanCount())
	}
}

func TestRequestCleanupWinsAgainstInflightReplacementCommit(t *testing.T) {
	s := NewStore()
	started := make(chan struct{})
	release := make(chan struct{})
	p := &fakePlacer{set: func(request PlacementRequest) (Physical, error) {
		close(started)
		<-release
		return Physical{ActualLine: request.Line, MULTIHandle: "h"}, nil
	}}
	type outcome struct {
		result ReplaceResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := s.ReplaceForOwner(context.Background(), p, "frontend-1", srcFor("foo.c"), []int{12}, []int{0})
		done <- outcome{result: result, err: err}
	}()
	<-started
	if moved := s.RequestCleanup("frontend-1"); moved != 0 {
		t.Fatalf("RequestCleanup() moved %d records before transaction commit, want 0", moved)
	}
	close(release)
	result := <-done
	if result.err != nil || len(result.result.Breakpoints) != 1 || !result.result.Breakpoints[0].Verified {
		t.Fatalf("ReplaceForOwner() = (%#v, %v)", result.result, result.err)
	}
	physical := result.result.Breakpoints[0].Physical[0]
	if s.PendingCount() != 1 || s.OrphanCount() != 0 || s.DAPOwnedCount() != 1 {
		t.Fatalf("post-detach counts = pending=%d orphaned=%d owned=%d", s.PendingCount(), s.OrphanCount(), s.DAPOwnedCount())
	}
	if target, ok := s.LookupHint(physical.HintToken); !ok || target.DAPID != 0 {
		t.Fatalf("detached in-flight hint target = %#v, %v", target, ok)
	}
	if _, err := s.CleanupPending(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if s.DAPOwnedCount() != 0 {
		t.Fatalf("post-cleanup owned=%d, want 0", s.DAPOwnedCount())
	}
}

func TestDetachFenceRetainsInflightPossiblyCommittedRecovery(t *testing.T) {
	s := NewStore()
	started := make(chan struct{})
	release := make(chan struct{})
	clearAttempts := 0
	p := &fakePlacer{
		set: func(PlacementRequest) (Physical, error) {
			close(started)
			<-release
			return Physical{PossiblyCommitted: true}, errors.New("after-B transport failure")
		},
		clear: func(Physical) error {
			clearAttempts++
			if clearAttempts == 1 {
				return errors.New("clear confirmation lost")
			}
			return nil
		},
	}
	type outcome struct {
		result ReplaceResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := s.ReplaceForOwner(context.Background(), p, "frontend-1", srcFor("foo.c"), []int{12}, []int{0})
		done <- outcome{result: result, err: err}
	}()
	<-started
	if moved, installed := s.RequestCleanupSignal("frontend-1"); moved != 0 || !installed {
		t.Fatalf("RequestCleanupSignal() = (%d, %t)", moved, installed)
	}
	close(release)
	got := <-done
	if got.err != nil || got.result.Breakpoints[0].Verified {
		t.Fatalf("ReplaceForOwner() = (%#v, %v)", got.result, got.err)
	}
	if s.PendingCount() != 1 || s.OrphanCount() != 0 || s.DAPOwnedCount() != 1 {
		t.Fatalf("post-detach pending=%d orphaned=%d owned=%d", s.PendingCount(), s.OrphanCount(), s.DAPOwnedCount())
	}
	cleanup, err := s.CleanupPending(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Cleared != 1 || s.DAPOwnedCount() != 0 || clearAttempts != 2 {
		t.Fatalf("cleanup=%#v owned=%d attempts=%d", cleanup, s.DAPOwnedCount(), clearAttempts)
	}
}
