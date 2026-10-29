//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	perfConvertSDDL = syscall.NewLazyDLL("advapi32.dll").NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	perfLocalFree   = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")
)

func createPerfFile(path string) (*os.File, error) {
	if strings.HasPrefix(path, `\\.\`) || strings.HasPrefix(path, `\\?\`) ||
		strings.Contains(strings.TrimPrefix(path, filepath.VolumeName(path)), ":") {
		return nil, errors.New("device paths and alternate data streams are not performance output files")
	}
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sddl, err := syscall.UTF16PtrFromString("D:P(A;;FA;;;OW)")
	if err != nil {
		return nil, err
	}
	var descriptor uintptr
	if ok, _, callErr := perfConvertSDDL.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0); ok == 0 {
		return nil, fmt.Errorf("build owner-only output ACL: %w", callErr)
	}
	defer perfLocalFree.Call(descriptor)
	attributes := syscall.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := syscall.CreateFile(path16, syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ, &attributes, syscall.CREATE_NEW, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("performance output is not a regular file")
	}
	return file, nil
}
