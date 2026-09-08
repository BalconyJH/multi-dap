//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const ensureCreateNewConsole = 0x00000010

type ensureProcess struct {
	cmd  *exec.Cmd
	done chan error
}

func startEnsureServe(request ensureServeRequest) (ensureChild, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args, err := request.args()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, args...)
	// Do not pipe stdio. serve recreates CONOUT$ before it launches
	// mpythonrun, whose WriteConsole use rejects pipes and NUL handles.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: ensureCreateNewConsole, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start serve: %w", err)
	}
	process := &ensureProcess{cmd: cmd, done: make(chan error, 1)}
	go func() { process.done <- cmd.Wait() }()
	return process, nil
}

func (p *ensureProcess) PID() int           { return p.cmd.Process.Pid }
func (p *ensureProcess) Done() <-chan error { return p.done }
func (p *ensureProcess) Kill() error        { return p.cmd.Process.Kill() }
