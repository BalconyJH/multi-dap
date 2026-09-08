package daemon

import (
	"errors"
	"fmt"
)

// ErrProbeInUse means another multi-dap daemon currently owns the configured
// physical-probe identity. It says nothing about external MULTI or target
// server processes; those are reported by their own startup failures.
var ErrProbeInUse = errors.New("daemon: configured probe is already owned by another multi-dap daemon")

// ProbeInUseError identifies the duplicate-daemon holder when its PID can be
// published by the platform lock. errors.Is(err, ErrProbeInUse) remains true.
type ProbeInUseError struct {
	ProbeID   string
	HolderPID uint32
}

func (e *ProbeInUseError) Error() string {
	if e.HolderPID == 0 {
		return fmt.Sprintf("%v (probe %q; holder PID is publishing startup metadata)", ErrProbeInUse, e.ProbeID)
	}
	return fmt.Sprintf("%v (probe %q; holder PID %d)", ErrProbeInUse, e.ProbeID, e.HolderPID)
}

func (e *ProbeInUseError) Is(target error) bool { return target == ErrProbeInUse }

// probeLock is released only after Runtime has stopped accepting DAP work,
// released its frontend, shut down its actor, and reaped its owned child.
// The lock itself, rather than metadata, is the source of truth.
type probeLock interface{ Close() error }
