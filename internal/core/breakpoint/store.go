// Package breakpoint implements the DAP-facing breakpoint store described in
// architecture.md section 6.5.
//
// The store performs no I/O. It never talks to MULTI and never talks to the
// bridge. Its job is purely computational: Diff turns a requested set of
// source lines into a Plan of what to add, remove, and keep relative to what
// this store already believes is live; Commit takes the PhysicalResults that
// the caller collected by actually executing that plan (through the bridge,
// against hardware) and turns them into Logical breakpoints with a verified
// or failed status. Every side effect a caller needs — issuing bp_set,
// issuing bp_clear, listening for hint datagrams — happens outside this
// package. That split is what keeps this store fully unit-testable without a
// bridge, a session, or hardware.
//
// # Replacement semantics
//
// DAP's setBreakpoints request carries the complete desired set of
// breakpoints for a source, not an incremental delta. Diff computes the
// three-way split (Add/Remove/Keep) against the lines this store currently
// tracks for that source.Identity.Key, exactly as architecture.md 6.5
// describes:
//
//	requested   foo.c = {12, 30}
//	current     foo.c = {12, 20}
//	diff        +30  -20  keep 12
//
// # Atomicity
//
// A DAP breakpoint is logical and may back onto several physical
// breakpoints, one per core whose ELF contains the source line. The logical
// breakpoint is atomic: it is reported verified only when every core that
// was attempted succeeded and every core agreed on the same actual line.
// Architecture.md 6.5 argues for this deliberately over a best-effort
// alternative: reporting verified=false while a breakpoint is quietly live
// on some core is worse than reporting failure cleanly, because the user is
// told nothing is set while the target stops anyway. When a logical
// breakpoint fails verification, RollbackNeeded reports exactly the physical
// breakpoints that were actually created (and therefore need a bp_clear) so
// the caller can undo them.
//
// # Ownership never forgets on failure
//
// Ownership records are never deleted just because a commit failed
// verification. A verified=false response must never cause multi-dap to
// forget a breakpoint it created — if it did, and rollback of that
// breakpoint later failed too (for instance because bp_clear itself
// perturbs a running target, per the M0-7 risk noted in architecture.md
// 6.5), the physical breakpoint would become orphaned on the target with no
// record anywhere that multi-dap put it there, and therefore no way to ever
// remove it. MarkOrphaned exists for exactly that path: when the caller's
// rollback attempt itself fails, the physical breakpoint moves into the
// store's orphaned set instead of disappearing, so it keeps counting toward
// DAPOwnedCount and remains a candidate for a retry at the next natural
// stop.
package breakpoint

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

// Physical is one MULTI-side breakpoint backing a Logical breakpoint on one
// core.
type Physical struct {
	Core          int
	RequestedLine int
	ActualLine    int
	MULTIHandle   string
	HintToken     uint32
	Orphaned      bool
}

// Logical is the DAP-facing breakpoint. It keeps one id regardless of how
// many cores it maps to; hitBreakpointIds on the stop event reports which
// Logical breakpoint fired (architecture.md 6.5).
type Logical struct {
	DAPID    int
	Source   source.Identity
	Line     int
	Physical []Physical
	Verified bool
	Message  string
}

// RollbackNeeded returns the physical breakpoints that were successfully
// created for this Logical breakpoint but whose overall verification failed
// — the set that must be undone with bp_clear so the logical breakpoint does
// not end up quietly live on some cores while DAP was told it failed. It
// returns nil once the breakpoint is verified: nothing needs to be undone.
func (l Logical) RollbackNeeded() []Physical {
	if l.Verified {
		return nil
	}
	return l.Physical
}

// PhysicalResult is what a caller reports back to Commit after actually
// attempting to create one physical breakpoint on one core. OK=false means
// bp_set failed on that core; Err carries MULTI's explanation. HintToken is
// optional: the caller generates it up front with NewHintToken and embeds it
// in the bp_set command list before the MULTIHandle is known, and may thread
// it back here so Commit can record it on the resulting Physical.
type PhysicalResult struct {
	Core          int
	RequestedLine int
	ActualLine    int
	MULTIHandle   string
	HintToken     uint32
	OK            bool
	Err           string
}

