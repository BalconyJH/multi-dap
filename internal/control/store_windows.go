//go:build windows

package control

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var replaceFileW = controlKernel32.NewProc("ReplaceFileW")

const (
	daclSecurityInformation          = 0x00000004
	protectedDaclSecurityInformation = 0x80000000
	errorSharingViolation            = syscall.Errno(32)
)

var (
	advapi32        = syscall.NewLazyDLL("advapi32.dll")
	convertSDDL     = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	setFileSecurity = advapi32.NewProc("SetFileSecurityW")
	localFree       = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
)

// OWNER RIGHTS has no implicit inheritance from a permissive parent. The
// protected DACL grants full access solely to the account that created the
// record directory/file; another local account cannot read the token.
func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("control: create record directory: %w", err)
	}
	return applyOwnerOnlyDACL(path)
}

func secureFile(path string) error { return applyOwnerOnlyDACL(path) }

func openStoreFile(path string) (*os.File, error) {
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := syscall.CreateFile(
		path16,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func isTransientStoreReadError(err error) bool {
	return errors.Is(err, errorSharingViolation)
}

func replaceFile(source, target string) error {
	source16, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	target16, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(target16)),
		uintptr(unsafe.Pointer(source16)),
		0,
		0,
		0,
		0,
	); result == 0 {
		return callErr
	}
	return nil
}

func applyOwnerOnlyDACL(path string) error {
	sddl, err := syscall.UTF16PtrFromString("D:P(A;;FA;;;OW)")
	if err != nil {
		return fmt.Errorf("control: encode record ACL: %w", err)
	}
	var descriptor uintptr
	if ok, _, callErr := convertSDDL.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0); ok == 0 {
		return fmt.Errorf("control: build record ACL: %w", callErr)
	}
	defer localFree.Call(descriptor)
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("control: encode record path: %w", err)
	}
	if ok, _, callErr := setFileSecurity.Call(uintptr(unsafe.Pointer(name)), daclSecurityInformation|protectedDaclSecurityInformation, descriptor); ok == 0 {
		return fmt.Errorf("control: restrict record ACL: %w", callErr)
	}
	return nil
}
