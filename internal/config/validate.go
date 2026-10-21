package config

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Validated is Config after paths, durations, endpoints, and defaults have
// been normalized. Runtime packages should depend on this type, never on a
// parser's raw representation.
type Validated struct {
	Multi          ValidatedMulti
	Connection     ValidatedConnection
	Cores          []ValidatedCore
	Inspection     ValidatedInspection
	SourceRewrites []ValidatedSourceRewrite
	Endpoints      ValidatedEndpoints
	Timing         ValidatedTiming
	Lifecycle      ValidatedLifecycle
	Probe          ProbeIdentity
}

type ValidatedMulti struct {
	Installation string
	Executable   string
	// Version is the normalized, observed MULTI IDE release when evidence is
	// available. It remains empty for a legacy installation with no release
	// marker; VersionSource is likewise empty in that validated model.
	Version       string
	VersionSource string
	PythonVersion int
}

type ValidatedConnection struct {
	Project     string
	Arguments   string
	Preparation ConnectionPreparation
}

type ValidatedCore struct {
	ID  int
	ELF string
}

// ValidatedInspection preserves whether default_core was supplied. Runtime
// routing must not mistake an omitted value for configured core zero.
type ValidatedInspection struct {
	HasDefaultCore bool
	DefaultCore    int
}

type ValidatedSourceRewrite struct {
	From string
	To   string
}

type ValidatedEndpoints struct {
	MBP  Endpoint
	DAP  Endpoint
	Hint Endpoint
}

type ValidatedTiming struct {
	PollCadence     time.Duration
	RPCDeadline     time.Duration
	StartupDeadline time.Duration
}

type ValidatedLifecycle struct {
	RequireResetAfterDownload bool
}

// Problem identifies one invalid field. Validate returns all independent
// problems in one error so an operator can repair a configuration in one pass.
type Problem struct {
	Field string
	Err   error
}

func (p Problem) Error() string { return p.Field + ": " + p.Err.Error() }

// Problems is returned when validation finds one or more configuration errors.
type Problems []Problem

func (p Problems) Error() string {
	parts := make([]string, len(p))
	for i, problem := range p {
		parts[i] = problem.Error()
	}
	return strings.Join(parts, "; ")
}

// Validate normalizes c relative to baseDir, verifies structural startup-time
// invariants, and resolves any available MULTI release metadata. It never
// executes a program from the installation; callers that explicitly trust the
// installation's gversion utility can use LoadWithExecutableProbe for parsed
// TOML input.
func (c Config) Validate(baseDir string) (Validated, error) {
	return c.validateWithOptions(context.Background(), baseDir, false)
}

func (c Config) validateWithOptions(ctx context.Context, baseDir string, allowExecutableProbe bool) (Validated, error) {
	out, err := c.validateStructure(baseDir)
	if err != nil {
		return Validated{}, err
	}
	requested := strings.TrimSpace(c.Multi.Version)
	resolved, probeErr := ResolveMultiVersionWithOptions(ctx, ResolveOptions{
		Installation:         out.Multi.Installation,
		Executable:           out.Multi.Executable,
		Requested:            requested,
		RequireDetect:        c.Multi.versionSet || requested != "",
		AllowExecutableProbe: allowExecutableProbe,
	})
	if probeErr != nil {
		var versionErr *MultiVersionError
		// A legacy/test installation may carry no release marker at all. Keep
		// omitted-version configurations compatible while still failing closed
		// for an explicit release or for damaged metadata.
		if !c.Multi.versionSet && requested == "" && errors.As(probeErr, &versionErr) && !versionErr.MarkerFound && errors.Is(probeErr, ErrMultiVersionUnavailable) {
			return out, nil
		}
		return Validated{}, Problems{{Field: "multi.version", Err: probeErr}}
	}
	out.Multi.Version = resolved.Version
	out.Multi.VersionSource = resolved.Source
	// Keep the wire-facing validated model backward-compatible when no release
	// was observed. The resolver exposes an explicit `unknown` source to callers
	// using optional detection, but check/status should omit source metadata until
	// there is an actual version value to report.
	if out.Multi.Version == "" {
		out.Multi.VersionSource = ""
	}
	return out, nil
}

