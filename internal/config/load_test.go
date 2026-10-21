package config

import (
	"context"
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

func TestLoadRejectsExplicitZeroPythonVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(content), `executable = "multi/multi.exe"`, "executable = \"multi/multi.exe\"\npython_version = 0", 1)
	writeConfig(t, path, updated)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "multi.python_version") {
		t.Fatalf("Load() error = %v, want explicit python version validation error", err)
	}
}

func TestLoadAllowsExecutableSymlinkWithinInstallation(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	install := filepath.Join(dir, "multi")
	if err := os.WriteFile(filepath.Join(install, "mpythonrun-real.exe"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(install, "mpythonrun.exe")
	if err := os.Symlink(filepath.Join(install, "mpythonrun-real.exe"), linked); err != nil {
		t.Skipf("symbolic links are unavailable in this test environment: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(content), `executable = "multi/multi.exe"`, `executable = "multi/mpythonrun.exe"`, 1)
	writeConfig(t, path, updated)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load(in-install executable symlink) error = %v", err)
	}
}

func TestLoadContextHonorsCancellationBeforeProbe(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadContext(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadContext(canceled) error = %v, want context.Canceled", err)
	}
}

func TestLoadAutoDetectsAndChecksMultiVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "multi", ".about"), []byte("product = \"MULTI IDE\"\nversion = 0x1c5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "multi", "defaults", "licensing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "multi", "defaults", "licensing", "install_info.txt"), []byte("Product Release Name: MULTI IDE 8.1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(auto version) error = %v", err)
	}
	if got.Multi.Version != "8.1.4" || got.Multi.VersionSource != MultiVersionSourceInstallInfo {
		t.Fatalf("auto-detected MULTI version = %#v", got.Multi)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	matching := strings.Replace(string(content), `executable = "multi/multi.exe"`, "executable = \"multi/multi.exe\"\nversion = \"8.1.4\"", 1)
	writeConfig(t, path, matching)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load(matching explicit version) error = %v", err)
	}

	mismatching := strings.Replace(matching, `version = "8.1.4"`, `version = "7.1.6"`, 1)
	writeConfig(t, path, mismatching)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "multi.version") || !strings.Contains(err.Error(), "requested") {
		t.Fatalf("Load(mismatching explicit version) error = %v", err)
	}

	// A qualified release is not a prefix-compatible match. In particular,
	// beta builds must not silently select the stable 8.1.4 installation.
	beta := strings.Replace(matching, `version = "8.1.4"`, `version = "8.1.4beta"`, 1)
	writeConfig(t, path, beta)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "multi.version") || !strings.Contains(err.Error(), "requested") {
		t.Fatalf("Load(beta explicit version) error = %v, want exact release mismatch", err)
	}
}

func TestLoadExplicitAutoRequiresEvidence(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(content), `executable = "multi/multi.exe"`, "executable = \"multi/multi.exe\"\nversion = \"auto\"", 1)
	writeConfig(t, path, updated)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "multi.version") || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("Load(explicit auto without evidence) error = %v", err)
	}
}

func TestLoadAllowsOmittedVersionWhenOnlyProductMarkerExists(t *testing.T) {
	dir := t.TempDir()
	path := writeValidConfig(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "multi", ".about"), []byte("product = \"MULTI IDE\"\nversion = 0x1c5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(product-only marker) error = %v", err)
	}
	if got.Multi.Version != "" || got.Multi.VersionSource != "" {
		t.Fatalf("product-only marker resolved version = %#v, want unknown", got.Multi)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(content), `executable = "multi/multi.exe"`, "executable = \"multi/multi.exe\"\nversion = \"auto\"", 1)
	writeConfig(t, path, updated)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "multi.version") {
		t.Fatalf("Load(explicit auto with product-only marker) error = %v, want multi.version failure", err)
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
