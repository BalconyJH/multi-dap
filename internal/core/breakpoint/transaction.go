package breakpoint

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

// PlacementRequest is a bridge-neutral request to create one physical
// breakpoint. HintToken is allocated before Set, because it has to be present
// in the breakpoint command list before the physical handle exists.
type PlacementRequest struct {
	Source    source.Identity
	Line      int
	Core      int
	HintToken uint32
}

// Placer is the entire side-effect boundary required by breakpoint placement.
// It deliberately carries structured values only: MULTI command spelling and
// response parsing belong below Debugger Core. Set may return a Physical with
// PossiblyCommitted set together with an error. That physical is a recovery
// intent, not a successful placement: Store owns it immediately and must
// Clear it before reporting the failed logical breakpoint.
type Placer interface {
	Set(context.Context, PlacementRequest) (Physical, error)
	Clear(context.Context, Physical) error
}

// ReplaceResult describes one replacement-semantics operation. Breakpoints
// contains an entry for every requested source line, including kept lines and
// unverified placement failures. Orphans are still physically live and must
// be retried at a natural stop by the session owner.
type ReplaceResult struct {
	Breakpoints []Logical
	Orphans     []Physical
}

// Replace applies replacement semantics for src. Each new logical breakpoint
// is atomic across cores: every expected core must set it and resolve it to
// the same line, otherwise every successful placement is cleared. A rollback
// failure is retained as an orphan; ownership is never forgotten.
//
// Existing breakpoints are cleared only after all requested additions are
// resolved. A failed clear similarly becomes an orphan rather than being
// silently discarded. Store serializes physical transactions separately from
// its short-lived state lock, so status and detach can proceed while MULTI
// calls are in flight.
func (s *Store) Replace(ctx context.Context, placer Placer, src source.Identity, lines, cores []int) (ReplaceResult, error) {
	return s.ReplaceForOwner(ctx, placer, "", src, lines, cores)
}

