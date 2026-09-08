//go:build windows

package control

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	winErrorInvalidParameter       = syscall.Errno(87)
)

var (
	controlKernel32 = syscall.NewLazyDLL("kernel32.dll")
	openProcess     = controlKernel32.NewProc("OpenProcess")
	getExitCode     = controlKernel32.NewProc("GetExitCodeProcess")
	closeProcess    = controlKernel32.NewProc("CloseHandle")
)

func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	handle, _, callErr := openProcess.Call(processQueryLimitedInformation, 0, uintptr(uint32(pid)))
	if handle == 0 {
		if callErr == winErrorInvalidParameter {
			return false, nil
		}
		return false, fmt.Errorf("open PID %d: %w", pid, callErr)
	}
	defer closeProcess.Call(handle)
	var exitCode uint32
	if result, _, callErr := getExitCode.Call(handle, uintptr(unsafe.Pointer(&exitCode))); result == 0 {
		return false, fmt.Errorf("read PID %d exit state: %w", pid, callErr)
	}
	return exitCode == stillActive, nil
}
