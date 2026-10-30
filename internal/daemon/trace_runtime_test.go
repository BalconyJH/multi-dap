package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/timemachine"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

func syntheticNativePE(t *testing.T, path string, machine, characteristics uint16) []byte {
	t.Helper()
	dos := make([]byte, 128)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[60:], uint32(len(dos)))
	data := bytes.NewBuffer(dos)
	data.WriteString("PE\x00\x00")
	if err := binary.Write(data, binary.LittleEndian, pe.FileHeader{
		Machine: machine, SizeOfOptionalHeader: 240, Characteristics: characteristics,
	}); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(data, binary.LittleEndian, pe.OptionalHeader64{
		Magic: 0x20b, NumberOfRvaAndSizes: 16,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func nativeRuntimeFixture(t *testing.T) (config.Validated, string) {
	t.Helper()
	cfg := testRuntimeConfig()
	cfg.Multi.Installation = t.TempDir()
	cfg.Multi.Version = "8.1.4"
	syntheticNativePE(t, filepath.Join(cfg.Multi.Installation, "timemachine_api.dll"), pe.IMAGE_FILE_MACHINE_AMD64, pe.IMAGE_FILE_DLL)
	syntheticNativePE(t, filepath.Join(cfg.Multi.Installation, "python-3.8.6", "python.exe"), pe.IMAGE_FILE_MACHINE_AMD64, 0)
	script := testBridgeScript(t)
	for _, name := range []string{"timemachine_helper.py", "timemachine_native.py"} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(script), name), []byte("# synthetic, never execute\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, script
}

func TestNativeDiscoveryIsOptionalAndDoesNotCreateHelpers(t *testing.T) {
	cfg, script := nativeRuntimeFixture(t)
	controller := prepareNativeTraceController(cfg, script, nil)
	if runtime.GOOS == "windows" {
		if controller == nil || !controller.Enabled() {
			t.Fatal("complete host-only fixture did not expose optional requests")
		}
		if status := controller.Snapshot(); status.State != "idle" || status.Handle != 0 ||
			status.NativeRuntimeQualified || status.HardwareQualified || status.PacketCountKnown {
			t.Fatalf("discovery performed or qualified native work: %#v", status)
		}
		<-controller.ShutdownDone()
	} else if controller != nil {
		t.Fatal("Windows-only production discovery enabled on another host")
	}
	for _, change := range []func(*config.Validated, string){
		func(c *config.Validated, _ string) { c.Multi.Version = "7.1.6d" },
		func(c *config.Validated, _ string) { c.Multi.Version = "" },
		func(c *config.Validated, _ string) { c.Multi.PythonVersion = 2 },
		func(c *config.Validated, _ string) { c.Multi.Installation = t.TempDir() },
		func(_ *config.Validated, s string) {
			if err := os.Remove(filepath.Join(filepath.Dir(s), "timemachine_helper.py")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		selected, selectedScript := nativeRuntimeFixture(t)
		change(&selected, selectedScript)
		if got := prepareNativeTraceController(selected, selectedScript, nil); got != nil {
			t.Fatal("unavailable native runtime enabled optional requests")
		}
	}
}

func TestNativeLibraryInspectionPinsActualBytesWithoutLoading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic.dll")
	data := syntheticNativePE(t, path, pe.IMAGE_FILE_MACHINE_AMD64, pe.IMAGE_FILE_DLL)
	want := sha256.Sum256(data)
	got, err := inspectNativeLibrary(path)
	if err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("native file fingerprint = %q, %v", got, err)
	}
	for _, invalid := range []struct{ machine, flags uint16 }{
		{pe.IMAGE_FILE_MACHINE_I386, pe.IMAGE_FILE_DLL},
		{pe.IMAGE_FILE_MACHINE_AMD64, 0},
	} {
		syntheticNativePE(t, path, invalid.machine, invalid.flags)
		if _, err := inspectNativeLibrary(path); err == nil {
			t.Fatal("unsupported library was admitted")
		}
	}
	if err := os.WriteFile(path, []byte("not PE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectNativeLibrary(path); err == nil {
		t.Fatal("malformed library was admitted")
	}
}

func TestNativeProductionFactoryStartFailureReturnsNoTypedNilBackend(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("production native discovery is Windows-only")
	}
	cfg, script := nativeRuntimeFixture(t)
	controller := prepareNativeTraceController(cfg, script, nil)
	if controller == nil {
		t.Fatal("host fixture did not expose the native factory")
	}
	t.Cleanup(func() { <-controller.ShutdownDone() })
	// The synthetic PE has no executable image flag or entry point. Windows
	// rejects process creation; no real interpreter or vendor DLL is executed.
	result, err := controller.Execute(context.Background(), "owner", tracecontrol.Request{
		Op: tracecontrol.OpOpen,
		Open: tracecontrol.OpenRequest{
			Source: timemachine.Source{
				Kind: "live", SessionID: uint32(os.Getpid()) + 3000000, SourceID: "synthetic-start-failure",
			},
			AcknowledgeUnverifiedSource: true,
		},
	})
	if !errors.Is(err, tracecontrol.ErrUnavailable) || result.Status.NeedsReconciliation ||
		result.Receipt.Outcome != "failed_before_native_open" {
		t.Fatalf("failed production factory = %#v, %v", result, err)
	}
}

func TestNativeRuntimeRejectsInstallationEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.exe")
	if err := os.WriteFile(outside, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := containedNativeFile(root, relative); err == nil {
		t.Fatal("installation escape accepted")
	}
	if _, err := containedNativeFile(root, "."); err == nil {
		t.Fatal("directory accepted as a native file")
	}
}

func TestNativeSourceLeaseExcludesAnotherCollectorAndReleases(t *testing.T) {
	// Include the PID so simultaneous test binaries cannot share a fixture.
	id := uint32(os.Getpid()) + 1000000
	lease, err := acquireNativeTraceSource(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if other, err := acquireNativeTraceSource(context.Background(), id); !errors.Is(err, tracecontrol.ErrUnavailable) || other != nil {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("competing source lease = %#v, %v", other, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := acquireNativeTraceSource(context.Background(), id)
	if err != nil {
		t.Fatalf("released source remained unavailable: %v", err)
	}
	_ = other.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireNativeTraceSource(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled source acquisition = %v", err)
	}
	for _, invalid := range []uint32{0, 1 << 31} {
		if _, err := acquireNativeTraceSource(context.Background(), invalid); !errors.Is(err, tracecontrol.ErrInvalidRequest) {
			t.Fatal("invalid source identity admitted")
		}
	}
}

func TestNativeDeadlinesStayBounded(t *testing.T) {
	for _, test := range []struct{ configured, want time.Duration }{
		{0, 3 * time.Second},
		{20 * time.Millisecond, 20 * time.Millisecond},
		{time.Hour, time.Minute},
	} {
		if got := boundedNativeDeadline(test.configured, 3*time.Second); got != test.want {
			t.Fatalf("native deadline = %v, want %v", got, test.want)
		}
	}
}
