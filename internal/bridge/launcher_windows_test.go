//go:build windows

package bridge

import (
	"os"
	"testing"
)

func TestWindowsCommandUsesNewConsoleAndInheritedHandles(t *testing.T) {
	command, err := (LaunchSpec{Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123}).Command()
	if err != nil {
		t.Fatal(err)
	}
	if command.SysProcAttr == nil || command.SysProcAttr.CreationFlags&createNewConsole == 0 {
		t.Fatalf("Command() creation flags = %#v, want CREATE_NEW_CONSOLE", command.SysProcAttr)
	}
	if command.Stdin != os.Stdin || command.Stdout != os.Stdout || command.Stderr != os.Stderr {
		t.Fatal("Command() redirected a standard handle instead of inheriting it")
	}
}
