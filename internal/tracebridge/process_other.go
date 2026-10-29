//go:build !windows

package tracebridge

import "os/exec"

func configureProcess(command *exec.Cmd) {}

func ordinaryPath(path string) bool { return true }

func configureInheritance(command *exec.Cmd, handles []uintptr) {}

func validInheritedHandles(handles []uintptr) bool { return len(handles) == 0 }
