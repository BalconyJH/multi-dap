//go:build windows

package daemon

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestNativeHelperLeaseAlsoPinsConfiguredProbe(t *testing.T) {
	probeID := fmt.Sprintf("native-helper-probe-fixture-%d", os.Getpid())
	probe, err := acquireProbeLock(probeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close() })
	lease, err := acquireNativeTraceSourceForProbe(context.Background(), uint32(os.Getpid())+2000000, probe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if handles := lease.(nativeSourceLease).InheritedHandles(); len(handles) != 2 || handles[0] == handles[1] {
		t.Fatal("helper did not receive separate source and probe handles")
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if other, err := acquireProbeLock(probeID); err == nil {
		_ = other.Close()
		t.Fatal("native lifetime released its configured probe prematurely")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := acquireProbeLock(probeID)
	if err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
}
