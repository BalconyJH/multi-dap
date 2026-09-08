package handle

import (
	"errors"
	"testing"
)

func TestResolveSucceedsAtSameStopEpoch(t *testing.T) {
	s := NewStore()
	id, err := s.Alloc(KindFrame, 0, 5, "frame0")
	if err != nil {
		t.Fatalf("Alloc() error = %v", err)
	}
	h, err := s.Resolve(id, 5, true)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if h.Value.(string) != "frame0" {
		t.Fatalf("Value = %v, want frame0", h.Value)
	}
}

func TestResolveFailsWhenRunning(t *testing.T) {
	s := NewStore()
	id, _ := s.Alloc(KindFrame, 0, 5, "frame0")
	if _, err := s.Resolve(id, 5, false); !errors.Is(err, ErrStale) {
		t.Fatalf("Resolve() while running error = %v, want ErrStale", err)
	}
}

func TestResolveFailsAtLaterStopEpoch(t *testing.T) {
	s := NewStore()
	id, _ := s.Alloc(KindFrame, 0, 5, "frame0")
	if _, err := s.Resolve(id, 6, true); !errors.Is(err, ErrStale) {
		t.Fatalf("Resolve() at newer epoch error = %v, want ErrStale", err)
	}
}

func TestIDsAreMonotonicAcrossDrops(t *testing.T) {
	s := NewStore()
	first, _ := s.Alloc(KindFrame, 0, 1, "a")
	s.DropStopBound()
	second, _ := s.Alloc(KindFrame, 0, 2, "b")
	if second <= first {
		t.Fatalf("second id %d <= first id %d; ids must be monotonic across the session", second, first)
	}
}

func TestStaleIDNeverResolvesToALiveObject(t *testing.T) {
	s := NewStore()
	stale, _ := s.Alloc(KindVariable, 0, 1, "old")
	s.DropStopBound()
	fresh, _ := s.Alloc(KindVariable, 0, 2, "new")
	if stale == fresh {
		t.Fatal("a stale id was reused for a live object")
	}
	if _, err := s.Resolve(stale, 2, true); !errors.Is(err, ErrStale) {
		t.Fatalf("stale Resolve() error = %v, want ErrStale", err)
	}
}

func TestAllocatedIDsAreInDAPRange(t *testing.T) {
	s := NewStore()
	id, err := s.Alloc(KindScope, 0, 1, "locals")
	if err != nil {
		t.Fatalf("Alloc() error = %v", err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want a value in (0, 2^31)", id)
	}
}

func TestAllocAllowsFinalDAPIDThenRefusesExhaustion(t *testing.T) {
	s := NewStore()
	s.setNextForTest(1<<31 - 1)
	id, err := s.Alloc(KindFrame, 0, 1, "x")
	if err != nil || id != 1<<31-1 {
		t.Fatalf("Alloc() final DAP id = (%d, %v), want (%d, nil)", id, err, 1<<31-1)
	}
	if _, err := s.Alloc(KindFrame, 0, 1, "y"); err == nil {
		t.Fatal("Alloc() after final DAP id error = nil, want exhaustion")
	}
}

func TestCoreIsPreserved(t *testing.T) {
	s := NewStore()
	id, _ := s.Alloc(KindMemory, 4, 1, "mem")
	h, _ := s.Resolve(id, 1, true)
	if h.Core != 4 {
		t.Fatalf("Core = %d, want 4", h.Core)
	}
}
