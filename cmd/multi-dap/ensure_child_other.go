//go:build !windows

package main

import (
	"os"
	"os/exec"
)

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
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &ensureProcess{cmd: cmd, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
}
func (p *ensureProcess) PID() int           { return p.cmd.Process.Pid }
func (p *ensureProcess) Done() <-chan error { return p.done }
func (p *ensureProcess) Kill() error        { return p.cmd.Process.Kill() }
