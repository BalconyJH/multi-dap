//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestPerfFileHasOwnerOnlyProtectedDACLFromCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	file, err := createPerfFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	getSecurity := syscall.NewLazyDLL("advapi32.dll").NewProc("GetFileSecurityW")
	toSDDL := syscall.NewLazyDLL("advapi32.dll").NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
	const dacl = 4
	var required uint32
	_, _, _ = getSecurity.Call(uintptr(unsafe.Pointer(path16)), dacl, 0, 0, uintptr(unsafe.Pointer(&required)))
	if required == 0 {
		t.Fatal("GetFileSecurityW did not provide a descriptor size")
	}
	descriptor := make([]byte, required)
	if ok, _, err := getSecurity.Call(uintptr(unsafe.Pointer(path16)), dacl, uintptr(unsafe.Pointer(&descriptor[0])), uintptr(len(descriptor)), uintptr(unsafe.Pointer(&required))); ok == 0 {
		t.Fatalf("GetFileSecurityW: %v", err)
	}
	var sddl *uint16
	var length uint32
	if ok, _, err := toSDDL.Call(uintptr(unsafe.Pointer(&descriptor[0])), 1, dacl, uintptr(unsafe.Pointer(&sddl)), uintptr(unsafe.Pointer(&length))); ok == 0 {
		t.Fatalf("ConvertSecurityDescriptorToStringSecurityDescriptorW: %v", err)
	}
	defer perfLocalFree.Call(uintptr(unsafe.Pointer(sddl)))
	if length == 0 || length > 4096 {
		t.Fatalf("SDDL size = %d", length)
	}
	text := syscall.UTF16ToString(unsafe.Slice(sddl, int(length)))
	if text != "D:P(A;;FA;;;OW)" {
		t.Fatalf("performance file DACL = %q, want protected owner-only full access", text)
	}
}

func TestPerfFileRejectsWindowsDevicePathsAndAlternateStreams(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	if err := os.WriteFile(base, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		`\\.\NUL`, `\\.\pipe\multi-dap-perf-test`, `\\?\C:\trace.json`, base + ":trace.json",
	} {
		if file, err := createPerfFile(path); err == nil {
			file.Close()
			t.Fatalf("device/stream path %q accepted", path)
		} else if !strings.Contains(err.Error(), "device paths and alternate data streams") {
			t.Fatalf("device/stream path %q was not rejected before CreateFile: %v", path, err)
		}
	}
	data, err := os.ReadFile(base)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("base file changed: %q, %v", data, err)
	}
}
