//go:build windows

package bridge

import "testing"

func TestSecureStartupWorkspaceAppliesOwnerOnlyACL(t *testing.T) {
	if err := secureStartupWorkspace(t.TempDir()); err != nil {
		t.Fatalf("secureStartupWorkspace() error = %v", err)
	}
}