// validateStructure performs the non-executable portion of configuration
// validation. Keeping this helper separate lets Load select an explicit probe
// policy without duplicating the structural checks.
func (c Config) validateStructure(baseDir string) (Validated, error) {
	var out Validated
	var problems Problems

	base, err := absoluteDirectory(baseDir)
	if err != nil {
		return Validated{}, fmt.Errorf("configuration base directory: %w", err)
	}

	out.Multi.Installation = validateDirectory(&problems, "multi.installation", c.Multi.Installation, base)
	out.Multi.Executable = validateFile(&problems, "multi.executable", c.Multi.Executable, base)
	out.Multi.Version = validateMultiVersionSpec(&problems, c.Multi.Version, c.Multi.versionSet)
	out.Multi.PythonVersion = validatePythonVersion(&problems, c.Multi.PythonVersion, c.Multi.pythonVersionSet)
	if out.Multi.Installation != "" && out.Multi.Executable != "" && !within(out.Multi.Installation, out.Multi.Executable) {
		problems = append(problems, Problem{"multi.executable", errors.New("must be inside multi.installation")})
	}

	out.Connection.Project = validateFile(&problems, "connection.project", c.Connection.Project, base)
	out.Connection.Arguments = strings.TrimSpace(c.Connection.Arguments)
	if out.Connection.Arguments == "" {
		problems = append(problems, Problem{"connection.arguments", errors.New("is required")})
	}
	out.Connection.Preparation = validateConnectionPreparation(&problems, c.Connection.Preparation)

	seenCoreIDs := make(map[int]struct{}, len(c.Cores))
	seenCoreELFs := make(map[string]int, len(c.Cores))
	if len(c.Cores) == 0 {
		problems = append(problems, Problem{"cores", errors.New("at least one core is required")})
	}
	for i, core := range c.Cores {
		field := fmt.Sprintf("cores[%d]", i)
		if core.ID < 0 {
			problems = append(problems, Problem{field + ".id", errors.New("must not be negative")})
		} else if _, exists := seenCoreIDs[core.ID]; exists {
			problems = append(problems, Problem{field + ".id", fmt.Errorf("duplicates core ID %d", core.ID)})
		} else {
			seenCoreIDs[core.ID] = struct{}{}
		}
		elf := validateFile(&problems, field+".elf", core.ELF, base)
		if elf != "" {
			identity := canonicalWindowsPathIdentity(elf)
			if previous, exists := seenCoreELFs[identity]; exists {
				problems = append(problems, Problem{field + ".elf", fmt.Errorf("duplicates canonical ELF identity of cores[%d].elf", previous)})
			} else {
				seenCoreELFs[identity] = i
			}
		}
		out.Cores = append(out.Cores, ValidatedCore{ID: core.ID, ELF: elf})
	}
	if c.Inspection.DefaultCore != nil {
		out.Inspection.HasDefaultCore = true
		out.Inspection.DefaultCore = *c.Inspection.DefaultCore
		if _, exists := seenCoreIDs[*c.Inspection.DefaultCore]; !exists {
			problems = append(problems, Problem{"inspection.default_core", fmt.Errorf("must identify a configured core ID (got %d)", *c.Inspection.DefaultCore)})
		}
	}

	for i, rewrite := range c.SourceRewrites {
		field := fmt.Sprintf("source_rewrites[%d]", i)
		from := validateAbsolutePath(&problems, field+".from", rewrite.From)
		to := validateAbsolutePath(&problems, field+".to", rewrite.To)
		out.SourceRewrites = append(out.SourceRewrites, ValidatedSourceRewrite{From: from, To: to})
	}
	for i := range out.SourceRewrites {
		for j := 0; j < i; j++ {
			if out.SourceRewrites[i].From != "" && out.SourceRewrites[j].From != "" && (within(out.SourceRewrites[i].From, out.SourceRewrites[j].From) || within(out.SourceRewrites[j].From, out.SourceRewrites[i].From)) {
				problems = append(problems, Problem{fmt.Sprintf("source_rewrites[%d].from", i), fmt.Errorf("overlaps source_rewrites[%d].from", j)})
			}
			if out.SourceRewrites[i].To != "" && out.SourceRewrites[j].To != "" && (within(out.SourceRewrites[i].To, out.SourceRewrites[j].To) || within(out.SourceRewrites[j].To, out.SourceRewrites[i].To)) {
				problems = append(problems, Problem{fmt.Sprintf("source_rewrites[%d].to", i), fmt.Errorf("overlaps source_rewrites[%d].to", j)})
			}
		}
	}

	out.Endpoints.MBP = validateEndpoint(&problems, "endpoints.mbp", c.Endpoints.MBP)
	out.Endpoints.DAP = validateEndpoint(&problems, "endpoints.dap", c.Endpoints.DAP)
	out.Endpoints.Hint = validateEndpoint(&problems, "endpoints.hint", c.Endpoints.Hint)
	if sameBoundTCPEndpoint(out.Endpoints.MBP, out.Endpoints.DAP) {
		problems = append(problems, Problem{"endpoints", errors.New("mbp and dap must not bind the same non-zero TCP endpoint")})
	}

	out.Timing.PollCadence = validateDuration(&problems, "timing.poll_cadence", c.Timing.PollCadence)
	out.Timing.RPCDeadline = validateDuration(&problems, "timing.rpc_deadline", c.Timing.RPCDeadline)
	out.Timing.StartupDeadline = validateDuration(&problems, "timing.startup_deadline", c.Timing.StartupDeadline)

	out.Lifecycle.RequireResetAfterDownload = true
	if c.Lifecycle.RequireResetAfterDownload != nil {
		out.Lifecycle.RequireResetAfterDownload = *c.Lifecycle.RequireResetAfterDownload
	}

	out.Probe.ID, err = ValidateProbeID(c.Probe.ID)
	if err != nil {
		problems = append(problems, Problem{"probe.id", err})
	}

	if len(problems) != 0 {
		return Validated{}, problems
	}
	return out, nil
}

