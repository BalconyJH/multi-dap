// Package multi is the MULTI Driver layer of architecture.md §4: the only
// place in this repository that legitimately parses Green Hills MULTI's
// text and dict output. Debugger Core talks to typed Go values; this
// package is where MULTI's own vocabulary — dict field names, column
// layout, "AT count:" — is translated into them and nowhere else.
//
// Every format parsed here was captured on real MULTI 7.1.6d hardware, not
// documented by Green Hills. Following the MULTI-NOSTRUCT discipline of
// architecture.md §7.4, each parser's doc comment says so explicitly and
// the corresponding golden fixture under testdata/ is the contract: if a
// later MULTI point release changes the shape, the fixture is what breaks,
// not a hand-rolled assumption buried in a regex.
package multi

import "fmt"

// Status mirrors the return value of GHS_Debugger.GetStatus(), which is the
// same integer as MULTI's $_STATE system variable. The nine values below
// were verified against MULTI 7.1.6d on hardware; GetStatus() returns the
// raw integer, so these constants must keep exactly these numeric values
// rather than being reordered for Go-side convenience.
type Status int

const (
	StatusNil        Status = 0 // nil: no debugger context at all.
	StatusNoProcess  Status = 1 // no_process: debugger exists, nothing downloaded/attached.
	StatusStopped    Status = 2 // stopped: the only stopped state.
	StatusRunning    Status = 3 // running: executing freely.
	StatusDying      Status = 4 // dying: process is terminating.
	StatusForking    Status = 5 // forking: process is forking.
	StatusExecuting  Status = 6 // executing: running a debugger-driven operation.
	StatusContinuing Status = 7 // continuing: transitioning back to running after a stop.
	StatusZombie     Status = 8 // zombie: process has exited but is not yet reaped.
)

// String returns MULTI's own lowercase, underscore-separated spelling of
// the state, matching $_STATE's textual form.
func (s Status) String() string {
	switch s {
	case StatusNil:
		return "nil"
	case StatusNoProcess:
		return "no_process"
	case StatusStopped:
		return "stopped"
	case StatusRunning:
		return "running"
	case StatusDying:
		return "dying"
	case StatusForking:
		return "forking"
	case StatusExecuting:
		return "executing"
	case StatusContinuing:
		return "continuing"
	case StatusZombie:
		return "zombie"
	default:
		return fmt.Sprintf("unknown_status(%d)", int(s))
	}
}

// IsStopped reports whether the target is suspended. StatusStopped is the
// only state for which this is true — architecture.md §6.4's handle
// lifetime and §6.2's epoch transitions both key off exactly this state,
// not off "not running".
func (s Status) IsStopped() bool {
	return s == StatusStopped
}

// IsRunning reports whether the target is executing in any of the senses
// that matter to the state machine of architecture.md §6.2:
// StatusRunning (free execution), StatusExecuting (a debugger-driven
// operation is under way), and StatusContinuing (mid-transition back to
// running after a stop) all count. StatusForking and StatusDying are
// process-lifecycle states, not "running" in the execution sense the
// Stopped/Running epoch model cares about, so they are deliberately
// excluded.
func (s Status) IsRunning() bool {
	switch s {
	case StatusRunning, StatusExecuting, StatusContinuing:
		return true
	default:
		return false
	}
}
