package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The detector executes only files explicitly named gversion.exe/gversion.
// Reuse the Go test binary as a tiny deterministic helper so probe tests do
// not depend on a shell, a platform-specific script format, or a real MULTI
// installation. TestMain handles the helper mode before testing flags are
// interpreted by the test suite.
func TestMain(m *testing.M) {
	if os.Getenv("MULTI_DAP_CONFIG_GVERSION_HELPER") == "1" {
		if marker := os.Getenv("MULTI_DAP_CONFIG_GVERSION_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("ran\n"), 0o600)
		}
		if os.Getenv("MULTI_DAP_CONFIG_GVERSION_FAIL") == "1" {
			os.Exit(7)
		}
		output := os.Stdout
		if os.Getenv("MULTI_DAP_CONFIG_GVERSION_STDERR") == "1" {
			output = os.Stderr
		}
		_, _ = fmt.Fprintln(output, "Green Hills Software, MULTI v8.1.4")
		if os.Getenv("MULTI_DAP_CONFIG_GVERSION_BANNER_THEN_FAIL") == "1" {
			os.Exit(7)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNormalizeMultiVersion(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "8.1.4", want: "8.1.4"},
		{raw: " v8.1.4 ", want: "8.1.4"},
		{raw: "7.1.6d", want: "7.1.6d"},
		{raw: "8.1.4-R1", want: "8.1.4-r1"},
	} {
		got, err := NormalizeMultiVersion(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("NormalizeMultiVersion(%q) = (%q, %v), want (%q, nil)", test.raw, got, err, test.want)
		}
	}
	for _, raw := range []string{"", "auto", "8", "8.x.4", "8.1.4/evil"} {
		if _, err := NormalizeMultiVersion(raw); err == nil {
			t.Fatalf("NormalizeMultiVersion(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestEquivalentMultiVersionAllowsGVersionWithoutReleaseSuffix(t *testing.T) {
	if !EquivalentMultiVersion("7.1.6", "7.1.6d") || !EquivalentMultiVersion("7.1.6D", "7.1.6") {
		t.Fatal("numeric gversion and suffixed install metadata should compare equal")
	}
	if EquivalentMultiVersion("7.1.6d", "7.1.6r") || EquivalentMultiVersion("7.1.6", "7.1.7") || EquivalentMultiVersion("8.1.4", "8.1.4beta") || EquivalentMultiVersion("8.1.4", "8.1.4-r1") || EquivalentMultiVersion("8.1.4", "8.1.4d") {
		t.Fatal("different MULTI releases should not compare equal")
	}
}

func TestParseMultiVersionEvidence(t *testing.T) {
	if got, ok := parseMultiBanner([]byte("Green Hills Software, MULTI v8.1.4\nRelease Date\n")); !ok || got != "8.1.4" {
		t.Fatalf("parseMultiBanner() = (%q, %v)", got, ok)
	}
	product, version, ok := parseReleaseMetadata([]byte("Product Release Name: MULTI IDE 7.1.6d\n"))
	if !ok || product != "MULTI IDE" || version != "7.1.6d" {
		t.Fatalf("parseReleaseMetadata() = (%q, %q, %v)", product, version, ok)
	}
	if _, _, ok := parseReleaseMetadata([]byte("Product Release Name: Compiler 2024.1.4\n")); ok {
		t.Fatal("Compiler metadata was accepted as MULTI evidence")
	}
	for _, fixture := range []string{
		"Product Release Name: MULTI IDE 8.1.4/garbage\n",
		"prefix MULTI IDE 8.1.4 suffix\n",
		"Product Release Name: Compiler 2024.1.4\n",
	} {
		if _, _, ok := parseReleaseMetadata([]byte(fixture)); ok {
			t.Fatalf("malformed or non-MULTI metadata was accepted: %q", fixture)
		}
	}
	if _, ok := parseTrustedMultiBanner([]byte("Green Hills Software, MULTI v8.1.4/garbage\n")); ok {
		t.Fatal("malformed gversion banner was accepted")
	}
	if _, ok := parseTrustedMultiBanner([]byte("noise: Green Hills Software, MULTI v8.1.4\n")); ok {
		t.Fatal("gversion banner with an unrelated prefix was accepted")
	}
}

func TestNormalizeMultiVersionBoundsInput(t *testing.T) {
	tooLong := "8.1.4" + strings.Repeat("x", maxMultiVersionLength)
	if _, err := NormalizeMultiVersion(tooLong); err == nil {
		t.Fatal("overlong MULTI version was accepted")
	}
}

func TestDetectMultiVersionUsesInstallMetadataWithoutStartingMULTI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".about"), []byte("product = \"MULTI IDE\"\nversion = 0x1c5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "defaults", "licensing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "defaults", "licensing", "install_info.txt"), []byte("Product Release Name: MULTI IDE 8.1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DetectMultiVersion(context.Background(), root)
	if err != nil {
		t.Fatalf("DetectMultiVersion() error = %v", err)
	}
	if got.Product != "MULTI IDE" || got.Version != "8.1.4" || got.Source != MultiVersionSourceInstallInfo {
		t.Fatalf("DetectMultiVersion() = %#v", got)
	}
}

func TestResolveMultiVersionChecksExplicitConstraint(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "install_summary.txt"), []byte("Installing \"MULTI IDE 8.1.4\" to \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveMultiVersion(context.Background(), root, "v8.1.4")
	if err != nil || got.Version != "8.1.4" {
		t.Fatalf("matching ResolveMultiVersion() = (%#v, %v)", got, err)
	}
	_, err = ResolveMultiVersion(context.Background(), root, "7.1.6")
	if err == nil || !errors.Is(err, ErrMultiVersionMismatch) || !strings.Contains(err.Error(), "requested \"7.1.6\"") {
		t.Fatalf("mismatching ResolveMultiVersion() error = %v", err)
	}
}

func TestDetectMultiVersionDoesNotSilentlyAcceptCompiler(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".about"), []byte("product = \"Compiler\"\nversion = 0x961\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := DetectMultiVersion(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "not MULTI IDE") {
		t.Fatalf("compiler detection error = %v", err)
	}
}

func TestDetectMultiVersionRejectsOversizedMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "install_summary.txt"), []byte(strings.Repeat("x", maxMultiVersionMetadataSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DetectMultiVersion(context.Background(), root); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized metadata error = %v, want size-limit failure", err)
	}
}

func TestResolveMultiVersionRejectsInvalidExplicitExecutable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "install_summary.txt"), []byte("Installing \"MULTI IDE 8.1.4\" to \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "other.exe")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, executable := range []string{filepath.Join(root, "missing.exe"), outside} {
		if _, err := ResolveMultiVersion(context.Background(), root, "8.1.4", executable); err == nil || !strings.Contains(err.Error(), "executable") {
			t.Fatalf("ResolveMultiVersion(%q) error = %v, want explicit executable validation", executable, err)
		}
	}
}