// Plan is the output of Diff: what a caller must do to bring the target's
// breakpoints for one source in line with a freshly requested set of lines.
type Plan struct {
	Add    []int
	Remove []Physical
	Keep   []int

	// src is the source.Identity this plan was computed against. It rides
	// along on the Plan (rather than being a separate Commit parameter)
	// because Commit needs it to know which source's bookkeeping to update,
	// and threading it back through the caller would just be an extra
	// argument that always has to match what Diff was given.
	src source.Identity
}

// Store holds every breakpoint multi-dap has created, across all sources and
// cores, for the lifetime of one debug session. It performs no I/O; see the
// package doc comment.
type Store struct {
	mu sync.Mutex

	// bySource maps a source's canonical key to the Logical breakpoints this
	// store currently believes are live on the target, keyed by requested
	// line. This is the "current" side of Diff's replacement comparison.
	bySource map[string]map[int]*Logical

	// orphaned holds physical breakpoints whose removal could not be
	// confirmed — see MarkOrphaned. They are no longer part of any tracked
	// Logical breakpoint's live set, but they still count as DAP-owned so
	// they are never silently lost.
	orphaned []Physical

	nextDAPID int

	hintTokens map[uint32]bool
}

// NewStore returns an empty breakpoint store.
func NewStore() *Store {
	return &Store{
		bySource:   map[string]map[int]*Logical{},
		hintTokens: map[uint32]bool{},
	}
}

// Diff compares the requested set of lines for src against the lines this
// store currently tracks for src, and returns the replacement plan: lines to
// add, the physical breakpoints backing lines to remove, and lines to leave
// untouched. Diff does not mutate the store; Commit does, once the caller
// reports what actually happened when it executed the plan.
func (s *Store) Diff(src source.Identity, lines []int) Plan {
	s.mu.Lock()
	defer s.mu.Unlock()

	requested := make(map[int]bool, len(lines))
	for _, l := range lines {
		requested[l] = true
	}

	existing := s.bySource[src.Key]

	plan := Plan{src: src}
	for _, l := range lines {
		if existing != nil {
			if _, ok := existing[l]; ok {
				plan.Keep = append(plan.Keep, l)
				continue
			}
		}
		plan.Add = append(plan.Add, l)
	}

	// Deterministic order: iterate existing lines sorted, not map order, so
	// Remove's ordering does not depend on Go's randomized map iteration.
	if existing != nil {
		removedLines := make([]int, 0, len(existing))
		for line := range existing {
			if !requested[line] {
				removedLines = append(removedLines, line)
			}
		}
		sort.Ints(removedLines)
		for _, line := range removedLines {
			plan.Remove = append(plan.Remove, existing[line].Physical...)
		}
	}

	return plan
}

// Commit records the outcome of actually executing a Plan: results reports,
// per attempted core, whether each Add line's physical breakpoint was
// created and where it actually landed. Commit builds one Logical breakpoint
// per plan.Add line, verified only when every result for that line
// succeeded and every successful result agreed on the same ActualLine
// (architecture.md 6.5's atomicity rule). Lines in plan.Remove stop being
// tracked as "current" here, on the assumption that the caller is executing
// their removal; if that removal itself fails, the caller must call
// MarkOrphaned with the specific Physical so it is not silently forgotten.
//
// Commit never returns an error for a failed breakpoint — a failed
// verification is reported through Logical.Verified and Logical.Message, not
// through the error return. The error return is reserved for a plan Commit
// cannot process at all (for example, one built by a different Store).
func (s *Store) Commit(plan Plan, results []PhysicalResult) ([]Logical, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := s.bySource[plan.src.Key]
	if bucket == nil {
		bucket = map[int]*Logical{}
		s.bySource[plan.src.Key] = bucket
	}

	for _, p := range plan.Remove {
		delete(bucket, p.RequestedLine)
	}

	grouped := make(map[int][]PhysicalResult, len(plan.Add))
	for _, r := range results {
		grouped[r.RequestedLine] = append(grouped[r.RequestedLine], r)
	}

	out := make([]Logical, 0, len(plan.Add))
	for _, line := range plan.Add {
		rs := grouped[line]

		lg := Logical{
			Source: plan.src,
			Line:   line,
		}

		okCount := 0
		for _, r := range rs {
			if !r.OK {
				continue
			}
			okCount++
			lg.Physical = append(lg.Physical, Physical{
				Core:          r.Core,
				RequestedLine: r.RequestedLine,
				ActualLine:    r.ActualLine,
				MULTIHandle:   r.MULTIHandle,
				HintToken:     r.HintToken,
			})
		}

		divergent := false
		for i := 1; i < len(lg.Physical); i++ {
			if lg.Physical[i].ActualLine != lg.Physical[0].ActualLine {
				divergent = true
				break
			}
		}

		switch {
		case len(rs) == 0:
			lg.Verified = false
			lg.Message = "no core reported a result for this breakpoint"
		case okCount != len(rs):
			lg.Verified = false
			lg.Message = describeFailure(rs)
		case divergent:
			lg.Verified = false
			lg.Message = describeFailure(rs)
		default:
			lg.Verified = true
		}

		s.nextDAPID++
		lg.DAPID = s.nextDAPID

		// Ownership record is kept regardless of Verified: see the package
		// doc comment's "Ownership never forgets on failure" section. Even a
		// wholly-failed line (okCount == 0, lg.Physical empty) is recorded
		// so DAPOwnedCount and future Diff calls stay consistent with what
		// this store told the caller to attempt.
		bucket[line] = &lg
		out = append(out, lg)
	}

	return out, nil
}

