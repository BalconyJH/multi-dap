//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	consoleKernel32  = syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode   = consoleKernel32.NewProc("GetConsoleMode")
	allocConsole     = consoleKernel32.NewProc("AllocConsole")
	consoleUser32    = syscall.NewLazyDLL("user32.dll")
	getConsoleWindow = consoleKernel32.NewProc("GetConsoleWindow")
	showWindow       = consoleUser32.NewProc("ShowWindow")
)

const winErrorAccessDenied = syscall.Errno(5)

func prepareBridgeConsole() error {
	if hasConsoleHandle(os.Stdout) && hasConsoleHandle(os.Stderr) {
		return nil
	}
	if result, _, callErr := allocConsole.Call(); result == 0 && !errors.Is(callErr, winErrorAccessDenied) {
		return fmt.Errorf("allocate console for MULTI bridge: %w", callErr)
	}
	output, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open console output for MULTI bridge: %w", err)
	}
	os.Stdout = output
	os.Stderr = output
	if window, _, _ := getConsoleWindow.Call(); window != 0 {
		_, _, _ = showWindow.Call(window, 0)
	}
	return nil
}

func hasConsoleHandle(file *os.File) bool {
	if file == nil {
		return false
	}
	var mode uint32
	result, _, _ := getConsoleMode.Call(file.Fd(), uintptr(unsafe.Pointer(&mode)))
	return result != 0
}