func TestResolveMultiVersionAllowsInInstallationExecutableSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "install_summary.txt"), []byte("Installing \"MULTI IDE 8.1.4\" to \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realExecutable := filepath.Join(root, "mpythonrun-real.exe")
	if err := os.WriteFile(realExecutable, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedExecutable := filepath.Join(root, "mpythonrun.exe")
	if err := os.Symlink(realExecutable, linkedExecutable); err != nil {
		t.Skipf("symbolic links are unavailable in this test environment: %v", err)
	}
	got, err := ResolveMultiVersion(context.Background(), root, "8.1.4", linkedExecutable)
	if err != nil {
		t.Fatalf("ResolveMultiVersion(in-install symlink) error = %v", err)
	}
	if got.Version != "8.1.4" {
		t.Fatalf("ResolveMultiVersion(in-install symlink) = %#v", got)
	}
}

func TestResolveMultiVersionRejectsExecutableSymlinkOutsideInstallation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "install_summary.txt"), []byte("Installing \"MULTI IDE 8.1.4\" to \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.exe")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedExecutable := filepath.Join(root, "mpythonrun.exe")
	if err := os.Symlink(outside, linkedExecutable); err != nil {
		t.Skipf("symbolic links are unavailable in this test environment: %v", err)
	}
	if _, err := ResolveMultiVersion(context.Background(), root, "8.1.4", linkedExecutable); err == nil || !strings.Contains(err.Error(), "resolved executable") {
		t.Fatalf("ResolveMultiVersion(out-of-install symlink) error = %v, want resolved-target rejection", err)
	}
}

