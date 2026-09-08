package source

import (
	"errors"
	"fmt"
	"sync"
)

// Presence is the result of asking one core whether it knows a source file.
// Unknown is deliberately distinct from Absent: transport failures, lossy
// output, and an unverified command grammar must not suppress a breakpoint.
type Presence uint8

const (
	PresenceUnknown Presence = iota
	PresencePresent
	PresenceAbsent
)

func (p Presence) String() string {
	switch p {
	case PresenceUnknown:
		return "unknown"
	case PresencePresent:
		return "present"
	case PresenceAbsent:
		return "absent"
	default:
		return fmt.Sprintf("invalid_presence(%d)", p)
	}
}

// Observation is one authoritative per-core source lookup result.
type Observation struct {
	Core     int
	Presence Presence
}

// Cache retains definitive source observations. Unknown is never recorded so
// a later query can recover after a transient bridge or target problem.
type Cache struct {
	mu     sync.RWMutex
	byFile map[string]map[int]Presence
}

func NewCache() *Cache {
	return &Cache{byFile: make(map[string]map[int]Presence)}
}

// Lookup returns a cached Present or Absent result for identity on core.
func (c *Cache) Lookup(identity Identity, core int) (Presence, bool) {
	if c == nil || identity.Key == "" || core < 0 {
		return PresenceUnknown, false
	}
	c.mu.RLock()
	presence, ok := c.byFile[identity.Key][core]
	c.mu.RUnlock()
	return presence, ok
}

// Record stores a definitive result. Unknown is rejected rather than cached.
func (c *Cache) Record(identity Identity, core int, presence Presence) error {
	if c == nil {
		return errors.New("source: nil cache")
	}
	if identity.Key == "" {
		return errors.New("source: source key is required")
	}
	if core < 0 {
		return fmt.Errorf("source: core %d must not be negative", core)
	}
	if presence != PresencePresent && presence != PresenceAbsent {
		return fmt.Errorf("source: cannot cache %s", presence)
	}
	c.mu.Lock()
	byCore := c.byFile[identity.Key]
	if byCore == nil {
		byCore = make(map[int]Presence)
		c.byFile[identity.Key] = byCore
	}
	byCore[core] = presence
	c.mu.Unlock()
	return nil
}
