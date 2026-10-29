//go:build windows

package tracebridge

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

var (
	jobKernel32       = syscall.NewLazyDLL("kernel32.dll")
	jobCreate         = jobKernel32.NewProc("CreateJobObjectW")
	jobSetInformation = jobKernel32.NewProc("SetInformationJobObject")
	jobAssign         = jobKernel32.NewProc("AssignProcessToJobObject")
)

type basicJobLimits struct {
	ProcessUserTime    int64
	JobUserTime        int64
	Flags              uint32
	MinimumWorkingSet  uintptr
	MaximumWorkingSet  uintptr
	ActiveProcessLimit uint32
	Affinity           uintptr
	PriorityClass      uint32
	SchedulingClass    uint32
}

type jobIOCounters struct {
	ReadOperations  uint64
	WriteOperations uint64
	OtherOperations uint64
	ReadBytes       uint64
	WriteBytes      uint64
	OtherBytes      uint64
}

type extendedJobLimits struct {
	Basic basicJobLimits
	// The Windows ABI pads the basic limits to eight bytes even in 32-bit Go.
	_                  [(8 - unsafe.Sizeof(basicJobLimits{})%8) % 8]byte
	IO                 jobIOCounters
	ProcessMemoryLimit uintptr
	JobMemoryLimit     uintptr
	PeakProcessMemory  uintptr
	PeakJobMemory      uintptr
}

type windowsProcessOwner struct {
	handle syscall.Handle
	once   sync.Once
	err    error
}

func newProcessOwner() (processOwner, error) {
	handle, _, _ := jobCreate.Call(0, 0)
	if handle == 0 {
		return nil, errors.New("tracebridge: job creation failed")
	}
	owner := &windowsProcessOwner{handle: syscall.Handle(handle)}
	limits := extendedJobLimits{}
	const killOnJobClose = 0x00002000
	const extendedLimitInformation = 9
	limits.Basic.Flags = killOnJobClose
	if result, _, _ := jobSetInformation.Call(
		handle, extendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits),
	); result == 0 {
		_ = owner.Close()
		return nil, errors.New("tracebridge: job configuration failed")
	}
	// A nil SECURITY_ATTRIBUTES pointer creates a non-inheritable handle.
	// Only the daemon retains it; its death closes the final handle and kills
	// the helper even when an FFI call prevents the helper from reading EOF.
	return owner, nil
}

func (o *windowsProcessOwner) Assign(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return errors.New("tracebridge: invalid owned process")
	}
	const processSetQuota = 0x0100
	const processTerminate = 0x0001
	// The original os.Process handle is still held, and no Wait/Release
	// goroutine has started. This PID cannot designate a reaped/reused child.
	handle, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(process.Pid))
	if err != nil {
		return errors.New("tracebridge: owned process handle unavailable")
	}
	defer syscall.CloseHandle(handle)
	if result, _, _ := jobAssign.Call(uintptr(o.handle), uintptr(handle)); result == 0 {
		return errors.New("tracebridge: job assignment failed")
	}
	return nil
}

func (o *windowsProcessOwner) Close() error {
	o.once.Do(func() { o.err = syscall.CloseHandle(o.handle) })
	return o.err
}
