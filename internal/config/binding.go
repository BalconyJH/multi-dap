package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const bindingSchema = "multi-dap.config-binding/v1"

// BindingDigest returns an opaque, stable identity for a validated project
// configuration. It intentionally represents runtime semantics rather than
// the configuration file's location or bytes, so equivalent normalized
// configurations have the same binding. The digest is a binding check, not a
// confidentiality boundary; callers must continue to protect configuration
// data and control-plane records with their existing access controls.
func BindingDigest(validated Validated) (string, error) {
	envelope := bindingEnvelope{
		Schema: bindingSchema,
		Multi: bindingMulti{
			Installation: validated.Multi.Installation,
			Executable:   validated.Multi.Executable,
		},
		Connection: bindingConnection{
			Project:     validated.Connection.Project,
			Arguments:   validated.Connection.Arguments,
			Preparation: string(validated.Connection.Preparation),
		},
		Cores:          make([]bindingCore, len(validated.Cores)),
		SourceRewrites: make([]bindingSourceRewrite, len(validated.SourceRewrites)),
		Inspection: bindingInspection{
			HasDefaultCore: validated.Inspection.HasDefaultCore,
			DefaultCore:    validated.Inspection.DefaultCore,
		},
		Endpoints: bindingEndpoints{
			MBP:  bindingEndpoint(validated.Endpoints.MBP),
			DAP:  bindingEndpoint(validated.Endpoints.DAP),
			Hint: bindingEndpoint(validated.Endpoints.Hint),
		},
		Timing: bindingTiming{
			PollCadenceNanoseconds:     int64(validated.Timing.PollCadence),
			RPCDeadlineNanoseconds:     int64(validated.Timing.RPCDeadline),
			StartupDeadlineNanoseconds: int64(validated.Timing.StartupDeadline),
		},
		Lifecycle: bindingLifecycle{
			RequireResetAfterDownload: validated.Lifecycle.RequireResetAfterDownload,
		},
		Probe: bindingProbe{ID: validated.Probe.ID},
	}
	for i, core := range validated.Cores {
		envelope.Cores[i] = bindingCore{ID: core.ID, ELF: core.ELF}
	}
	for i, rewrite := range validated.SourceRewrites {
		envelope.SourceRewrites[i] = bindingSourceRewrite{From: rewrite.From, To: rewrite.To}
	}

	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// bindingEnvelope is deliberately map-free. Field order is part of its
// canonical JSON representation, allowing the versioned schema to evolve by
// adding a new envelope type rather than silently changing an existing digest.
type bindingEnvelope struct {
	Schema         string                 `json:"schema"`
	Multi          bindingMulti           `json:"multi"`
	Connection     bindingConnection      `json:"connection"`
	Cores          []bindingCore          `json:"cores"`
	Inspection     bindingInspection      `json:"inspection"`
	SourceRewrites []bindingSourceRewrite `json:"source_rewrites"`
	Endpoints      bindingEndpoints       `json:"endpoints"`
	Timing         bindingTiming          `json:"timing"`
	Lifecycle      bindingLifecycle       `json:"lifecycle"`
	Probe          bindingProbe           `json:"probe"`
}

type bindingMulti struct {
	Installation string `json:"installation"`
	Executable   string `json:"executable"`
}

type bindingConnection struct {
	Project     string `json:"project"`
	Arguments   string `json:"arguments"`
	Preparation string `json:"preparation"`
}

type bindingCore struct {
	ID  int    `json:"id"`
	ELF string `json:"elf"`
}

type bindingInspection struct {
	HasDefaultCore bool `json:"has_default_core"`
	DefaultCore    int  `json:"default_core"`
}

type bindingSourceRewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type bindingEndpoints struct {
	MBP  bindingEndpoint `json:"mbp"`
	DAP  bindingEndpoint `json:"dap"`
	Hint bindingEndpoint `json:"hint"`
}

type bindingEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type bindingTiming struct {
	PollCadenceNanoseconds     int64 `json:"poll_cadence_nanoseconds"`
	RPCDeadlineNanoseconds     int64 `json:"rpc_deadline_nanoseconds"`
	StartupDeadlineNanoseconds int64 `json:"startup_deadline_nanoseconds"`
}

type bindingLifecycle struct {
	RequireResetAfterDownload bool `json:"require_reset_after_download"`
}

type bindingProbe struct {
	ID string `json:"id"`
}
