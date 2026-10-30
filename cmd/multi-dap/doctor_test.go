package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/control"
	"github.com/Tacrolimus/multi-dap/internal/daemon"
	"github.com/Tacrolimus/multi-dap/internal/hostprocess"
)

func TestMain(m *testing.M) {
	// CLI tests must not depend on the development host's process inventory.
	// The hostprocess package separately tests its real read-only OS surface.
	collectHostProcesses = func(context.Context, string) hostprocess.Report {
		return emptyProcessReport()
	}
	os.Exit(m.Run())
}

func emptyProcessReport() hostprocess.Report {
	return hostprocess.Report{
		State: hostprocess.StateComplete, Scope: hostprocess.ScopeConfiguredInstallation,
		Processes: []hostprocess.Entry{},
	}
}

func useProcessCollector(t *testing.T, collect func(context.Context, string) hostprocess.Report) {
	t.Helper()
	previous := collectHostProcesses
	collectHostProcesses = collect
	t.Cleanup(func() { collectHostProcesses = previous })
}

func TestDoctorProcessAdmissionPreservesStaticChecks(t *testing.T) {
	configPath := writeConfig(t)
	calls := 0
	useProcessCollector(t, func(context.Context, string) hostprocess.Report {
		calls++
		return emptyProcessReport()
	})
	for _, command := range []string{"doctor", "check"} {
		var output bytes.Buffer
		if err := run(nil, []string{command, "--config", configPath}, &output); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != "{\"event\":\"config_checked\",\"ok\":true}\n" {
			t.Fatalf("%s static receipt changed: %q", command, got)
		}
	}
	for _, args := range [][]string{
		{"doctor", "--config", configPath, "--include-paths"},
		{"doctor", "--config", configPath, "--processes", "extra"},
		{"doctor", "--config", configPath, "--unknown"},
		{"doctor", "--config", filepath.Join(t.TempDir(), "missing.toml"), "--processes"},
		{"check", "--config", configPath, "--processes"},
		{"check", "--config", configPath, "--include-paths"},
	} {
		var output bytes.Buffer
		if err := run(context.Background(), args, &output); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
		if output.Len() != 0 {
			t.Fatalf("invalid command produced output: %q", output.String())
		}
	}
	if calls != 0 {
		t.Fatalf("static or invalid command queried inventory %d times", calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"doctor", "--config", configPath, "--processes"}, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled doctor = %v", err)
	}
	if calls != 0 {
		t.Fatal("cancelled doctor queried inventory")
	}
}

func TestDoctorProcessReportsAreExplicitAndPathRedacted(t *testing.T) {
	configPath := writeConfig(t)
	for _, state := range []string{
		hostprocess.StateComplete, hostprocess.StatePartial, hostprocess.StateUnavailable, hostprocess.StateUnsupported,
	} {
		for _, paths := range []bool{false, true} {
			t.Run(state+map[bool]string{false: "/redacted", true: "/private"}[paths], func(t *testing.T) {
				report := hostprocess.Report{
					State: state, Scope: hostprocess.ScopeConfiguredInstallation, Scanned: 15, Unresolved: 3,
					Processes: []hostprocess.Entry{{
						PID: 123, ParentPID: 45, Executable: "multi.exe",
						ImagePath: `C:\private-installation\multi.exe`, Match: hostprocess.MatchInstallationPath,
					}},
				}
				useProcessCollector(t, func(ctx context.Context, installation string) hostprocess.Report {
					if installation != filepath.Join(filepath.Dir(configPath), "multi") {
						t.Fatalf("inventory installation = %q", installation)
					}
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > processInventoryTimeout || time.Until(deadline) <= 0 {
						t.Fatalf("inventory deadline = (%v, %v)", deadline, ok)
					}
					return report
				})
				args := []string{"doctor", "--config", configPath, "--processes"}
				if paths {
					args = append(args, "--include-paths")
				}
				var output bytes.Buffer
				if err := run(nil, args, &output); err != nil {
					t.Fatal(err)
				}
				var receipt processInventoryMessage
				if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Schema != "multi-dap.host-processes.v1" || receipt.Event != "process_inventory" ||
					!receipt.ConfigChecked || receipt.PathsIncluded != paths ||
					receipt.OwnershipVerified || receipt.ProbeAvailabilityVerified ||
					receipt.Inventory.State != state || len(receipt.Inventory.Processes) != 1 {
					t.Fatalf("unexpected doctor receipt: %+v", receipt)
				}
				got := receipt.Inventory.Processes[0]
				if (got.ImagePath != "") != paths || got.PID != 123 || got.ParentPID != 45 {
					t.Fatalf("unexpected process entry: %+v", got)
				}
				if !paths && strings.Contains(output.String(), "private-installation") {
					t.Fatal("default report leaked a private executable path")
				}
				if report.Processes[0].ImagePath == "" {
					t.Fatal("redaction mutated collector-owned data")
				}
				if strings.Contains(output.String(), `"ok":true`) {
					t.Fatal("configuration validation implied overall inventory success")
				}
			})
		}
	}
}

