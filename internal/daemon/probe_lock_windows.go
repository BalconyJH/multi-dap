//go:build windows

package daemon

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	winErrorAlreadyExists = syscall.Errno(183)
	winErrorFileNotFound  = syscall.Errno(2)
	winPageReadWrite      = 0x04
	winFileMapRead        = 0x0004
	winFileMapWrite       = 0x0002
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	createSemaphoreW    = kernel32.NewProc("CreateSemaphoreW")
	setLastError        = kernel32.NewProc("SetLastError")
	closeHandle         = kernel32.NewProc("CloseHandle")
	createFileMappingW  = kernel32.NewProc("CreateFileMappingW")
	openFileMappingW    = kernel32.NewProc("OpenFileMappingW")
	mapViewOfFile       = kernel32.NewProc("MapViewOfFile")
	unmapViewOfFile     = kernel32.NewProc("UnmapViewOfFile")
	getCurrentProcessID = kernel32.NewProc("GetCurrentProcessId")
	getCurrentProcess   = kernel32.NewProc("GetCurrentProcess")
	readProcessMemory   = kernel32.NewProc("ReadProcessMemory")
	writeProcessMemory  = kernel32.NewProc("WriteProcessMemory")
)

type windowsProbeLock struct {
	semaphore syscall.Handle
	mapping   syscall.Handle
	view      uintptr
	once      sync.Once
	err       error
}

func acquireProbeLock(probeID string) (probeLock, error) {
	// GetLastError is thread-local. Keep the CreateSemaphoreW call and its
	// ERROR_ALREADY_EXISTS observation on one OS thread despite Go scheduling.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	semaphoreName, mappingName := probeObjectNames(probeID)
	semaphoreName16, err := syscall.UTF16PtrFromString(semaphoreName)
	if err != nil {
		return nil, fmt.Errorf("daemon: encode probe lock name: %w", err)
	}
	_, _, _ = setLastError.Call(0)
	// A permanently non-signalled semaphore represents ownership through its
	// live handle. Unlike a mutex, that handle can be closed safely from a
	// different goroutine; unlike a signalled semaphore, no create/acquire gap
	// lets a concurrent starter steal the just-created object.
	handle, _, callErr := createSemaphoreW.Call(0, 0, 1, uintptr(unsafe.Pointer(semaphoreName16)))
	if handle == 0 {
		return nil, fmt.Errorf("daemon: create probe lock: %w", callErr)
	}
	if syscall.GetLastError() == winErrorAlreadyExists || errors.Is(callErr, winErrorAlreadyExists) {
		_, _, _ = closeHandle.Call(handle)
		return nil, &ProbeInUseError{ProbeID: probeID, HolderPID: readHolderPID(mappingName)}
	}
	lock := &windowsProbeLock{semaphore: syscall.Handle(handle)}
	if err := lock.publishPID(mappingName); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func probeObjectNames(probeID string) (lock string, mapping string) {
	digest := sha256.Sum256([]byte(probeID))
	key := fmt.Sprintf("%x", digest[:])
	return "Local\\multi-dap-probe-" + key + "-lock", "Local\\multi-dap-probe-" + key + "-metadata"
}

func (l *windowsProbeLock) publishPID(mappingName string) error {
	name, err := syscall.UTF16PtrFromString(mappingName)
	if err != nil {
		return fmt.Errorf("daemon: encode probe metadata name: %w", err)
	}
	invalidHandleValue := ^uintptr(0)
	mapping, _, callErr := createFileMappingW.Call(invalidHandleValue, 0, winPageReadWrite, 0, 4, uintptr(unsafe.Pointer(name)))
	if mapping == 0 {
		return fmt.Errorf("daemon: create probe metadata: %w", callErr)
	}
	view, _, callErr := mapViewOfFile.Call(mapping, winFileMapRead|winFileMapWrite, 0, 0, 4)
	if view == 0 {
		_, _, _ = closeHandle.Call(mapping)
		return fmt.Errorf("daemon: map probe metadata: %w", callErr)
	}
	pid, _, _ := getCurrentProcessID.Call()
	if err := writePID(view, uint32(pid)); err != nil {
		_, _, _ = unmapViewOfFile.Call(view)
		_, _, _ = closeHandle.Call(mapping)
		return err
	}
	l.mapping = syscall.Handle(mapping)
	l.view = view
	return nil
}

func readHolderPID(mappingName string) uint32 {
	name, err := syscall.UTF16PtrFromString(mappingName)
	if err != nil {
		return 0
	}
	for range 20 {
		mapping, _, callErr := openFileMappingW.Call(winFileMapRead, 0, uintptr(unsafe.Pointer(name)))
		if mapping == 0 {
			if syscall.GetLastError() == winErrorFileNotFound || errors.Is(callErr, winErrorFileNotFound) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return 0
		}
		view, _, _ := mapViewOfFile.Call(mapping, winFileMapRead, 0, 0, 4)
		if view != 0 {
			pid, _ := readPID(view)
			_, _, _ = unmapViewOfFile.Call(view)
			_, _, _ = closeHandle.Call(mapping)
			if pid != 0 {
				return pid
			}
		} else {
			_, _, _ = closeHandle.Call(mapping)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return 0
}

func (l *windowsProbeLock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		var first error
		if l.view != 0 {
			if writeErr := writePID(l.view, 0); writeErr != nil && first == nil {
				first = writeErr
			}
			if result, _, callErr := unmapViewOfFile.Call(l.view); result == 0 && first == nil {
				first = callErr
			}
			l.view = 0
		}
		if l.mapping != 0 {
			if result, _, callErr := closeHandle.Call(uintptr(l.mapping)); result == 0 && first == nil {
				first = callErr
			}
			l.mapping = 0
		}
		if l.semaphore != 0 {
			if result, _, callErr := closeHandle.Call(uintptr(l.semaphore)); result == 0 && first == nil {
				first = callErr
			}
			l.semaphore = 0
		}
		if first != nil {
			l.err = fmt.Errorf("daemon: release probe lock: %w", first)
		}
	})
	return l.err
}

func writePID(address uintptr, pid uint32) error {
	process, _, _ := getCurrentProcess.Call()
	if result, _, callErr := writeProcessMemory.Call(process, address, uintptr(unsafe.Pointer(&pid)), 4, 0); result == 0 {
		return fmt.Errorf("daemon: publish probe holder PID: %w", callErr)
	}
	return nil
}

func readPID(address uintptr) (uint32, error) {
	process, _, _ := getCurrentProcess.Call()
	var pid uint32
	if result, _, callErr := readProcessMemory.Call(process, address, uintptr(unsafe.Pointer(&pid)), 4, 0); result == 0 {
		return 0, fmt.Errorf("daemon: read probe holder PID: %w", callErr)
	}
	return pid, nil
}
