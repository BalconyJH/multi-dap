//go:build !windows

package bridge

import (
	"errors"
	"os/exec"
)

// Command is unavailable outside Windows because mpythonrun's console
// contract is a Windows-specific operational invariant.
func (s LaunchSpec) Command() (*exec.Cmd, error) {
	return nil, errors.New("bridge: mpythonrun launcher is supported only on Windows")
}