func TestDoctorInventoryOutputBoundAndSanitizedWriteErrors(t *testing.T) {
	configPath := writeConfig(t)
	useProcessCollector(t, func(context.Context, string) hostprocess.Report {
		report := emptyProcessReport()
		report.Processes = []hostprocess.Entry{{
			PID: 123, Executable: "multi.exe", Match: hostprocess.MatchInstallationPath,
			ImagePath: strings.Repeat("x", processInventoryMaxJSON),
		}}
		return report
	})
	var output bytes.Buffer
	err := run(context.Background(), []string{"doctor", "--config", configPath, "--processes", "--include-paths"}, &output)
	if err == nil || err.Error() != "process inventory report exceeds its output limit" || output.Len() != 0 {
		t.Fatalf("oversized report = (%v, %d output bytes)", err, output.Len())
	}
	if err := run(nil, []string{"doctor", "--config", configPath, "--processes"}, io.Discard); err != nil {
		t.Fatalf("path redaction did not precede output bound: %v", err)
	}
	for _, writer := range []io.Writer{processNoticeFailureWriter{}, processNoticeShortWriter{}} {
		err = run(nil, []string{"doctor", "--config", configPath, "--processes"}, writer)
		if err == nil || err.Error() != "write process inventory report failed" {
			t.Fatalf("write error was not sanitized: %v", err)
		}
	}
}

type processNoticeFailureWriter struct{}

func (processNoticeFailureWriter) Write([]byte) (int, error) {
	return 0, errors.New("private diagnostic destination")
}

type processNoticeShortWriter struct{}

func (processNoticeShortWriter) Write([]byte) (int, error) { return 0, nil }

func TestStartupProcessNoticeIsCountOnlyAndObservational(t *testing.T) {
	for _, state := range []string{
		hostprocess.StateComplete, hostprocess.StatePartial, hostprocess.StateUnavailable,
		hostprocess.StateUnsupported, "private unknown state",
	} {
		t.Run(state, func(t *testing.T) {
			useProcessCollector(t, func(context.Context, string) hostprocess.Report {
				return hostprocess.Report{
					State: state, Scope: "private scope", Scanned: 22, Unresolved: 5, Truncated: true,
					Reason: "private raw error",
					Processes: []hostprocess.Entry{{
						PID: 12345678, ParentPID: 87654321, Executable: "private.exe", ImagePath: "private image",
					}},
				}
			})
			var output bytes.Buffer
			if err := reportStartupProcesses(nil, "unused", &output); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "12345678") ||
				strings.Contains(output.String(), "87654321") {
				t.Fatalf("startup notice leaked process metadata: %q", output.String())
			}
			var notice startupProcessMessage
			if err := json.Unmarshal(output.Bytes(), &notice); err != nil {
				t.Fatal(err)
			}
			if notice.Event != "host_process_notice" || notice.Related != 1 ||
				notice.Scanned != 22 || notice.Unresolved != 5 || !notice.Truncated ||
				notice.OwnershipVerified || notice.ProbeAvailabilityVerified || notice.Hint == "" {
				t.Fatalf("unexpected notice: %+v", notice)
			}
		})
	}
	useProcessCollector(t, func(context.Context, string) hostprocess.Report { return emptyProcessReport() })
	var output bytes.Buffer
	if err := reportStartupProcesses(nil, "unused", &output); err != nil || output.Len() != 0 {
		t.Fatalf("empty complete observation = (%v, %q)", err, output.String())
	}
	useProcessCollector(t, func(context.Context, string) hostprocess.Report {
		report := emptyProcessReport()
		report.State = hostprocess.StatePartial
		return report
	})
	for _, writer := range []io.Writer{processNoticeFailureWriter{}, processNoticeShortWriter{}} {
		if err := reportStartupProcesses(nil, "unused", writer); err == nil ||
			err.Error() != "write host process notice failed" {
			t.Fatalf("notice write error = %v", err)
		}
	}
}

func TestHostProcessObservationHonorsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	useProcessCollector(t, func(scan context.Context, _ string) hostprocess.Report {
		cancel()
		if scan.Err() != context.Canceled {
			t.Fatalf("scan did not inherit cancellation: %v", scan.Err())
		}
		return emptyProcessReport()
	})
	var output bytes.Buffer
	if err := reportStartupProcesses(ctx, "unused", &output); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("cancelled observation = (%v, %q)", err, output.String())
	}
}

