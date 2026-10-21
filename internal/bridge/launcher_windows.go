//go:build windows

package bridge

import (
	"os"
	"os/exec"
	"strings"
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
	cmd.Env, err = commandEnvironment(s.PythonVersion)
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole}
	return cmd, nil
}

func commandEnvironment(rawPythonVersion int) ([]string, error) {
	pythonVersion, err := normalizePythonVersion(rawPythonVersion)
	if err != nil {
		return nil, err
	}

	// exec.Cmd replaces (rather than augments) the environment when Env is
	// non-nil. Remove any inherited spelling of this Windows case-insensitive
	// variable before appending the launcher's authoritative value.
	const variable = "GHS_IDE_USE_PYTHON3"
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		separator := strings.IndexByte(entry, '=')
		if separator > 0 && strings.EqualFold(entry[:separator], variable) {
			continue
		}
		environment = append(environment, entry)
	}
	value := "0"
	if pythonVersion == PythonVersion3 {
		value = "1"
	}
	return append(environment, variable+"="+value), nil
}
