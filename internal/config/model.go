// Package config defines the target-agnostic project configuration model.
//
// This package intentionally does not select a TOML implementation. A future
// parser decodes its input into Config, then calls Validate before any caller
// can launch MULTI or bind a listener. Keeping those steps separate prevents a
// parser choice from becoming part of Debugger Core's API.
package config

// Config is the parser-facing representation of one multi-dap project.
// Paths may be relative to the directory passed to Validate.
type Config struct {
	Multi          MultiConfig      `toml:"multi"`
	Connection     ConnectionConfig `toml:"connection"`
	Cores          []CoreConfig     `toml:"cores"`
	Inspection     InspectionConfig `toml:"inspection"`
	SourceRewrites []SourceRewrite  `toml:"source_rewrites"`
	Endpoints      EndpointsConfig  `toml:"endpoints"`
	Timing         TimingConfig     `toml:"timing"`
	Lifecycle      LifecycleConfig  `toml:"lifecycle"`
	Probe          ProbeIdentity    `toml:"probe"`
}

// MultiConfig identifies the local MULTI installation and its mpythonrun
// executable. Both are explicit so launcher behaviour never depends on PATH.
type MultiConfig struct {
	Installation string `toml:"installation"`
	Executable   string `toml:"executable"`
}

// ConnectionConfig contains the project loaded by DebugProgram and the opaque
// dbserver argument string passed to ConnectToTarget. The argument grammar
// belongs to MULTI and is intentionally not parsed here.
type ConnectionConfig struct {
	Project     string                `toml:"project"`
	Arguments   string                `toml:"arguments"`
	Preparation ConnectionPreparation `toml:"preparation"`
}

// ConnectionPreparation selects an explicit, documented target preparation
// action for a cold connection. The empty value is intentionally inert.
type ConnectionPreparation string

const (
	ConnectionPreparationNone                   ConnectionPreparation = ""
	ConnectionPreparationAlreadyPresentNoVerify ConnectionPreparation = "already_present_no_verify"
)

// CoreConfig maps one MULTI core ID to the ELF used for its source index.
type CoreConfig struct {
	ID  int    `toml:"id"`
	ELF string `toml:"elf"`
}

// InspectionConfig selects an explicit core for DAP inspection requests whose
// wire format carries only a numeric address. A nil DefaultCore intentionally
// differs from core zero: omitted defaults remain unambiguous only for a
// single-core configuration.
type InspectionConfig struct {
	DefaultCore *int `toml:"default_core"`
}

// SourceRewrite maps a debug-side source-path prefix to the corresponding
// client-side prefix. Rules must not overlap, so lookup order is immaterial.
type SourceRewrite struct {
	From string `toml:"from"`
	To   string `toml:"to"`
}

// EndpointsConfig contains listeners used by the three protocol channels.
// Port zero asks the operating system to allocate a random port.
type EndpointsConfig struct {
	MBP  Endpoint `toml:"mbp"`
	DAP  Endpoint `toml:"dap"`
	Hint Endpoint `toml:"hint"`
}

// Endpoint is a host/port listener address. Host is deliberately a separate
// field so validation can reject non-loopback bindings before net.Listen.
type Endpoint struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

// TimingConfig declares actor polling cadence and Go-owned deadlines. Values
// use time.ParseDuration syntax and remain strings until validation.
type TimingConfig struct {
	PollCadence     string `toml:"poll_cadence"`
	RPCDeadline     string `toml:"rpc_deadline"`
	StartupDeadline string `toml:"startup_deadline"`
}

// LifecycleConfig controls target-specific lifecycle gates. Nil means the
// documented safe default: a reset is required after download.
type LifecycleConfig struct {
	RequireResetAfterDownload *bool `toml:"require_reset_after_download"`
}

// ProbeIdentity names the physical probe for daemon duplicate detection. It
// is an operator-provided stable identifier, not a target identifier.
type ProbeIdentity struct {
	ID string `toml:"id"`
}
