//go:build windows

package bridge

import (
	"os"
	"os/exec"
	"syscall"
)

const createNewConsole = 0x00000010

// Command prepares mpythonrun with its own console. Standard handles remain
// inherited rather than being replaced with pipes or files: MULTI writes via
// WriteConsole and can modal-stall if stdout is redirected.
func (s LaunchSpec) Command() (*exec.Cmd, error) {
	args, err := s.Args()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(s.Executable, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole}
	return cmd, nil
}