func TestServeProcessNoticePrecedesRuntimeAndPreservesReady(t *testing.T) {
	configPath, bridgeScript := processDiagnosticFixture(t)
	var stdout, stderr bytes.Buffer
	seen := false
	useProcessCollector(t, func(context.Context, string) hostprocess.Report {
		seen = true
		report := emptyProcessReport()
		report.Processes = []hostprocess.Entry{{PID: 123, Executable: "private.exe", ImagePath: "private image"}}
		return report
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous := startRuntime
	t.Cleanup(func() { startRuntime = previous })
	startRuntime = func(context.Context, daemon.Options) (runtime, error) {
		if !seen || !strings.Contains(stderr.String(), `"event":"host_process_notice"`) {
			t.Fatal("runtime started before process observation was reported")
		}
		cancel()
		return newFakeRuntime(), nil
	}
	if err := runMain(ctx, []string{"serve", "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", t.TempDir()}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var ready map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &ready); err != nil || len(ready) != 3 {
		t.Fatalf("ready contract changed: (%v, %q)", err, stdout.String())
	}
	if bytes.Count(stderr.Bytes(), []byte{'\n'}) != 1 || strings.Contains(stderr.String(), "private") {
		t.Fatalf("unexpected diagnostics: %q", stderr.String())
	}
}

func TestStartupProcessDiagnosticsCannotLaunchOnCancellationOrWriteFailure(t *testing.T) {
	for _, command := range []string{"serve", "ensure"} {
		for _, scenario := range []string{"cancelled", "write_failure", "cancel_during_scan"} {
			t.Run(command+"/"+scenario, func(t *testing.T) {
				configPath, bridgeScript := processDiagnosticFixture(t)
				previousRuntime, previousChild, previousDiscovery := startRuntime, launchEnsureServe, discoverWarmSession
				t.Cleanup(func() {
					startRuntime, launchEnsureServe, discoverWarmSession = previousRuntime, previousChild, previousDiscovery
				})
				startRuntime = func(context.Context, daemon.Options) (runtime, error) {
					t.Fatal("unexpected runtime start")
					return nil, nil
				}
				launchEnsureServe = func(ensureServeRequest) (ensureChild, error) {
					t.Fatal("unexpected daemon child start")
					return nil, nil
				}
				discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
					t.Fatal("unexpected warm discovery")
					return daemon.WarmSession{}, nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if scenario == "cancelled" {
					cancel()
				}
				calls := 0
				useProcessCollector(t, func(context.Context, string) hostprocess.Report {
					calls++
					if scenario == "cancel_during_scan" {
						cancel()
					}
					report := emptyProcessReport()
					report.State = hostprocess.StateUnavailable
					return report
				})
				args := []string{command, "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", t.TempDir()}
				if command == "ensure" {
					args = append(args, "--acquisition", "cold")
				}
				var stdout bytes.Buffer
				err := runWithStreams(ctx, args, strings.NewReader(""), &stdout, processNoticeFailureWriter{})
				if err == nil || stdout.Len() != 0 {
					t.Fatalf("startup unexpectedly accepted failure: (%v, %q)", err, stdout.String())
				}
				if scenario == "write_failure" && err.Error() != "write host process notice failed" {
					t.Fatalf("write failure = %v", err)
				}
				if scenario != "write_failure" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation = %v", err)
				}
				if scenario == "cancelled" && calls != 0 {
					t.Fatal("pre-cancelled startup scanned host")
				}
			})
		}
	}
}

func TestEnsureProcessNoticePrecedesWarmDiscovery(t *testing.T) {
	configPath, bridgeScript := processDiagnosticFixture(t)
	var stderr bytes.Buffer
	useProcessCollector(t, func(context.Context, string) hostprocess.Report {
		report := emptyProcessReport()
		report.State = hostprocess.StateUnsupported
		return report
	})
	previousConsole, previousDiscovery := prepareEnsureConsole, discoverWarmSession
	t.Cleanup(func() { prepareEnsureConsole, discoverWarmSession = previousConsole, previousDiscovery })
	prepareEnsureConsole = func() error {
		if !strings.Contains(stderr.String(), `"event":"host_process_notice"`) {
			t.Fatal("console preparation preceded diagnostics")
		}
		return nil
	}
	sentinel := errors.New("fake discovery complete")
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		if !strings.Contains(stderr.String(), `"event":"host_process_notice"`) {
			t.Fatal("discovery preceded diagnostics")
		}
		return daemon.WarmSession{}, sentinel
	}
	err := runEnsureWithDiagnostics(nil, []string{
		"--config", configPath, "--bridge-script", bridgeScript, "--control-dir", t.TempDir(),
		"--primary-elf", filepath.Join(filepath.Dir(configPath), "core.elf"),
	}, io.Discard, &stderr)
	if !errors.Is(err, sentinel) {
		t.Fatalf("warm diagnostic path = %v", err)
	}
}

