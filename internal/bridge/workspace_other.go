//go:build !windows

package bridge

import (
	"fmt"
	"os"
)

// secureStartupWorkspace removes any permissions inherited from the system
// temporary directory.  bridge.py creates ready.json and console snapshots
// beneath this directory, so 0700 is the private boundary for all of them.
func secureStartupWorkspace(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("restrict startup workspace: %w", err)
	}
	return nil
}
