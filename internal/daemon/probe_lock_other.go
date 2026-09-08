//go:build !windows

package daemon

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// The production daemon is Windows-only, but a handle-backed advisory lock
// keeps package tests and static analysis correct on other development hosts.
// The file's PID text is diagnostic metadata only: flock, released by the OS
// when this handle dies, is the actual exclusion mechanism.
type fileProbeLock struct {
	file     *os.File
	once     sync.Once
	closeErr error
}

func acquireProbeLock(probeID string) (probeLock, error) {
	digest := sha256.Sum256([]byte(probeID))
	directory := filepath.Join(os.TempDir(), "multi-dap")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("daemon: create probe lock directory: %w", err)
	}
	path := filepath.Join(directory, fmt.Sprintf("probe-%x.lock", digest[:]))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open probe lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		data, _ := os.ReadFile(path)
		_ = file.Close()
		pid, _ := strconv.ParseUint(string(data), 10, 32)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &ProbeInUseError{ProbeID: probeID, HolderPID: uint32(pid)}
		}
		return nil, fmt.Errorf("daemon: acquire probe lock: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("daemon: clear probe lock metadata: %w", err)
	}
	if _, err := file.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("daemon: write probe lock metadata: %w", err)
	}
	return &fileProbeLock{file: file}, nil
}

func (l *fileProbeLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.once.Do(func() {
		l.closeErr = l.file.Close()
	})
	return l.closeErr
}