func TestVersionTargetResolvesAncestorLinks(t *testing.T) {
	for _, test := range []struct {
		name               string
		outside            bool
		linkedInstallation bool
	}{
		{name: "inside ancestor"},
		{name: "escaping ancestor", outside: true},
		{name: "linked installation", linkedInstallation: true},
		{name: "escaping ancestor in linked installation", outside: true, linkedInstallation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			installation := filepath.Join(root, "installation")
			targetDirectory := filepath.Join(installation, "real-bin")
			if test.outside {
				targetDirectory = filepath.Join(root, "outside")
			}
			for _, directory := range []string{installation, targetDirectory} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			executable := filepath.Join(targetDirectory, "mpythonrun.exe")
			if err := os.WriteFile(executable, []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(targetDirectory, filepath.Join(installation, "bin")); err != nil {
				t.Skipf("symbolic links are unavailable in this test environment: %v", err)
			}
			if test.linkedInstallation {
				alias := filepath.Join(root, "installation-alias")
				if err := os.Symlink(installation, alias); err != nil {
					t.Fatal(err)
				}
				installation = alias
			}
			got, configured, err := versionTarget(installation, filepath.Join("bin", "mpythonrun.exe"))
			if !configured {
				t.Fatal("explicit executable was not recognized")
			}
			if test.outside {
				if err == nil || !strings.Contains(err.Error(), "resolved executable") {
					t.Fatalf("versionTarget() error = %v, want resolved-target rejection", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("versionTarget() error = %v", err)
			}
			want, err := filepath.EvalSymlinks(executable)
			if err != nil {
				t.Fatal(err)
			}
			want, err = filepath.Abs(want)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("versionTarget() = %q, want resolved path %q", got, want)
			}
		})
	}
}

func TestResolveMultiVersionHonorsContextCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "install_summary.txt"), []byte("Installing \"MULTI IDE 8.1.4\" to \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveMultiVersion(ctx, root, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveMultiVersion(canceled) error = %v, want context.Canceled", err)
	}
}

func TestTrustedGVersionBannerRequiresVendorMarker(t *testing.T) {
	if _, ok := parseTrustedMultiBanner([]byte("MULTI v8.1.4\n")); ok {
		t.Fatal("untrusted MULTI banner was accepted as gversion evidence")
	}
	if got, ok := parseTrustedMultiBanner([]byte("Green Hills Software, MULTI v8.1.4\n")); !ok || got != "8.1.4" {
		t.Fatalf("trusted gversion banner = (%q, %v)", got, ok)
	}
}

func TestTrustedGVersionBannerAcceptsExactTargetPrefix(t *testing.T) {
	target := filepath.Join(t.TempDir(), "multi.exe")
	data := []byte(target + ": Green Hills Software, MULTI v8.1.4  \n")
	if got, ok := parseTrustedMultiBanner(data, target); !ok || got != "8.1.4" {
		t.Fatalf("target-prefixed gversion banner = (%q, %v)", got, ok)
	}
	wrongTarget := filepath.Join(t.TempDir(), "other.exe")
	if _, ok := parseTrustedMultiBanner(data, wrongTarget); ok {
		t.Fatal("gversion banner with a different target prefix was accepted")
	}
}