// ReplaceForOwner applies replacement semantics to one frontend owner's live
// configuration. Ownership is part of the store invariant rather than an
// actor-side convention, so a later frontend cannot clear a prior owner's
// records after detach has made them pending cleanup.
func (s *Store) ReplaceForOwner(ctx context.Context, placer Placer, owner Owner, src source.Identity, lines, cores []int) (ReplaceResult, error) {
	if placer == nil {
		return ReplaceResult{}, errors.New("breakpoint: placer is required")
	}
	if src.Key == "" {
		return ReplaceResult{}, errors.New("breakpoint: source key is required")
	}
	requested, err := uniquePositive(lines, "line")
	if err != nil {
		return ReplaceResult{}, err
	}
	expected, err := uniqueNonNegative(cores, "core")
	if err != nil {
		return ReplaceResult{}, err
	}
	if len(requested) != 0 && len(expected) == 0 {
		return ReplaceResult{}, errors.New("breakpoint: placement requires at least one core")
	}

	// The transaction lock protects physical mutation order. The state lock is
	// acquired only to take a plan and to commit its outcome; no MULTI I/O runs
	// while it is held.
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()

	s.mu.Lock()
	if s.cleanupOwners[owner] {
		s.mu.Unlock()
		return ReplaceResult{}, errors.New("breakpoint: owner has detached")
	}
	bucket := s.bySource[src.Key]
	want := make(map[int]struct{}, len(requested))
	for _, line := range requested {
		want[line] = struct{}{}
	}
	kept := make(map[int]Logical, len(requested))
	add := make([]int, 0, len(requested))
	for _, line := range requested {
		if current := bucket[line]; current != nil {
			if current.Owner != owner {
				s.mu.Unlock()
				return ReplaceResult{}, errors.New("breakpoint: source line belongs to another frontend")
			}
			kept[line] = cloneLogical(*current)
			continue
		}
		add = append(add, line)
	}
	oldLines := make([]int, 0)
	for line := range bucket {
		if _, keep := want[line]; !keep {
			oldLines = append(oldLines, line)
		}
	}
	sort.Ints(oldLines)
	old := make(map[int]Logical, len(oldLines))
	for _, line := range oldLines {
		if logical := bucket[line]; logical != nil && logical.Owner == owner {
			old[line] = cloneLogical(*logical)
		}
	}
	s.mu.Unlock()

	placed := make(map[int]placementOutcome, len(add))
	for _, line := range add {
		placed[line] = s.placeLogical(ctx, placer, owner, src, line, expected)
	}

	// Existing lines are cleared only after all additions have reached their
	// own atomic outcome. A detach fence observed here suppresses further
	// replacement clearing: RequestCleanup has already moved those records to
	// the stopped-bound queue.
	clears := make(map[uint32]error)
	for _, line := range oldLines {
		for _, physical := range old[line].Physical {
			s.mu.Lock()
			detached := s.cleanupOwners[owner]
			s.mu.Unlock()
			if detached {
				continue
			}
			clears[physical.HintToken] = placer.Clear(ctx, physical)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	detached := s.cleanupOwners[owner]
	result := ReplaceResult{Breakpoints: make([]Logical, 0, len(requested))}
	for _, line := range requested {
		if logical, ok := kept[line]; ok {
			result.Breakpoints = append(result.Breakpoints, logical)
			continue
		}
		outcome := placed[line]
		logical := outcome.logical
		s.nextDAPID++
		logical.DAPID = s.nextDAPID
		if logical.Verified && !detached {
			if s.bySource[src.Key] == nil {
				s.bySource[src.Key] = make(map[int]*Logical)
			}
			s.bySource[src.Key][line] = &logical
			for _, physical := range logical.Physical {
				s.liveHints[physical.HintToken] = HintTarget{DAPID: logical.DAPID, Core: physical.Core}
			}
		} else if logical.Verified {
			for _, physical := range logical.Physical {
				s.appendPendingLocked(physical)
			}
		}
		for _, physical := range outcome.rollbackFailed {
			if detached {
				s.appendPendingLocked(physical)
			} else {
				s.appendOrphanedLocked(physical)
				result.Orphans = append(result.Orphans, physical)
			}
		}
		result.Breakpoints = append(result.Breakpoints, logical)
	}

	for _, line := range oldLines {
		logical := old[line]
		if !detached {
			if current := s.bySource[src.Key][line]; current != nil && current.Owner == owner {
				delete(s.bySource[src.Key], line)
			}
		}
		for _, physical := range logical.Physical {
			err, attempted := clears[physical.HintToken]
			if !attempted {
				continue
			}
			if err == nil {
				s.removePhysicalEvidenceLocked(physical)
				continue
			}
			if !detached {
				s.appendOrphanedLocked(physical)
				result.Orphans = append(result.Orphans, physical)
			}
		}
	}
	if bucket := s.bySource[src.Key]; len(bucket) == 0 {
		delete(s.bySource, src.Key)
	}
	return result, nil
}

type placementOutcome struct {
	logical        Logical
	rollbackFailed []Physical
}

func (s *Store) placeLogical(ctx context.Context, placer Placer, owner Owner, src source.Identity, line int, cores []int) placementOutcome {
	logical := Logical{Owner: owner, Source: src, Line: line}
	created := make([]Physical, 0, len(cores))
	for _, core := range cores {
		token := s.NewHintToken()
		physical, err := placer.Set(ctx, PlacementRequest{Source: src, Line: line, Core: core, HintToken: token})
		physical.Owner, physical.Core, physical.RequestedLine, physical.HintToken = owner, core, line, token
		if err != nil {
			if physical.PossiblyCommitted {
				created = append(created, physical)
			}
			return rollbackPlacement(ctx, placer, logical, created, fmt.Errorf("core %d: %w", core, err))
		}
		if physical.PossiblyCommitted {
			// A success result cannot remain an uncertain recovery intent. Treat
			// it as a backend contract violation but still recover the target
			// state rather than forgetting a potentially live breakpoint.
			created = append(created, physical)
			return rollbackPlacement(ctx, placer, logical, created, fmt.Errorf("core %d: placer returned an uncertain placement without an error", core))
		}
		created = append(created, physical)
	}
	for index := 1; index < len(created); index++ {
		if created[index].ActualLine != created[0].ActualLine {
			return rollbackPlacement(ctx, placer, logical, created, fmt.Errorf("cores resolved to different lines (%d and %d)", created[0].ActualLine, created[index].ActualLine))
		}
	}
	logical.Physical, logical.Verified = created, true
	return placementOutcome{logical: logical}
}

// rollbackPlacement owns every physical record that could have reached the
// target, including uncertain Set recovery intents. It is intentionally the
// one place that performs best-effort cleanup: individual placers report all
// failures, while Store persists every unresolved record as an orphan.
func rollbackPlacement(ctx context.Context, placer Placer, logical Logical, created []Physical, failure error) placementOutcome {
	logical.Message = "rolled back: " + failure.Error()
	// A DAP id makes the unverified response stable and explicitly distinct
	// from a token or physical handle. It is intentionally not retained as a
	// live logical breakpoint after successful rollback.
	var rollbackFailed []Physical
	for _, physical := range created {
		if err := placer.Clear(ctx, physical); err != nil {
			rollbackFailed = append(rollbackFailed, physical)
		}
	}
	return placementOutcome{logical: logical, rollbackFailed: rollbackFailed}
}

func cloneLogical(value Logical) Logical {
	value.Physical = append([]Physical(nil), value.Physical...)
	return value
}

func uniquePositive(values []int, noun string) ([]int, error) {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			return nil, fmt.Errorf("breakpoint: %s must be positive", noun)
		}
		seen[value] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Ints(out)
	return out, nil
}

func uniqueNonNegative(values []int, noun string) ([]int, error) {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if value < 0 {
			return nil, fmt.Errorf("breakpoint: %s must not be negative", noun)
		}
		seen[value] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Ints(out)
	return out, nil
}
