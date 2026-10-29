//go:build !windows

package tracebridge

import "testing"

func TestNonWindowsCannotAcceptWindowsInheritedHandles(t *testing.T) {
	if validInheritedHandles([]uintptr{1}) || !validInheritedHandles(nil) {
		t.Fatal("Windows handle inheritance must fail closed on other hosts")
	}
}