func TestTrustedGVersionBannerHandlesUnicodeTargetPrefix(t *testing.T) {
	// U+0130 has a different UTF-8 byte length after Unicode lower-casing.
	// The parser must keep the marker index in the original byte string and
	// must not panic or accidentally inspect a shifted suffix.
	target := filepath.Join(t.TempDir(), "\u0130-multi.exe")
	data := []byte(target + ": Green Hills Software, MULTI v8.1.4\n")
	if got, ok := parseTrustedMultiBanner(data, target); !ok || got != "8.1.4" {
		t.Fatalf("Unicode target-prefixed gversion banner = (%q, %v)", got, ok)
	}
}

func TestDetectMultiVersionAllowsLegacyMetadataFreeFixtureOnlyAsUnknownThroughLoad(t *testing.T) {
	root := t.TempDir()
	_, err := DetectMultiVersion(context.Background(), root)
	if err == nil || !errors.Is(err, ErrMultiVersionUnavailable) {
		t.Fatalf("metadata-free detector error = %v, want unavailable", err)
	}
	var probeErr *MultiVersionError
	if !errors.As(err, &probeErr) || probeErr.MarkerFound {
		t.Fatalf("metadata-free detector error = %#v, want marker-free probe error", probeErr)
	}
	optional, err := ResolveMultiVersionWithOptions(context.Background(), ResolveOptions{Installation: root})
	if err != nil {
		t.Fatalf("optional metadata-free resolver error = %v", err)
	}
	if optional.Version != "" || optional.Source != MultiVersionSourceUnknown {
		t.Fatalf("optional metadata-free resolver = %#v, want unknown source", optional)
	}
}

func TestProbeMultiVersionReportsGVersionFailure(t *testing.T) {
	dir := t.TempDir()
	installTestGVersion(t, dir)
	marker := filepath.Join(dir, "probe-ran")
	t.Setenv("MULTI_DAP_CONFIG_GVERSION_MARKER", marker)
	t.Setenv("MULTI_DAP_CONFIG_GVERSION_FAIL", "1")
	_, err := ProbeMultiVersion(context.Background(), dir)
	if err == nil || !errors.Is(err, ErrMultiVersionUnavailable) {
		t.Fatalf("ProbeMultiVersion() error = %v, want unavailable", err)
	}
	if !fileExists(marker) {
		t.Fatal("gversion failure helper was not invoked")
	}
}

func TestProbeMultiVersionRejectsBannerFromFailedTargetProbe(t *testing.T) {
	dir := t.TempDir()
	installTestGVersion(t, dir)
	target := filepath.Join(dir, "multi.exe")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTI_DAP_CONFIG_GVERSION_BANNER_THEN_FAIL", "1")
	if _, err := ProbeMultiVersion(context.Background(), dir, target); err == nil || !errors.Is(err, ErrMultiVersionUnavailable) {
		t.Fatalf("ProbeMultiVersion(failed target with banner) error = %v, want unavailable", err)
	}
}

func TestProbeMultiVersionAcceptsNoArgumentGVersionBannerOnStderr(t *testing.T) {
	dir := t.TempDir()
	installTestGVersion(t, dir)
	t.Setenv("MULTI_DAP_CONFIG_GVERSION_STDERR", "1")
	got, err := ProbeMultiVersion(context.Background(), dir)
	if err != nil {
		t.Fatalf("ProbeMultiVersion() error = %v", err)
	}
	if got.Version != "8.1.4" || got.Source != MultiVersionSourceGVersion {
		t.Fatalf("ProbeMultiVersion() = %#v, want stderr gversion evidence", got)
	}
}

func installTestGVersion(t *testing.T, installation string) string {
	t.Helper()
	t.Setenv("MULTI_DAP_CONFIG_GVERSION_HELPER", "1")
	if err := os.MkdirAll(installation, 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(installation, "gversion.exe")
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