func validateMultiVersionSpec(problems *Problems, raw string, explicit bool) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		if explicit {
			*problems = append(*problems, Problem{"multi.version", errors.New("is required when specified; use \"auto\" for discovery")})
		}
		return ""
	}
	if strings.EqualFold(value, MultiVersionAuto) {
		// "auto" is a resolver directive, never an observed release value.
		return ""
	}
	normalized, err := NormalizeMultiVersion(value)
	if err != nil {
		*problems = append(*problems, Problem{"multi.version", err})
		return ""
	}
	return normalized
}

func validatePythonVersion(problems *Problems, raw int, explicit bool) int {
	// An omitted TOML integer decodes to zero. Prefer the Python 3 runtime for
	// current MULTI installations; legacy users can opt into Python 2.
	if raw == 0 {
		// Load marks an explicitly supplied zero so it cannot silently turn into
		// the preferred runtime. Directly constructed Config values leave this
		// marker false and retain the documented zero-value default.
		if explicit {
			*problems = append(*problems, Problem{"multi.python_version", fmt.Errorf("must be %d or %d", PythonVersion2, PythonVersion3)})
		}
		return DefaultPythonVersion
	}
	switch raw {
	case PythonVersion2, PythonVersion3:
		return raw
	default:
		*problems = append(*problems, Problem{"multi.python_version", fmt.Errorf("must be %d or %d", PythonVersion2, PythonVersion3)})
		return DefaultPythonVersion
	}
}

