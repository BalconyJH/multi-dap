package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateAcceptsAndNormalizesConfig(t *testing.T) {
	root := testFiles(t)
	cfg := validConfig()
	cfg.Multi.Installation = "multi"
	cfg.Multi.Executable = "multi/mpythonrun.exe"
	cfg.Connection.Project = "project.ghsmc"
	cfg.Connection.Preparation = ConnectionPreparationAlreadyPresentNoVerify
	cfg.Cores[0].ELF = "core0.elf"
	cfg.Cores = append(cfg.Cores, CoreConfig{ID: 1, ELF: "core1.elf"})
	cfg.SourceRewrites = []SourceRewrite{
		{From: filepath.Join(root, "debug"), To: filepath.Join(root, "client")},
	}
	cfg.Endpoints.MBP.Host = "0:0:0:0:0:0:0:1"

	got, err := cfg.Validate(root)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got.Multi.Installation != filepath.Join(root, "multi") {
		t.Errorf("normalized installation = %q", got.Multi.Installation)
	}
	if got.Connection.Project != filepath.Join(root, "project.ghsmc") {
		t.Errorf("normalized project = %q", got.Connection.Project)
	}
	if got.Connection.Preparation != ConnectionPreparationAlreadyPresentNoVerify {
		t.Errorf("normalized preparation = %q", got.Connection.Preparation)
	}
	if got.Timing.PollCadence != 25*time.Millisecond || got.Timing.RPCDeadline != time.Second || got.Timing.StartupDeadline != 5*time.Second {
		t.Errorf("normalized timing = %#v", got.Timing)
	}
	if !got.Lifecycle.RequireResetAfterDownload {
		t.Error("default lifecycle gate was not enabled")
	}
	if got.Endpoints.MBP.Host != "::1" {
		t.Errorf("normalized MBP host = %q, want ::1", got.Endpoints.MBP.Host)
	}
}

func TestValidateRejectsBoundaryViolations(t *testing.T) {
	root := testFiles(t)

	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{
			name:   "missing MULTI executable",
			mutate: func(c *Config) { c.Multi.Executable = "multi/missing.exe" },
			field:  "multi.executable",
		},
		{
			name:   "executable outside installation",
			mutate: func(c *Config) { c.Multi.Executable = "outside.exe" },
			field:  "multi.executable",
		},
		{
			name:   "missing connection project",
			mutate: func(c *Config) { c.Connection.Project = "missing.ghsmc" },
			field:  "connection.project",
		},
		{
			name:   "missing connection arguments",
			mutate: func(c *Config) { c.Connection.Arguments = " \t" },
			field:  "connection.arguments",
		},
		{
			name:   "empty core set",
			mutate: func(c *Config) { c.Cores = nil },
			field:  "cores",
		},
		{
			name:   "negative core ID",
			mutate: func(c *Config) { c.Cores[0].ID = -1 },
			field:  "cores[0].id",
		},
		{
			name:   "duplicate core ID",
			mutate: func(c *Config) { c.Cores = append(c.Cores, CoreConfig{ID: 0, ELF: "core1.elf"}) },
			field:  "cores[1].id",
		},
		{
			name: "duplicate canonical core ELF",
			mutate: func(c *Config) {
				c.Cores = append(c.Cores, CoreConfig{ID: 4, ELF: "./CORE0.elf"})
			},
			field: "cores[1].elf",
		},
		{
			name:   "missing core ELF",
			mutate: func(c *Config) { c.Cores[0].ELF = "missing.elf" },
			field:  "cores[0].elf",
		},
		{
			name:   "non-loopback MBP host",
			mutate: func(c *Config) { c.Endpoints.MBP.Host = "0.0.0.0" },
			field:  "endpoints.mbp.host",
		},
		{
			name:   "non-loopback hint host",
			mutate: func(c *Config) { c.Endpoints.Hint.Host = "example.invalid" },
			field:  "endpoints.hint.host",
		},
		{
			name:   "invalid endpoint port",
			mutate: func(c *Config) { c.Endpoints.DAP.Port = 65536 },
			field:  "endpoints.dap.port",
		},
		{
			name: "conflicting TCP listeners",
			mutate: func(c *Config) {
				c.Endpoints.DAP = c.Endpoints.MBP
				c.Endpoints.DAP.Port = 43001
				c.Endpoints.MBP.Port = 43001
			},
			field: "endpoints",
		},
		{
			name:   "zero poll cadence",
			mutate: func(c *Config) { c.Timing.PollCadence = "0s" },
			field:  "timing.poll_cadence",
		},
		{
			name:   "malformed RPC deadline",
			mutate: func(c *Config) { c.Timing.RPCDeadline = "soon" },
			field:  "timing.rpc_deadline",
		},
		{
			name:   "empty probe identity",
			mutate: func(c *Config) { c.Probe.ID = "" },
			field:  "probe.id",
		},
		{
			name:   "unsafe probe identity",
			mutate: func(c *Config) { c.Probe.ID = "probe/id" },
			field:  "probe.id",
		},
		{
			name: "relative source rewrite",
			mutate: func(c *Config) {
				c.SourceRewrites = []SourceRewrite{{From: "debug", To: filepath.Join(root, "client")}}
			},
			field: "source_rewrites[0].from",
		},
		{
			name: "overlapping source rewrites",
			mutate: func(c *Config) {
				c.SourceRewrites = []SourceRewrite{
					{From: filepath.Join(root, "debug"), To: filepath.Join(root, "client")},
					{From: filepath.Join(root, "debug", "generated"), To: filepath.Join(root, "client", "generated")},
				}
			},
			field: "source_rewrites[1].from",
		},
		{
			name: "case-only source rewrite overlap",
			mutate: func(c *Config) {
				c.SourceRewrites = []SourceRewrite{
					{From: filepath.Join(root, "debug"), To: filepath.Join(root, "client")},
					{From: filepath.Join(root, "DEBUG"), To: filepath.Join(root, "other-client")},
				}
			},
			field: "source_rewrites[1].from",
		},
		{
			name: "overlapping client rewrite prefixes",
			mutate: func(c *Config) {
				c.SourceRewrites = []SourceRewrite{
					{From: filepath.Join(root, "debug-one"), To: filepath.Join(root, "client")},
					{From: filepath.Join(root, "debug-two"), To: filepath.Join(root, "client", "generated")},
				}
			},
			field: "source_rewrites[1].to",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.mutate(&cfg)
			_, err := cfg.Validate(root)
			if err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
			if !hasProblem(err, test.field) {
				t.Fatalf("Validate() error = %v, want problem for %q", err, test.field)
			}
		})
	}
}

