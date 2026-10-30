//go:build !windows

package daemon

type hostNativeSourceLease struct{ probeLock }

func inheritNativeSourceLock(lock, _ probeLock) (nativeSourceLease, error) {
	return &hostNativeSourceLease{probeLock: lock}, nil
}

func (*hostNativeSourceLease) InheritedHandles() []uintptr { return nil }