// describeFailure builds a human-readable explanation for why a logical
// breakpoint's commit did not verify, either because one or more cores
// failed outright or because the cores that succeeded disagreed on the
// actual line.
func describeFailure(rs []PhysicalResult) string {
	var failed []string
	actualsToCores := map[int][]int{}
	for _, r := range rs {
		if !r.OK {
			reason := r.Err
			if reason == "" {
				reason = "failed"
			}
			failed = append(failed, fmt.Sprintf("core %d: %s", r.Core, reason))
			continue
		}
		actualsToCores[r.ActualLine] = append(actualsToCores[r.ActualLine], r.Core)
	}

	if len(failed) > 0 {
		sort.Strings(failed)
		return "rolled back: " + strings.Join(failed, "; ")
	}

	if len(actualsToCores) > 1 {
		lines := make([]int, 0, len(actualsToCores))
		for line := range actualsToCores {
			lines = append(lines, line)
		}
		sort.Ints(lines)
		parts := make([]string, 0, len(lines))
		for _, line := range lines {
			parts = append(parts, fmt.Sprintf("line %d on cores %v", line, actualsToCores[line]))
		}
		return "rolled back: cores resolved to different lines: " + strings.Join(parts, "; ")
	}

	return "rolled back: unexplained verification failure"
}

// MarkOrphaned records that a physical breakpoint's removal could not be
// confirmed — for instance because bp_clear could not be attempted while the
// target was running (the M0-7 risk architecture.md 6.5 calls out) or the
// bp_clear call itself failed. The physical breakpoint is moved into the
// store's orphaned set rather than dropped: it keeps counting toward
// DAPOwnedCount, and a caller can retry its removal at the next natural
// stop instead of losing track of a breakpoint still live on the target.
func (s *Store) MarkOrphaned(p Physical) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Orphaned = true
	s.orphaned = append(s.orphaned, p)
}

// DAPOwnedCount returns the number of physical breakpoints multi-dap
// currently believes it owns on the target: everything tracked under a
// Logical breakpoint plus everything moved into the orphaned set. This is
// the figure architecture.md 6.5 requires "status" to report explicitly, and
// it is unaffected by verification failures — see the package doc comment.
func (s *Store) DAPOwnedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	for _, bucket := range s.bySource {
		for _, lg := range bucket {
			n += len(lg.Physical)
		}
	}
	n += len(s.orphaned)
	return n
}

// NewHintToken draws a 32-bit, session-local-unique token from crypto/rand.
// Zero is rejected because it is used as an "absent" sentinel elsewhere, and
// a collision with any token already issued this session is rejected by
// rejection sampling. Architecture.md 6.5 is explicit that this token is not
// a security mechanism — 32 bits is not unguessable, and authenticity of the
// hint channel comes from the per-session nonce (architecture.md 7.6) — so a
// plain retry loop over crypto/rand is sufficient; there is no need for a
// constant-time or side-channel-hardened generator here.
func (s *Store) NewHintToken() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()

	for {
		var buf [4]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand is documented to never fail on supported
			// platforms; if it somehow does, there is no weaker fallback
			// worth falling back to for a value whose whole job is to be
			// unguessable enough to not collide by accident.
			panic("breakpoint: crypto/rand failed: " + err.Error())
		}
		tok := binary.LittleEndian.Uint32(buf[:])
		if tok == 0 {
			continue
		}
		if s.hintTokens[tok] {
			continue
		}
		s.hintTokens[tok] = true
		return tok
	}
}