func TestValidateLifecycleOverrideAndEndpointEdgePorts(t *testing.T) {
	root := testFiles(t)
	noReset := false
	cfg := validConfig()
	cfg.Lifecycle.RequireResetAfterDownload = &noReset
	cfg.Endpoints.MBP.Port = 0
	cfg.Endpoints.Hint.Port = 65535

	got, err := cfg.Validate(root)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got.Lifecycle.RequireResetAfterDownload {
		t.Error("explicit lifecycle override was ignored")
	}
}

func TestValidateInspectionDefaultCore(t *testing.T) {
	root := testFiles(t)
	zero := 0
	unknown := 99

	t.Run("configured core zero remains explicit", func(t *testing.T) {
		cfg := validConfig()
		cfg.Inspection.DefaultCore = &zero
		got, err := cfg.Validate(root)
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if !got.Inspection.HasDefaultCore || got.Inspection.DefaultCore != 0 {
			t.Fatalf("inspection = %#v, want explicit core zero", got.Inspection)
		}
	})

	t.Run("omitted remains omitted", func(t *testing.T) {
		got, err := validConfig().Validate(root)
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if got.Inspection.HasDefaultCore {
			t.Fatalf("inspection = %#v, want no configured default", got.Inspection)
		}
	})

	t.Run("unknown core is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.Inspection.DefaultCore = &unknown
		_, err := cfg.Validate(root)
		if err == nil || !hasProblem(err, "inspection.default_core") {
			t.Fatalf("Validate() error = %v, want inspection.default_core", err)
		}
	})
}

func TestValidateConnectionPreparation(t *testing.T) {
	root := testFiles(t)
	for _, test := range []struct {
		name string
		raw  ConnectionPreparation
		want ConnectionPreparation
		ok   bool
	}{
		{name: "omitted", raw: "", want: ConnectionPreparationNone, ok: true},
		{name: "none", raw: " none ", want: ConnectionPreparationNone, ok: true},
		{name: "already present", raw: ConnectionPreparationAlreadyPresentNoVerify, want: ConnectionPreparationAlreadyPresentNoVerify, ok: true},
		{name: "unknown", raw: "download", ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Connection.Preparation = test.raw
			got, err := cfg.Validate(root)
			if (err == nil) != test.ok {
				t.Fatalf("Validate() error = %v, want ok=%v", err, test.ok)
			}
			if test.ok && got.Connection.Preparation != test.want {
				t.Fatalf("preparation = %q, want %q", got.Connection.Preparation, test.want)
			}
			if !test.ok && !hasProblem(err, "connection.preparation") {
				t.Fatalf("Validate() error = %v, want connection.preparation", err)
			}
		})
	}
}

func TestValidateProbeIDForIDEProxy(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "  probe-1  ", want: "probe-1", ok: true},
		{raw: "probe.alpha_2", want: "probe.alpha_2", ok: true},
		{raw: "", ok: false},
		{raw: "probe/one", ok: false},
	} {
		got, err := ValidateProbeID(test.raw)
		if (err == nil) != test.ok || got != test.want {
			t.Fatalf("ValidateProbeID(%q) = (%q, %v), want (%q, ok=%v)", test.raw, got, err, test.want, test.ok)
		}
	}
}

func validConfig() Config {
	return Config{
		Multi: MultiConfig{Installation: "multi", Executable: "multi/mpythonrun.exe"},
		Connection: ConnectionConfig{
			Project:   "project.ghsmc",
			Arguments: "target-server --device-file=device.dvf",
		},
		Cores: []CoreConfig{{ID: 0, ELF: "core0.elf"}},
		Endpoints: EndpointsConfig{
			MBP:  Endpoint{Host: "127.0.0.1", Port: 43001},
			DAP:  Endpoint{Host: "::1", Port: 43002},
			Hint: Endpoint{Host: "127.0.0.1", Port: 43003},
		},
		Timing: TimingConfig{
			PollCadence:     "25ms",
			RPCDeadline:     "1s",
			StartupDeadline: "5s",
		},
		Probe: ProbeIdentity{ID: "probe-1"},
	}
}

func testFiles(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{
		"multi/mpythonrun.exe",
		"project.ghsmc",
		"core0.elf",
		"core1.elf",
		"outside.exe",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func hasProblem(err error, field string) bool {
	var problems Problems
	if !errors.As(err, &problems) {
		return false
	}
	for _, problem := range problems {
		if problem.Field == field {
			return true
		}
	}
	return false
}
