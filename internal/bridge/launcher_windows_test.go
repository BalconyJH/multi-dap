//go:build windows

package bridge

import (
	"os"
	"strings"
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

func TestWindowsCommandPinsPythonRuntimeEnvironment(t *testing.T) {
	// Seed a differently-cased inherited value to verify replacement is
	// case-insensitive on Windows rather than producing duplicate entries.
	t.Setenv("ghs_ide_use_python3", "inherited")
	for _, test := range []struct {
		name    string
		version int
		want    string
	}{
		{name: "default Python 3", version: 0, want: "1"},
		{name: "explicit Python 2", version: PythonVersion2, want: "0"},
		{name: "explicit Python 3", version: PythonVersion3, want: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, err := (LaunchSpec{Executable: "mpythonrun.exe", BridgeScript: "bridge.py", RPCPort: 43123, PythonVersion: test.version}).Command()
			if err != nil {
				t.Fatal(err)
			}
			const variable = "GHS_IDE_USE_PYTHON3"
			var values []string
			for _, entry := range command.Env {
				separator := strings.IndexByte(entry, '=')
				if separator > 0 && strings.EqualFold(entry[:separator], variable) {
					values = append(values, entry[separator+1:])
				}
			}
			if len(values) != 1 || values[0] != test.want {
				t.Fatalf("Command() %s environment entries = %#v, want [%q]", variable, values, test.want)
			}
		})
	}
}
