//go:build windows

package daemon

import (
	"errors"
	"sync"
	"syscall"
	"unsafe"
)

var duplicateTraceHandle = kernel32.NewProc("DuplicateHandle")

type windowsNativeSourceLease struct {
	lock      probeLock
	inherited []syscall.Handle
	once      sync.Once
	err       error
}

func inheritNativeSourceLock(lock, probe probeLock) (nativeSourceLease, error) {
	lease := &windowsNativeSourceLease{lock: lock}
	for _, selected := range []probeLock{lock, probe} {
		if selected == nil {
			continue
		}
		duplicate, err := duplicateNativeLock(selected)
		if err != nil {
			for _, handle := range lease.inherited {
				_ = syscall.CloseHandle(handle)
			}
			return nil, err
		}
		lease.inherited = append(lease.inherited, duplicate)
	}
	return lease, nil
}

func duplicateNativeLock(lock probeLock) (syscall.Handle, error) {
	owned, ok := lock.(*windowsProbeLock)
	if !ok || owned.semaphore == 0 {
		return 0, errors.New("native source lock cannot be inherited")
	}
	process, _, _ := getCurrentProcess.Call()
	var duplicate syscall.Handle
	const duplicateSameAccess = 0x00000002
	okay, _, err := duplicateTraceHandle.Call(
		process, uintptr(owned.semaphore), process, uintptr(unsafe.Pointer(&duplicate)),
		0, 1, duplicateSameAccess,
	)
	if okay == 0 {
		return 0, err
	}
	return duplicate, nil
}

func (l *windowsNativeSourceLease) InheritedHandles() []uintptr {
	handles := make([]uintptr, len(l.inherited))
	for index, handle := range l.inherited {
		handles[index] = uintptr(handle)
	}
	return handles
}

func (l *windowsNativeSourceLease) Close() error {
	l.once.Do(func() {
		for _, handle := range l.inherited {
			l.err = errors.Join(l.err, syscall.CloseHandle(handle))
		}
		l.err = errors.Join(l.err, l.lock.Close())
	})
	return l.err
}
