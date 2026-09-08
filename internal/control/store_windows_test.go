//go:build windows

package control

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestLoadClassifiesWindowsSharingViolationAsRecordTransition(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.recordPath("probe-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(
		path16,
		syscall.GENERIC_READ,
		0,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)

	if _, err := store.Load("probe-1"); !errors.Is(err, ErrRecordTransition) {
		t.Fatalf("Load() error = %v, want ErrRecordTransition", err)
	}
}
