//go:build windows

package bridge

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	daclSecurityInformation          = 0x00000004
	protectedDaclSecurityInformation = 0x80000000
)

var (
	bridgeAdvapi32        = syscall.NewLazyDLL("advapi32.dll")
	bridgeConvertSDDL     = bridgeAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	bridgeSetFileSecurity = bridgeAdvapi32.NewProc("SetFileSecurityW")
	bridgeLocalFree       = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
)

// secureStartupWorkspace protects the ready-file/console directory from the
// permissive ACL that may be inherited from the system temporary directory.
// OWNER RIGHTS does not implicitly include a permissive parent ACE.
func secureStartupWorkspace(path string) error {
	sddl, err := syscall.UTF16PtrFromString("D:P(A;;FA;;;OW)")
	if err != nil {
		return fmt.Errorf("encode startup workspace ACL: %w", err)
	}
	var descriptor uintptr
	if ok, _, callErr := bridgeConvertSDDL.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0); ok == 0 {
		return fmt.Errorf("build startup workspace ACL: %w", callErr)
	}
	defer bridgeLocalFree.Call(descriptor)
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode startup workspace path: %w", err)
	}
	if ok, _, callErr := bridgeSetFileSecurity.Call(uintptr(unsafe.Pointer(name)), daclSecurityInformation|protectedDaclSecurityInformation, descriptor); ok == 0 {
		return fmt.Errorf("restrict startup workspace ACL: %w", callErr)
	}
	return nil
}
