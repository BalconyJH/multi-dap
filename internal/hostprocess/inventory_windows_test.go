//go:build windows

package hostprocess

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInventoryActualOwnedChild(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestInventoryOwnedChildHelper$")
	command.Env = append(os.Environ(), "MULTI_DAP_HOSTPROCESS_HELPER=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if err := command.Wait(); err != nil {
			t.Errorf("owned harmless child exit: %v", err)
		}
	})
	ready, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("child readiness = %q, %v", ready, err)
	}
	report := Collect(context.Background(), filepath.Dir(executable))
	if report.State != StateComplete && report.State != StatePartial {
		t.Fatalf("actual inventory unavailable: %#v", report)
	}
	for _, entry := range report.Processes {
		if entry.PID == uint32(command.Process.Pid) {
			if entry.ParentPID != uint32(os.Getpid()) || entry.Match != MatchInstallationPath {
				t.Fatalf("owned child metadata = %#v", entry)
			}
			actual, actualOK := windowsPath(entry.ImagePath)
			want, wantOK := windowsPath(executable)
			if !actualOK || !wantOK || !strings.EqualFold(actual, want) {
				t.Fatalf("owned child executable differs from launched file")
			}
			return
		}
	}
	t.Fatalf("owned child missing: state=%s reason=%s scanned=%d unresolved=%d",
		report.State, report.Reason, report.Scanned, report.Unresolved)
}

func TestInventoryOwnedChildHelper(t *testing.T) {
	if os.Getenv("MULTI_DAP_HOSTPROCESS_HELPER") != "1" {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestInventoryStrictUTF16(t *testing.T) {
	tests := []struct {
		value      []uint16
		terminated bool
		valid      bool
	}{
		{[]uint16{'a', 0, 'b'}, true, true},
		{[]uint16{'a'}, false, true},
		{[]uint16{0xD83D, 0xDE00}, false, true},
		{[]uint16{'a'}, true, false},
		{[]uint16{'a', 0, 'b'}, false, false},
		{[]uint16{0xD83D}, false, false},
		{[]uint16{0xDE00}, false, false},
		{[]uint16{0xD83D, 'a'}, false, false},
	}
	for _, test := range tests {
		if _, valid := strictUTF16(test.value, test.terminated); valid != test.valid {
			t.Fatalf("UTF-16 %x terminated=%v valid=%v", test.value, test.terminated, valid)
		}
	}
}

func TestInventoryRejectsProcessBornAfterSnapshotCutoff(t *testing.T) {
	snapshot := &windowsSnapshot{cutoff: 0}
	if image, err := snapshot.image(snapshotEntry{pid: uint32(os.Getpid())}); err == nil || image != "" {
		t.Fatalf("post-cutoff process accepted: image=%q err=%v", image, err)
	}
}
