// Package handle implements the stop-bound handle store specified in
// architecture.md §6.4.
//
// Frame, scope, variable, and memory handles are suspended-state references:
// per DAP they are valid only while the debuggee is suspended, and they
// become invalid the instant execution resumes — not when the next stop
// occurs. A Store hands out int32 ids in DAP's required (0, 2^31) range and
// answers every later lookup against the StopEpoch that was current when the
// id was minted.
package handle

import (
	"errors"
	"fmt"
	"sync"
)

// Kind identifies what a handle's Value holds.
type Kind int

const (
	KindFrame Kind = iota
	KindScope
	KindVariable
	KindMemory
)

// CoreID names one core of a multicore target.
//
// Core is carried on the handle independently of the stop-epoch validity
// check: cores do not share an address space, so a variable or memory view
// must never be resolved against the wrong one even when the handle is
// otherwise still valid.
type CoreID int

// Handle is one suspended-state reference: a frame, scope, variable, or
// memory view that only makes sense while the target that produced it is
// still stopped at the same stop.
type Handle struct {
	ID        int32
	Kind      Kind
	Core      CoreID
	StopEpoch uint64
	Value     any
}

// ErrStale is returned by Resolve when an id no longer names a live handle:
// the target has resumed since the id was allocated, the id was allocated
// for an earlier stop, or the id was never allocated at all. Callers that
// need to name the offending id in a DAP error message should use
// fmt.Errorf("handle %d: %w", id, ErrStale) and errors.Is to test the result.
var ErrStale = errors.New("stale handle")

// errExhausted is returned by Alloc when the id space is nearly exhausted.
// It is intentionally its own sentinel, distinct from ErrStale: running out
// of ids is an allocator condition, not a staleness condition, and the two
// must never be confused by a caller doing errors.Is(err, ErrStale).
var errExhausted = errors.New("handle id space exhausted")

// maxID is the last id Alloc will hand out. DAP requires variablesReference
// values in (0, 2^31); 1<<31-1 is the largest value that still fits. After
// allocating it, next becomes zero, the private exhausted sentinel.
const maxID = 1<<31 - 1

// Store allocates and resolves handles for one debug session.
//
// IDs are monotonic across the entire session; they are never reset or
// rewound, including when DropStopBound clears every stop-bound handle at a
// resume. Restarting the allocator at 1 after each resume would let a stale
// reference from a racing client resolve to a different, live object at the
// next stop and return plausible-looking wrong data instead of failing.
// Monotonic ids plus the StopEpoch check on Resolve instead make every stale
// reference fail loudly, every time. Exhaustion of the int32 range is not
// reachable in a realistic session; Alloc reports it as an explicit error
// rather than silently wrapping back to 1, which would reintroduce exactly
// the reuse hazard monotonicity exists to prevent.
//
// A Store is intended to be driven solely from the Session Actor goroutine
// (architecture.md §6.1), so in the steady state there is only ever one
// caller. The mutex below is defensive — it guards against a future mistake,
// not a license for multiple goroutines to call into a Store concurrently as
// a matter of course.
type Store struct {
	mu   sync.Mutex
	next int32
	m    map[int32]Handle
}

// NewStore returns an empty Store with its id allocator starting at 1.
func NewStore() *Store {
	return &Store{
		next: 1,
		m:    make(map[int32]Handle),
	}
}

// Alloc mints a new handle for value, tagged with kind, core, and the
// StopEpoch current at allocation time, and returns its id.
//
// Alloc refuses once the next id to hand out would reach maxID, returning an
// explicit exhaustion error rather than wrapping around. Wrapping would
// eventually reuse an id that a stale client reference still names, which is
// precisely the failure mode the monotonic allocator exists to rule out.
func (s *Store) Alloc(kind Kind, core CoreID, stopEpoch uint64, value any) (int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.next <= 0 {
		return 0, fmt.Errorf("alloc kind=%d core=%d: %w", kind, core, errExhausted)
	}

	id := s.next
	if id == maxID {
		s.next = 0
	} else {
		s.next++
	}
	s.m[id] = Handle{
		ID:        id,
		Kind:      kind,
		Core:      core,
		StopEpoch: stopEpoch,
		Value:     value,
	}
	return id, nil
}

// Resolve looks up id and returns its Handle, but only when the debuggee is
// currently stopped (stopped == true) and the handle's StopEpoch equals the
// caller's supplied stopEpoch.
//
// Validity is keyed to StopEpoch rather than to mere presence in the map
// because a handle allocated at one stop must never resolve successfully
// once a later stop has occurred, even though "later stop" and "resumed" are
// observed at different times (architecture.md §6.2, §6.3): the epoch check
// is what makes a handle from stop N fail at stop N+1 even if DropStopBound
// has not yet run for some reason. stopped guards the other half of §6.4's
// rule — a handle is invalid the moment execution resumes, not merely once
// the next stop lands.
//
// Any failure is reported as ErrStale, wrapped with the id so a DAP error
// message can name it; errors.Is(err, ErrStale) still succeeds.
func (s *Store) Resolve(id int32, stopEpoch uint64, stopped bool) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.m[id]
	if !ok || !stopped || h.StopEpoch != stopEpoch {
		return Handle{}, fmt.Errorf("handle %d: %w", id, ErrStale)
	}
	return h, nil
}

// DropStopBound discards every handle currently held, in response to a
// confirmed Stopped -> Running transition (architecture.md §6.2, §6.4). It
// never rewinds the id allocator: the next Alloc continues from where it
// left off, so an id that named a dropped handle can never be handed out
// again.
func (s *Store) DropStopBound() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.m = make(map[int32]Handle)
}

// setNextForTest forces the next id Alloc will hand out. It exists solely so
// package tests can exercise near-exhaustion behavior without allocating two
// billion handles. It is deliberately not exported: production callers must
// not be able to alter the monotonic allocator.
func (s *Store) setNextForTest(next int32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.next = next
}