// canonicalWindowsPathIdentity matches the target's case-insensitive path
// comparison without requiring MULTI or a live target during config loading.
func canonicalWindowsPathIdentity(path string) string {
	return strings.ToLower(strings.ReplaceAll(filepath.Clean(path), "/", "\\"))
}

func absoluteDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("is not a directory")
	}
	return filepath.Clean(abs), nil
}

func validateDirectory(problems *Problems, field, path, base string) string {
	normalized := validatePath(problems, field, path, base)
	if normalized == "" {
		return ""
	}
	info, err := os.Stat(normalized)
	if err != nil {
		*problems = append(*problems, Problem{field, err})
		return ""
	}
	if !info.IsDir() {
		*problems = append(*problems, Problem{field, errors.New("must be a directory")})
		return ""
	}
	return normalized
}

func validateFile(problems *Problems, field, path, base string) string {
	normalized := validatePath(problems, field, path, base)
	if normalized == "" {
		return ""
	}
	info, err := os.Stat(normalized)
	if err != nil {
		*problems = append(*problems, Problem{field, err})
		return ""
	}
	if !info.Mode().IsRegular() {
		*problems = append(*problems, Problem{field, errors.New("must be a regular file")})
		return ""
	}
	return normalized
}

func validateAbsolutePath(problems *Problems, field, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		*problems = append(*problems, Problem{field, errors.New("is required")})
		return ""
	}
	if !filepath.IsAbs(path) {
		*problems = append(*problems, Problem{field, errors.New("must be absolute")})
		return ""
	}
	return filepath.Clean(path)
}

func validatePath(problems *Problems, field, path, base string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		*problems = append(*problems, Problem{field, errors.New("is required")})
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

func validateEndpoint(problems *Problems, field string, endpoint Endpoint) Endpoint {
	endpoint.Host = strings.TrimSpace(endpoint.Host)
	if endpoint.Host == "" {
		*problems = append(*problems, Problem{field + ".host", errors.New("is required")})
	} else if addr, err := netip.ParseAddr(endpoint.Host); err != nil || !addr.IsLoopback() {
		*problems = append(*problems, Problem{field + ".host", errors.New("must be a loopback IP literal")})
	} else {
		endpoint.Host = addr.String()
	}
	if endpoint.Port < 0 || endpoint.Port > 65535 {
		*problems = append(*problems, Problem{field + ".port", errors.New("must be between 0 and 65535")})
	}
	return endpoint
}

func validateDuration(problems *Problems, field, raw string) time.Duration {
	duration, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || duration <= 0 {
		if err == nil {
			err = errors.New("must be greater than zero")
		}
		*problems = append(*problems, Problem{field, err})
		return 0
	}
	return duration
}

func validateConnectionPreparation(problems *Problems, raw ConnectionPreparation) ConnectionPreparation {
	switch ConnectionPreparation(strings.TrimSpace(string(raw))) {
	case ConnectionPreparationNone, "none":
		return ConnectionPreparationNone
	case ConnectionPreparationAlreadyPresentNoVerify:
		return ConnectionPreparationAlreadyPresentNoVerify
	default:
		*problems = append(*problems, Problem{"connection.preparation", errors.New("must be none or already_present_no_verify")})
		return ConnectionPreparationNone
	}
}

func within(parent, child string) bool {
	// MULTI and the daemon run on Windows, where source paths are
	// case-insensitive. Compare normalized keys so case-only variants cannot
	// make two source-rewrite rules appear disjoint.
	rel, err := filepath.Rel(strings.ToLower(parent), strings.ToLower(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func sameBoundTCPEndpoint(a, b Endpoint) bool {
	return a.Port != 0 && a.Port == b.Port && a.Host == b.Host
}

// ValidateProbeID normalizes and validates the stable daemon identity used by
// project configuration and IDE proxy frontends. It performs no filesystem or
// control-plane access.
func ValidateProbeID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if !validProbeID(id) {
		return "", errors.New("must contain only letters, digits, '.', '_', or '-'")
	}
	return id, nil
}

func validProbeID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
