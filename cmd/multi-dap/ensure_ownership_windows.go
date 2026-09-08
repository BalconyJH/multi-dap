//go:build windows

package main

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
)

var (
	ensureJobKernel32        = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectW         = ensureJobKernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = ensureJobKernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = ensureJobKernel32.NewProc("AssignProcessToJobObject")
	getCurrentEnsureProcess  = ensureJobKernel32.NewProc("GetCurrentProcess")

	ensureOwnershipOnce sync.Once
	ensureOwnershipErr  error
	ensureOwnershipJob  syscall.Handle
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IOInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// acquireEnsureChildOwnership puts only the detached serve child and every
// future descendant it creates in a kill-on-close Job. It runs before daemon
// startup, so an ensure timeout can never leave its mpythonrun bridge outside
// the exact child ownership tree.
func acquireEnsureChildOwnership() error {
	ensureOwnershipOnce.Do(func() {
		handle, _, callErr := createJobObjectW.Call(0, 0)
		if handle == 0 {
			ensureOwnershipErr = fmt.Errorf("create ensure child job: %w", callErr)
			return
		}
		limits := jobObjectExtendedLimitInformation{}
		limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
		if result, _, callErr := setInformationJobObject.Call(
			handle,
			jobObjectExtendedLimitInformationClass,
			uintptr(unsafe.Pointer(&limits)),
			unsafe.Sizeof(limits),
		); result == 0 {
			_, _, _ = ensureJobKernel32.NewProc("CloseHandle").Call(handle)
			ensureOwnershipErr = fmt.Errorf("configure ensure child job: %w", callErr)
			return
		}
		current, _, _ := getCurrentEnsureProcess.Call()
		if result, _, callErr := assignProcessToJobObject.Call(handle, current); result == 0 {
			_, _, _ = ensureJobKernel32.NewProc("CloseHandle").Call(handle)
			ensureOwnershipErr = fmt.Errorf("assign ensure child to job: %w", callErr)
			return
		}
		// Do not close this handle. On normal serve exit its runtime first
		// closes bridge resources; on forced termination Windows closes this
		// final handle and terminates only this job's descendants.
		ensureOwnershipJob = syscall.Handle(handle)
	})
	return ensureOwnershipErr
}
