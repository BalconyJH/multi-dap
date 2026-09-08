package config

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBindingDigestDeterministicAndOpaque(t *testing.T) {
	validated := bindingTestValidated()
	first, err := BindingDigest(validated)
	if err != nil {
		t.Fatalf("BindingDigest() error = %v", err)
	}
	second, err := BindingDigest(validated)
	if err != nil {
		t.Fatalf("BindingDigest() second error = %v", err)
	}
	if first != second {
		t.Fatalf("BindingDigest() changed for the same input: first %q, second %q", first, second)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("BindingDigest() = %q, want lowercase 64-character SHA-256 hex", first)
	}
	for _, raw := range []string{
		validated.Multi.Installation,
		validated.Connection.Project,
		validated.Connection.Arguments,
		validated.Cores[0].ELF,
		validated.SourceRewrites[0].From,
	} {
		if strings.Contains(first, raw) {
			t.Fatalf("BindingDigest() exposes raw configuration value %q", raw)
		}
	}
}

func TestBindingDigestTreatsEmptyOptionalSliceAsNormalizedEmpty(t *testing.T) {
	nilSlices := bindingTestValidated()
	nilSlices.SourceRewrites = nil
	emptySlices := nilSlices
	emptySlices.SourceRewrites = []ValidatedSourceRewrite{}

	nilDigest, err := BindingDigest(nilSlices)
	if err != nil {
		t.Fatalf("BindingDigest(nil slices) error = %v", err)
	}
	emptyDigest, err := BindingDigest(emptySlices)
	if err != nil {
		t.Fatalf("BindingDigest(empty slices) error = %v", err)
	}
	if nilDigest != emptyDigest {
		t.Fatalf("BindingDigest() distinguished semantically empty optional slices: nil %q, empty %q", nilDigest, emptyDigest)
	}
}

func TestBindingDigestChangesForEveryRuntimeField(t *testing.T) {
	base := bindingTestValidated()
	baseDigest := mustBindingDigest(t, base)

	tests := []struct {
		name   string
		mutate func(*Validated)
	}{
		{"multi installation", func(v *Validated) { v.Multi.Installation = `C:\MULTI-alt` }},
		{"multi executable", func(v *Validated) { v.Multi.Executable = `C:\MULTI\bin\alt.exe` }},
		{"connection project", func(v *Validated) { v.Connection.Project = `C:\work\alt.cmp` }},
		{"connection arguments", func(v *Validated) { v.Connection.Arguments = `usb=other` }},
		{"connection preparation", func(v *Validated) { v.Connection.Preparation = ConnectionPreparationAlreadyPresentNoVerify }},
		{"core ID", func(v *Validated) { v.Cores[0].ID = 7 }},
		{"core ELF", func(v *Validated) { v.Cores[0].ELF = `C:\work\alt.elf` }},
		{"core order", func(v *Validated) { v.Cores[0], v.Cores[1] = v.Cores[1], v.Cores[0] }},
		{"inspection presence", func(v *Validated) { v.Inspection.HasDefaultCore = false }},
		{"inspection default core", func(v *Validated) { v.Inspection.DefaultCore = 1 }},
		{"source rewrite from", func(v *Validated) { v.SourceRewrites[0].From = `C:\source-alt` }},
		{"source rewrite to", func(v *Validated) { v.SourceRewrites[0].To = `D:\client-alt` }},
		{"source rewrite order", func(v *Validated) {
			v.SourceRewrites[0], v.SourceRewrites[1] = v.SourceRewrites[1], v.SourceRewrites[0]
		}},
		{"MBP host", func(v *Validated) { v.Endpoints.MBP.Host = "127.0.0.2" }},
		{"MBP port", func(v *Validated) { v.Endpoints.MBP.Port = 10001 }},
		{"DAP host", func(v *Validated) { v.Endpoints.DAP.Host = "127.0.0.2" }},
		{"DAP port", func(v *Validated) { v.Endpoints.DAP.Port = 10002 }},
		{"hint host", func(v *Validated) { v.Endpoints.Hint.Host = "127.0.0.2" }},
		{"hint port", func(v *Validated) { v.Endpoints.Hint.Port = 10003 }},
		{"poll cadence", func(v *Validated) { v.Timing.PollCadence = 101 * time.Millisecond }},
		{"RPC deadline", func(v *Validated) { v.Timing.RPCDeadline = 2 * time.Second }},
		{"startup deadline", func(v *Validated) { v.Timing.StartupDeadline = 3 * time.Second }},
		{"lifecycle", func(v *Validated) { v.Lifecycle.RequireResetAfterDownload = false }},
		{"probe ID", func(v *Validated) { v.Probe.ID = "probe-alt" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := bindingTestValidated()
			test.mutate(&mutated)
			if got := mustBindingDigest(t, mutated); got == baseDigest {
				t.Fatal("BindingDigest() did not change after a runtime field changed")
			}
		})
	}
}

func mustBindingDigest(t *testing.T, validated Validated) string {
	t.Helper()
	digest, err := BindingDigest(validated)
	if err != nil {
		t.Fatalf("BindingDigest() error = %v", err)
	}
	return digest
}

func bindingTestValidated() Validated {
	return Validated{
		Multi: ValidatedMulti{
			Installation: `C:\MULTI`,
			Executable:   `C:\MULTI\bin\mpythonrun.exe`,
		},
		Connection: ValidatedConnection{
			Project:     `C:\work\target.cmp`,
			Arguments:   "usb=serial-1234",
			Preparation: ConnectionPreparationNone,
		},
		Cores: []ValidatedCore{
			{ID: 0, ELF: `C:\work\core0.elf`},
			{ID: 1, ELF: `C:\work\core1.elf`},
		},
		Inspection: ValidatedInspection{HasDefaultCore: true, DefaultCore: 0},
		SourceRewrites: []ValidatedSourceRewrite{
			{From: `C:\source`, To: `D:\client`},
			{From: `C:\vendor`, To: `D:\vendor`},
		},
		Endpoints: ValidatedEndpoints{
			MBP:  Endpoint{Host: "127.0.0.1", Port: 10000},
			DAP:  Endpoint{Host: "127.0.0.1", Port: 0},
			Hint: Endpoint{Host: "::1", Port: 0},
		},
		Timing: ValidatedTiming{
			PollCadence:     100 * time.Millisecond,
			RPCDeadline:     time.Second,
			StartupDeadline: 2 * time.Second,
		},
		Lifecycle: ValidatedLifecycle{RequireResetAfterDownload: true},
		Probe:     ProbeIdentity{ID: "probe-1"},
	}
}
