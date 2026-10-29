//go:build windows

package tracebridge

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func configureProcess(command *exec.Cmd) {
	const createNoWindow = 0x08000000
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true, CreationFlags: createNoWindow,
	}
}

func configureInheritance(command *exec.Cmd, handles []uintptr) {
	for _, handle := range handles {
		command.SysProcAttr.AdditionalInheritedHandles = append(
			command.SysProcAttr.AdditionalInheritedHandles, syscall.Handle(handle),
		)
	}
}

func validInheritedHandles(handles []uintptr) bool {
	getFlags := jobKernel32.NewProc("GetHandleInformation")
	for _, handle := range handles {
		var flags uint32
		if result, _, _ := getFlags.Call(handle, uintptr(unsafe.Pointer(&flags))); result == 0 ||
			flags&1 == 0 {
			return false
		}
	}
	return true
}

func ordinaryPath(path string) bool {
	return !strings.HasPrefix(path, `\\.\`) && !strings.HasPrefix(path, `\\?\`) &&
		!strings.Contains(strings.TrimPrefix(path, filepath.VolumeName(path)), ":")
}
