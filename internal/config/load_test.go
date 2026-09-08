package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidatesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := filepath.Join(dir, "multi", "multi.exe"); got.Multi.Executable != want {
		t.Errorf("Multi.Executable = %q, want %q", got.Multi.Executable, want)
	}
	if want := filepath.Join(dir, "project.gpj"); got.Connection.Project != want {
		t.Errorf("Connection.Project = %q, want %q", got.Connection.Project, want)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	appendConfig(t, path, "\nextra_setting = true\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "decode configuration") || !strings.Contains(err.Error(), "unknown fields") {
		t.Fatalf("Load() error = %v, want strict unknown-field error", err)
	}
}

func TestLoadRejectsInvalidTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.toml")
	writeConfig(t, path, "[multi\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "invalid TOML") || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("Load() error = %v, want TOML decoding error", err)
	}
}

func TestLoadRejectsTrailingDocumentData(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	appendConfig(t, path, "\nnot-a-second-document\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "invalid TOML") {
		t.Fatalf("Load() error = %v, want trailing-data error", err)
	}
}

func TestLoadRejectsMissingDirectoryAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.toml")
	if _, err := Load(missing); err == nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "stat configuration") {
		t.Errorf("Load(missing) error = %v, want stat error wrapping ErrNotExist", err)
	}

	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Errorf("Load(directory) error = %v, want regular-file error", err)
	}

	oversized := filepath.Join(dir, "large.toml")
	writeConfig(t, oversized, strings.Repeat("#", maxConfigFileSize+1))
	if _, err := Load(oversized); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("Load(oversized) error = %v, want size limit error", err)
	}
}

func TestLoadWrapsValidationError(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, strings.Replace(string(content), `arguments = "-serial"`, `arguments = ""`, 1))

	_, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "validate configuration") || !strings.Contains(err.Error(), "connection.arguments") {
		t.Fatalf("Load() error = %v, want validation error", err)
	}
}

func TestLoadNormalizesConnectionPreparation(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withPreparation := strings.Replace(string(content), `arguments = "-serial"`, "arguments = \"-serial\"\npreparation = \"none\"", 1)
	writeConfig(t, path, withPreparation)
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Connection.Preparation != ConnectionPreparationNone {
		t.Fatalf("preparation = %q, want none", got.Connection.Preparation)
	}

	invalid := strings.Replace(withPreparation, `preparation = "none"`, `preparation = "download"`, 1)
	writeConfig(t, path, invalid)
	_, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "connection.preparation") {
		t.Fatalf("Load() error = %v, want connection.preparation", err)
	}
}

func writeValidConfig(t *testing.T, dir string) string {
	t.Helper()
	for _, path := range []string{
		filepath.Join(dir, "multi", "multi.exe"),
		filepath.Join(dir, "project.gpj"),
		filepath.Join(dir, "app.elf"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(dir, "project.toml")
	writeConfig(t, path, `
[multi]
installation = "multi"
executable = "multi/multi.exe"

[connection]
project = "project.gpj"
arguments = "-serial"

[[cores]]
id = 0
elf = "app.elf"

[endpoints.mbp]
host = "127.0.0.1"
port = 0

[endpoints.dap]
host = "127.0.0.1"
port = 0

[endpoints.hint]
host = "127.0.0.1"
port = 0

[timing]
poll_cadence = "100ms"
rpc_deadline = "1s"
startup_deadline = "5s"

[probe]
id = "probe-1"
`)
	return path
}

func appendConfig(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