func TestEnsureCancellationAfterDiscoveryDoesNotLaunchChild(t *testing.T) {
	configPath, bridgeScript := processDiagnosticFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previousConsole, previousDiscovery, previousLaunch := prepareEnsureConsole, discoverWarmSession, launchEnsureServe
	t.Cleanup(func() {
		prepareEnsureConsole, discoverWarmSession, launchEnsureServe = previousConsole, previousDiscovery, previousLaunch
	})
	prepareEnsureConsole = func() error { return nil }
	primary := filepath.Join(filepath.Dir(configPath), "core.elf")
	discoverWarmSession = func(context.Context, config.Validated, string, string) (daemon.WarmSession, error) {
		cancel()
		return daemon.WarmSession{ServiceRouterHost: "127.0.0.1", ServiceRouterPort: 40123, PrimaryELF: primary}, nil
	}
	launchEnsureServe = func(ensureServeRequest) (ensureChild, error) {
		t.Fatal("cancelled ensure launched a daemon child")
		return nil, nil
	}
	err := runEnsure(ctx, []string{
		"--config", configPath, "--bridge-script", bridgeScript, "--control-dir", t.TempDir(), "--primary-elf", primary,
	}, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-discovery cancellation = %v", err)
	}
}

func TestEnsureChildDoesNotRepeatParentProcessObservation(t *testing.T) {
	configPath, bridgeScript := processDiagnosticFixture(t)
	useProcessCollector(t, func(context.Context, string) hostprocess.Report {
		t.Fatal("ensure child repeated parent process observation")
		return emptyProcessReport()
	})
	previousRuntime, previousConsole, previousOwnership := startRuntime, prepareEnsureConsole, acquireEnsureChild
	t.Cleanup(func() {
		startRuntime, prepareEnsureConsole, acquireEnsureChild = previousRuntime, previousConsole, previousOwnership
	})
	prepareEnsureConsole = func() error { return nil }
	acquireEnsureChild = func() error { return nil }
	sentinel := errors.New("fake start reached")
	startRuntime = func(context.Context, daemon.Options) (runtime, error) { return nil, sentinel }
	err := runServe(context.Background(), []string{
		"--config", configPath, "--bridge-script", bridgeScript, "--control-dir", t.TempDir(),
		"--ensure-child", "--ensure-config-digest", bindingDigestForPath(t, configPath),
	}, io.Discard)
	if !errors.Is(err, sentinel) {
		t.Fatalf("ensure child startup = %v", err)
	}
}

func TestStartupInventorySkipsInvalidAndConflictingConfiguration(t *testing.T) {
	configPath, bridgeScript := processDiagnosticFixture(t)
	useProcessCollector(t, func(context.Context, string) hostprocess.Report {
		t.Fatal("invalid or conflicting startup scanned host")
		return emptyProcessReport()
	})
	for _, command := range []string{"serve", "ensure"} {
		for _, scenario := range []string{"invalid_config", "invalid_acquisition", "binding_mismatch"} {
			t.Run(command+"/"+scenario, func(t *testing.T) {
				controlDir := t.TempDir()
				args := []string{command, "--config", configPath, "--bridge-script", bridgeScript, "--control-dir", controlDir}
				if scenario == "invalid_config" {
					args[2] = filepath.Join(t.TempDir(), "missing.toml")
				}
				if command == "serve" {
					if scenario == "invalid_acquisition" {
						args = append(args, "--session-mode", "invalid")
					}
				} else {
					mode := "cold"
					if scenario == "invalid_acquisition" {
						mode = "invalid"
					}
					args = append(args, "--acquisition", mode)
				}
				if scenario == "binding_mismatch" {
					store, err := control.NewStore(controlDir)
					if err != nil {
						t.Fatal(err)
					}
					reservation, err := store.Reserve("test-probe", strings.Repeat("b", 64))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = reservation.Cancel() })
				}
				err := runWithStreams(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard)
				if err == nil {
					t.Fatal("invalid startup succeeded")
				}
				if scenario == "binding_mismatch" && !errors.Is(err, control.ErrConfigurationMismatch) {
					t.Fatalf("binding mismatch = %v", err)
				}
			})
		}
	}
}

func processDiagnosticFixture(t *testing.T) (string, string) {
	t.Helper()
	configPath := writeConfig(t)
	bridgeScript := filepath.Join(t.TempDir(), "bridge.py")
	if err := os.WriteFile(bridgeScript, []byte("# harmless fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, bridgeScript
}
