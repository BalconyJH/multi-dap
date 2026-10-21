package config

// This file contains MULTI installation release inspection. The default reads
// metadata only; executing an installation tool requires explicit opt-in.
// MULTI's PE version resource is not the product release (the 8.1.4 install
// reports 8.0 there), so discovery uses Green Hills' gversion utility and the
// installation's human-readable release metadata instead.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// MultiVersionAuto asks ResolveMultiVersion to discover the installed
	// release. An omitted [multi].version also discovers available metadata,
	// but may retain an unknown version for a legacy installation with no
	// release evidence; "auto" makes that evidence mandatory.
	MultiVersionAuto = "auto"

	MultiVersionSourceGVersion       = "gversion"
	MultiVersionSourceInstallInfo    = "install_info"
	MultiVersionSourceInstallSummary = "install_summary"
	MultiVersionSourceUnknown        = "unknown"

	maxMultiVersionMetadataSize  = 1 << 20
	maxMultiVersionCommandOutput = 64 << 10
	maxMultiVersionLength        = 64
	multiVersionProbeTimeout     = 3 * time.Second
	multiVersionWaitDelay        = 250 * time.Millisecond
)

// MultiVersion is the sanitized result of inspecting one MULTI IDE
// installation. Path identifies only the local evidence file/tool and is not
// included in configuration binding.
type MultiVersion struct {
	Product string `json:"product"`
	Version string `json:"version"`
	Source  string `json:"source"`
	Path    string `json:"path,omitempty"`
}

// ResolveOptions is the extensible form of ResolveMultiVersion. The ordinary
// functions are metadata-only; callers that need to execute the installation's
// gversion utility must opt in with AllowExecutableProbe or use the explicitly
// named ProbeMultiVersion helpers.
type ResolveOptions struct {
	Installation  string
	Executable    string
	Requested     string
	RequireDetect bool
	// AllowExecutableProbe permits running the installation's gversion tool.
	// Configuration loading leaves this false so `check`, `status`, and other
	// config-bound commands only read metadata. Explicit version-detection
	// commands may opt in when an installation has no textual release record.
	AllowExecutableProbe bool
}

// MultiVersionError reports a failed probe without embedding unbounded or
// potentially sensitive command output.
type MultiVersionError struct {
	Installation string
	Reason       string
	MarkerFound  bool
	Err          error
}

func (e *MultiVersionError) Error() string {
	if e == nil {
		return "multi version probe failed"
	}
	if e.Reason == "" {
		if e.Err != nil {
			return fmt.Sprintf("multi version probe for %q: %v", e.Installation, e.Err)
		}
		return fmt.Sprintf("multi version probe for %q failed", e.Installation)
	}
	if e.Err != nil {
		return fmt.Sprintf("multi version probe for %q: %s: %v", e.Installation, e.Reason, e.Err)
	}
	return fmt.Sprintf("multi version probe for %q: %s", e.Installation, e.Reason)
}

func (e *MultiVersionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

var (
	// ErrMultiVersionUnavailable means no usable version evidence was found.
	// Callers may retain legacy behaviour when MarkerFound is false.
	ErrMultiVersionUnavailable = errors.New("version evidence is unavailable")
	ErrMultiVersionMismatch    = errors.New("requested version does not match the installed version")
	errMultiVersionOutputLimit = errors.New("gversion output exceeds size limit")
)

var (
	multiVersionShape = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)+)((?:[a-z][a-z0-9._-]*|[-+][a-z0-9][a-z0-9._-]*)?)$`)
	// gversion's stable identity is one complete banner line. Keep only
	// optional horizontal whitespace before the vendor marker; accepting
	// arbitrary text before it would let unrelated command output masquerade as
	// a release identity.
	multiBannerShape    = regexp.MustCompile(`(?i)^[ \t]*Green Hills Software,[ \t]+MULTI(?:[ \t]+IDE)?[ \t]+v?([0-9]+(?:\.[0-9]+)+[a-z0-9._-]*)[ \t]*$`)
	productReleaseShape = regexp.MustCompile(`(?i)^Product[ \t]+Release[ \t]+Name[ \t]*:[ \t]*MULTI(?:[ \t]+IDE)?[ \t]+v?([0-9]+(?:\.[0-9]+)+[a-z0-9._-]*)[ \t]*$`)
	installSummaryShape = regexp.MustCompile(`(?i)^Installing[ \t]+"[ \t]*MULTI(?:[ \t]+IDE)?[ \t]+v?([0-9]+(?:\.[0-9]+)+[a-z0-9._-]*)[ \t]*"[ \t]+to[ \t]+"[^"\r\n]*"[ \t]*$`)
	bareReleaseShape    = regexp.MustCompile(`(?i)^MULTI(?:[ \t]+IDE)?[ \t]+v?([0-9]+(?:\.[0-9]+)+[a-z0-9._-]*)[ \t]*$`)
	productShape        = regexp.MustCompile(`(?im)^\s*product\s*=\s*"([^"]+)"`)
)

// DetectMultiVersion inspects installation metadata. It never executes a
// program from the installation directory; callers that need gversion must
// opt in with ProbeMultiVersion.
func DetectMultiVersion(ctx context.Context, installation string, executable ...string) (MultiVersion, error) {
	selected := ""
	if len(executable) != 0 {
		selected = executable[0]
	}
	return detectMultiVersion(ctx, ResolveOptions{Installation: installation, Executable: selected, RequireDetect: true})
}

// ResolveMultiVersion discovers the installed version and, when requested is
// non-empty, verifies it. "auto" is an explicit request to require usable
// discovery. It only reads installation metadata; use ProbeResolveMultiVersion
// when gversion execution is explicitly acceptable. The optional executable
// follows DetectMultiVersion's convention.
func ResolveMultiVersion(ctx context.Context, installation, requested string, executable ...string) (MultiVersion, error) {
	selected := ""
	if len(executable) != 0 {
		selected = executable[0]
	}
	return ResolveMultiVersionWithOptions(ctx, ResolveOptions{
		Installation:  installation,
		Executable:    selected,
		Requested:     requested,
		RequireDetect: true,
	})
}

// ProbeMultiVersion is the explicit executable-probe form of
// DetectMultiVersion. It may run gversion.exe/gversion from installation.
func ProbeMultiVersion(ctx context.Context, installation string, executable ...string) (MultiVersion, error) {
	selected := ""
	if len(executable) != 0 {
		selected = executable[0]
	}
	return detectMultiVersion(ctx, ResolveOptions{
		Installation:         installation,
		Executable:           selected,
		RequireDetect:        true,
		AllowExecutableProbe: true,
	})
}

// ProbeResolveMultiVersion is the explicit executable-probe form of
// ResolveMultiVersion. It may run gversion.exe/gversion from installation and
// then checks a non-empty requested release constraint.
func ProbeResolveMultiVersion(ctx context.Context, installation, requested string, executable ...string) (MultiVersion, error) {
	selected := ""
	if len(executable) != 0 {
		selected = executable[0]
	}
	return ResolveMultiVersionWithOptions(ctx, ResolveOptions{
		Installation:         installation,
		Executable:           selected,
		Requested:            requested,
		RequireDetect:        true,
		AllowExecutableProbe: true,
	})
}

// Detect and Resolve are concise metadata-only aliases for integrations that
// do not need the longer names. They intentionally retain the same variadic
// signature.
func Detect(ctx context.Context, installation string, executable ...string) (MultiVersion, error) {
	return DetectMultiVersion(ctx, installation, executable...)
}

func Resolve(ctx context.Context, installation, requested string, executable ...string) (MultiVersion, error) {
	return ResolveMultiVersion(ctx, installation, requested, executable...)
}

// DetectMULTIVersion and ResolveMULTIVersion preserve the all-caps product
// spelling for callers that use the vendor name in exported symbols.
func DetectMULTIVersion(ctx context.Context, installation string, executable ...string) (MultiVersion, error) {
	return DetectMultiVersion(ctx, installation, executable...)
}

func ResolveMULTIVersion(ctx context.Context, installation, requested string, executable ...string) (MultiVersion, error) {
	return ResolveMultiVersion(ctx, installation, requested, executable...)
}

// ResolveMultiVersionWithOptions is useful to configuration loaders which
// need to distinguish an omitted version from an explicit "auto" request. If
// RequireDetect is false and no release evidence exists, it returns a result
// with an empty Version and MultiVersionSourceUnknown instead of failing.
func ResolveMultiVersionWithOptions(ctx context.Context, options ResolveOptions) (MultiVersion, error) {
	requested, explicit, err := normalizeRequestedMultiVersion(options.Requested)
	if err != nil {
		return MultiVersion{}, err
	}
	options.Requested = requested
	if strings.EqualFold(strings.TrimSpace(options.Requested), MultiVersionAuto) {
		explicit = true
	}
	result, err := detectMultiVersion(ctx, options)
	if err != nil {
		return MultiVersion{}, err
	}
	if result.Version == "" {
		if options.RequireDetect || explicit {
			return MultiVersion{}, &MultiVersionError{Installation: options.Installation, Reason: "no MULTI release version was found", MarkerFound: true, Err: ErrMultiVersionUnavailable}
		}
		return result, nil
	}
	if requested == "" || strings.EqualFold(requested, MultiVersionAuto) {
		return result, nil
	}
	if !EquivalentMultiVersion(result.Version, requested) {
		return MultiVersion{}, &MultiVersionError{
			Installation: options.Installation,
			Reason:       fmt.Sprintf("requested %q, detected %q", requested, result.Version),
			MarkerFound:  true,
			Err:          ErrMultiVersionMismatch,
		}
	}
	return result, nil
}

// NormalizeMultiVersion canonicalizes a release string while retaining a
// vendor suffix such as the trailing "d" in MULTI 7.1.6d.
func NormalizeMultiVersion(raw string) (string, error) {
	value := strings.TrimSpace(strings.Trim(raw, "\"'"))
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(value, "v"), "V"))
	if len(value) > maxMultiVersionLength {
		return "", fmt.Errorf("invalid MULTI version %q (exceeds %d characters)", boundedVersionInput(raw), maxMultiVersionLength)
	}
	match := multiVersionShape.FindStringSubmatch(value)
	if match == nil {
		return "", fmt.Errorf("invalid MULTI version %q (want e.g. 8.1.4)", boundedVersionInput(raw))
	}
	return match[1] + strings.ToLower(match[2]), nil
}

func boundedVersionInput(raw string) string {
	const maxShown = 128
	if len(raw) <= maxShown {
		return raw
	}
	return raw[:maxShown] + "..."
}

// EquivalentMultiVersion compares normalized releases and applies the one
// verified vendor exception: gversion reports 7.1.6 while install_info records
// 7.1.6d. Other release qualifiers remain exact.
func EquivalentMultiVersion(left, right string) bool {
	a, errA := NormalizeMultiVersion(left)
	b, errB := NormalizeMultiVersion(right)
	if errA != nil || errB != nil {
		return false
	}
	if a == b {
		return true
	}
	baseA, suffixA := splitMultiVersionSuffix(a)
	baseB, suffixB := splitMultiVersionSuffix(b)
	if baseA != baseB {
		return false
	}
	// MULTI 7.1.6's gversion banner omits the trailing `d` that the
	// installation metadata retains. That one known omission is compatible;
	// beta, release-candidate, and other qualifiers (including an unverified
	// `d` suffix on another release) are distinct releases and must never be
	// accepted by an exact version constraint.
	if baseA != "7.1.6" {
		return false
	}
	return (suffixA == "" && suffixB == "d") || (suffixA == "d" && suffixB == "")
}

func splitMultiVersionSuffix(value string) (string, string) {
	for i, r := range value {
		if !((r >= '0' && r <= '9') || r == '.') {
			return value[:i], value[i:]
		}
	}
	return value, ""
}

func normalizeRequestedMultiVersion(raw string) (string, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false, nil
	}
	if strings.EqualFold(value, MultiVersionAuto) {
		return MultiVersionAuto, true, nil
	}
	normalized, err := NormalizeMultiVersion(value)
	return normalized, true, err
}

func detectMultiVersion(ctx context.Context, options ResolveOptions) (MultiVersion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return MultiVersion{}, &MultiVersionError{Installation: strings.TrimSpace(options.Installation), Reason: "probe canceled", Err: err}
	}
	installation := strings.TrimSpace(options.Installation)
	if installation == "" {
		return MultiVersion{}, &MultiVersionError{Reason: "installation is required", Err: ErrMultiVersionUnavailable}
	}
	installation, err := filepath.Abs(installation)
	if err != nil {
		return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "resolve installation path", Err: err}
	}
	installation = filepath.Clean(installation)
	info, err := os.Stat(installation)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("is not a directory")
		}
		return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "installation is unavailable", Err: err}
	}

	markerFound := false
	product := ""
	aboutPath := filepath.Join(installation, ".about")
	if data, found, readErr := readVersionMetadata(aboutPath); found {
		if readErr != nil {
			return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "read .about", MarkerFound: true, Err: readErr}
		}
		product = parseAboutProduct(data)
		if product == "" {
			return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: ".about has no product marker", MarkerFound: true, Err: ErrMultiVersionUnavailable}
		}
		if !isMultiProduct(product) {
			return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: fmt.Sprintf("product %q is not MULTI IDE", product), MarkerFound: true}
		}
		product = "MULTI IDE"
	}

	// Textual release records are safe to inspect during configuration loading
	// and are sufficient for the supported MULTI installations. Validate every
	// record that is present, even when an executable probe is also available;
	// silently ignoring a damaged or conflicting marker could select the wrong
	// installation.
	configuredTarget, configured, targetErr := versionTarget(installation, options.Executable)
	if targetErr != nil {
		return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "validate executable", MarkerFound: markerFound, Err: targetErr}
	}

	metadata, metadataFound, metadataErr := readReleaseMetadata(installation)
	markerFound = markerFound || metadataFound
	if metadataErr != nil {
		return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "read MULTI release metadata", MarkerFound: true, Err: metadataErr}
	}
	if err := ctx.Err(); err != nil {
		return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "probe canceled", MarkerFound: markerFound, Err: err}
	}
	if len(metadata) > 0 && !options.AllowExecutableProbe {
		if err := ctx.Err(); err != nil {
			return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "probe canceled", MarkerFound: markerFound, Err: err}
		}
		if product == "" {
			return metadata[0], nil
		}
		metadata[0].Product = product
		return metadata[0], nil
	}

	// gversion is the authoritative release probe. It reads the target binary;
	// neither the target nor the MULTI GUI is started by this code.
	if options.AllowExecutableProbe {
		probeCtx, cancelProbe := context.WithTimeout(ctx, multiVersionProbeTimeout)
		defer cancelProbe()
		targets := []string(nil)
		if configured {
			targets = []string{configuredTarget}
		} else {
			targets = defaultVersionTargets(installation)
			// gversion prints its product banner even without a file argument
			// (the vendor utility exits non-zero in that case). Keep a no-argument
			// attempt for stripped installations that do not retain multi.exe.
			if len(targets) == 0 {
				targets = []string{""}
			}
		}
		for _, tool := range []string{filepath.Join(installation, "gversion.exe"), filepath.Join(installation, "gversion")} {
			if !regularPath(tool) {
				continue
			}
			markerFound = true
			for _, target := range targets {
				if err := ctx.Err(); err != nil {
					return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "probe canceled", MarkerFound: markerFound, Err: err}
				}
				stdout, stderr, runErr := runVersionTool(probeCtx, tool, target, installation)
				// Check cancellation before considering output. A valid banner from
				// a process that exceeded the deadline is not a successful probe.
				if err := ctx.Err(); err != nil {
					return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "probe canceled", MarkerFound: markerFound, Err: err}
				}
				if probeCtx.Err() == nil && !errors.Is(runErr, errMultiVersionOutputLimit) {
					// A target-qualified probe is valid only when gversion exited
					// successfully. Some vendor builds print a banner before
					// reporting a target error; accepting that partial output would
					// turn a failed executable check into false identity evidence.
					var version string
					var ok bool
					if runErr == nil {
						version, ok = parseTrustedMultiBanner(stdout, target)
					}
					// gversion emits its product banner on stderr when invoked
					// without a file argument, alongside its usage text and a
					// non-zero exit status. That fallback is accepted only for
					// the deliberately empty-target attempt; target-prefixed
					// probes continue to use stdout exclusively.
					if !ok && target == "" {
						version, ok = parseTrustedMultiBanner(stderr)
					}
					if ok {
						candidate := MultiVersion{Product: product, Version: version, Source: MultiVersionSourceGVersion, Path: tool}
						if candidate.Product == "" {
							candidate.Product = "MULTI IDE"
						}
						if err := reconcileMetadata(&candidate, metadata); err != nil {
							return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "MULTI release metadata conflicts with gversion", MarkerFound: true, Err: err}
						}
						return candidate, nil
					}
				}
				if runErr != nil {
					// A timeout local to one gversion invocation is recoverable;
					// cancellation of the caller is not.
					if probeCtx.Err() != nil {
						break
					}
					continue
				}
			}
			if probeCtx.Err() != nil {
				break
			}
		}
		if configured {
			// A missing gversion binary is not itself corruption when a valid
			// textual record is available. Once a tool was present and the
			// explicitly selected target could not be identified, however, do not
			// hide a typo or mismatched executable behind another fallback.
			toolPresent := regularPath(filepath.Join(installation, "gversion.exe")) || regularPath(filepath.Join(installation, "gversion"))
			if toolPresent {
				return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "gversion could not identify the requested executable", MarkerFound: true, Err: ErrMultiVersionUnavailable}
			}
		}
	}

	// If an executable probe was not requested (or was unavailable), use the
	// already-validated textual record. This path is deliberately deterministic
	// and does not invoke anything from the installation directory.
	if len(metadata) > 0 {
		if err := ctx.Err(); err != nil {
			return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "probe canceled", MarkerFound: markerFound, Err: err}
		}
		if product != "" {
			metadata[0].Product = product
		}
		return metadata[0], nil
	}

	// product_list/readme are useful identity hints for a real installation,
	// but do not contain a stable release string and therefore cannot be trusted
	// as a version value or treated as damaged release evidence. An omitted
	// version remains compatible with such stripped installations; an explicit
	// version or "auto" still requires a real release record above.
	if err := ctx.Err(); err != nil {
		return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "probe canceled", MarkerFound: markerFound, Err: err}
	}
	if !markerFound && !options.RequireDetect {
		// A legacy/test installation may be structurally usable while carrying
		// no release evidence at all. Keep this non-fatal only for callers that
		// explicitly allow an unknown version; an explicit detector request must
		// still fail closed below.
		return MultiVersion{Product: product, Source: MultiVersionSourceUnknown}, nil
	}
	if !markerFound {
		return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "no MULTI release metadata found", MarkerFound: false, Err: ErrMultiVersionUnavailable}
	}
	return MultiVersion{}, &MultiVersionError{Installation: installation, Reason: "MULTI installation metadata has no parseable release version", MarkerFound: true, Err: ErrMultiVersionUnavailable}
}

// readReleaseMetadata parses all known textual release records. A present but
// unreadable or malformed record is an error rather than a reason to trust a
// different source silently.
func readReleaseMetadata(installation string) ([]MultiVersion, bool, error) {
	metadata := make([]MultiVersion, 0, 2)
	foundAny := false
	for _, candidate := range []struct {
		name   string
		source string
	}{
		{name: filepath.Join(installation, "defaults", "licensing", "install_info.txt"), source: MultiVersionSourceInstallInfo},
		{name: filepath.Join(installation, "install_summary.txt"), source: MultiVersionSourceInstallSummary},
	} {
		data, found, readErr := readVersionMetadata(candidate.name)
		if !found {
			continue
		}
		foundAny = true
		if readErr != nil {
			return nil, true, fmt.Errorf("%s: %w", candidate.name, readErr)
		}
		parsedProduct, version, ok := parseReleaseMetadata(data)
		if !ok {
			return nil, true, fmt.Errorf("%s has no parseable MULTI release", candidate.name)
		}
		if parsedProduct != "" && !isMultiProduct(parsedProduct) {
			return nil, true, fmt.Errorf("%s identifies product %q", candidate.name, parsedProduct)
		}
		current := MultiVersion{Product: parsedProduct, Version: version, Source: candidate.source, Path: candidate.name}
		if len(metadata) > 0 && !EquivalentMultiVersion(metadata[0].Version, current.Version) {
			return nil, true, fmt.Errorf("conflicting releases %q and %q", metadata[0].Version, current.Version)
		}
		if len(metadata) == 0 || (strings.HasSuffix(current.Version, "d") && !strings.HasSuffix(metadata[0].Version, "d")) {
			metadata = append(metadata[:0], current)
		}
	}
	return metadata, foundAny, nil
}

func reconcileMetadata(candidate *MultiVersion, metadata []MultiVersion) error {
	for _, marker := range metadata {
		if !EquivalentMultiVersion(candidate.Version, marker.Version) {
			return fmt.Errorf("gversion reported %q, metadata reported %q", candidate.Version, marker.Version)
		}
		if strings.HasSuffix(marker.Version, "d") && !strings.HasSuffix(candidate.Version, "d") {
			candidate.Version = marker.Version
		}
	}
	return nil
}

func parseTrustedMultiBanner(data []byte, target ...string) (string, bool) {
	if !bytes.Contains(bytes.ToLower(data), []byte("green hills software")) {
		return "", false
	}
	return parseMultiBanner(data, target...)
}

func versionTarget(installation, configured string) (string, bool, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", false, nil
	}
	path := configured
	if !filepath.IsAbs(path) {
		path = filepath.Join(installation, path)
	}
	path = filepath.Clean(path)
	if !within(installation, path) {
		return "", true, errors.New("must be inside the MULTI installation")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", true, err
	}
	if !info.Mode().IsRegular() {
		return "", true, errors.New("must be a regular file")
	}

	installationTarget, err := filepath.EvalSymlinks(installation)
	if err != nil {
		return "", true, fmt.Errorf("resolve installation links: %w", err)
	}
	installationTarget, err = filepath.Abs(installationTarget)
	if err != nil {
		return "", true, fmt.Errorf("resolve installation path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", true, fmt.Errorf("resolve executable link: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", true, fmt.Errorf("resolve executable link path: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if !within(filepath.Clean(installationTarget), resolved) {
		return "", true, errors.New("resolved executable must be inside the MULTI installation")
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return "", true, fmt.Errorf("stat resolved executable: %w", err)
	}
	if !resolvedInfo.Mode().IsRegular() {
		return "", true, errors.New("resolved executable must be a regular file")
	}
	return resolved, true, nil
}

func defaultVersionTargets(installation string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, 2)
	for _, path := range []string{filepath.Join(installation, "multi.exe"), filepath.Join(installation, "mpythonrun.exe")} {
		path = filepath.Clean(path)
		key := strings.ToLower(path)
		if _, exists := seen[key]; exists || !regularPath(path) {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, path)
	}
	return result
}

func regularPath(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func readVersionMetadata(path string) ([]byte, bool, error) {
	// Reject links before opening, then bind the size check and bounded read to
	// the same descriptor. This avoids a replacement/growth race between a
	// path-size check and os.ReadFile.
	lstat, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if !lstat.Mode().IsRegular() {
		return nil, true, errors.New("is not a regular file")
	}
	if lstat.Size() > maxMultiVersionMetadataSize {
		return nil, true, errors.New("exceeds metadata size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, true, err
	}
	if !info.Mode().IsRegular() {
		return nil, true, errors.New("is not a regular file")
	}
	if !os.SameFile(lstat, info) {
		return nil, true, errors.New("changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMultiVersionMetadataSize+1))
	if err != nil {
		return nil, true, err
	}
	if len(data) > maxMultiVersionMetadataSize {
		return nil, true, errors.New("exceeds metadata size limit")
	}
	return data, true, nil
}

func parseAboutProduct(data []byte) string {
	match := productShape.FindSubmatch(data)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}

func isMultiProduct(product string) bool {
	value := strings.ToLower(strings.TrimSpace(product))
	return value == "multi ide" || value == "multi"
}

func parseMultiBanner(data []byte, target ...string) (string, bool) {
	// Parse one complete vendor banner line. A substring match would accept a
	// valid-looking prefix such as `MULTI v8.1.4/garbage`.
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	version := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if len(target) > 0 {
			if stripped, ok := stripVersionTargetPrefix(line, target[0]); ok {
				line = stripped
			}
		}
		match := multiBannerShape.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		candidate, err := NormalizeMultiVersion(match[1])
		if err != nil {
			continue
		}
		if version != "" {
			if !EquivalentMultiVersion(version, candidate) {
				return "", false
			}
			if strings.HasSuffix(candidate, "d") && !strings.HasSuffix(version, "d") {
				version = candidate
			}
			continue
		}
		version = candidate
	}
	return version, version != ""
}

// stripVersionTargetPrefix accepts the path decoration emitted by gversion
// when a file argument is supplied (for example,
// `D:\\ghs\\multi_814\\multi.exe: Green Hills Software, MULTI v8.1.4`).
// The prefix is trusted only when it identifies the exact target passed to
// this invocation; arbitrary log prefixes remain invalid evidence.
func stripVersionTargetPrefix(line, target string) (string, bool) {
	marker := "green hills software,"
	// The marker is ASCII. Search byte-for-byte with ASCII case folding rather
	// than lower-casing the whole line: Unicode case mappings can change the
	// byte length of a path before the marker, making an index computed from the
	// folded string unsafe to apply to the original line.
	index := indexASCIIEqualFold(line, marker)
	if index <= 0 {
		return "", false
	}
	if index+len(marker) > len(line) {
		return "", false
	}
	prefix := strings.TrimSpace(line[:index])
	if !strings.HasSuffix(prefix, ":") {
		return "", false
	}
	prefix = strings.TrimSpace(strings.TrimSuffix(prefix, ":"))
	prefix = strings.TrimSpace(strings.Trim(prefix, "\"'"))
	target = strings.TrimSpace(strings.Trim(target, "\"'"))
	if prefix == "" || target == "" {
		return "", false
	}
	return line[index:], sameVersionTargetPath(prefix, target)
}

func indexASCIIEqualFold(value, needle string) int {
	if needle == "" {
		return 0
	}
	if len(value) < len(needle) {
		return -1
	}
	for i := 0; i <= len(value)-len(needle); i++ {
		if asciiEqualFold(value[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

func asciiEqualFold(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := 0; i < len(left); i++ {
		l, r := left[i], right[i]
		if l >= 'A' && l <= 'Z' {
			l += 'a' - 'A'
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if l != r {
			return false
		}
	}
	return true
}

func sameVersionTargetPath(left, right string) bool {
	left = canonicalWindowsPathIdentity(left)
	right = canonicalWindowsPathIdentity(right)
	return left != "" && left == right
}

func parseReleaseMetadata(data []byte) (string, string, bool) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	var version string
	found := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		var match []string
		switch {
		case productReleaseShape.MatchString(line):
			match = productReleaseShape.FindStringSubmatch(line)
		case installSummaryShape.MatchString(line):
			match = installSummaryShape.FindStringSubmatch(line)
		case bareReleaseShape.MatchString(line):
			match = bareReleaseShape.FindStringSubmatch(line)
		default:
			// A recognized release key that does not identify MULTI is a
			// positive non-MULTI marker (for example Compiler 2024.1.4).
			lower := strings.ToLower(line)
			if strings.HasPrefix(lower, "product release name:") || strings.HasPrefix(lower, "installing ") {
				return "", "", false
			}
			continue
		}
		if len(match) != 2 {
			return "", "", false
		}
		candidate, err := NormalizeMultiVersion(match[1])
		if err != nil {
			return "", "", false
		}
		if found && !EquivalentMultiVersion(version, candidate) {
			return "", "", false
		}
		if !found || (strings.HasSuffix(candidate, "d") && !strings.HasSuffix(version, "d")) {
			version = candidate
		}
		found = true
	}
	if !found {
		return "", "", false
	}
	return "MULTI IDE", version, true
}

func runVersionTool(ctx context.Context, tool, target, directory string) (stdoutOutput, stderrOutput []byte, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, multiVersionProbeTimeout)
	defer cancel()
	args := []string(nil)
	if strings.TrimSpace(target) != "" {
		args = []string{target}
	}
	command := exec.CommandContext(probeCtx, tool, args...)
	// A vendor helper may leave descendants holding the stdout/stderr pipe
	// after its own process is killed at the context deadline. WaitDelay keeps a
	// diagnostic probe from hanging forever on those inherited handles.
	command.WaitDelay = multiVersionWaitDelay
	command.Dir = directory
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	// Both streams are bounded so a broken vendor tool cannot consume
	// unbounded memory. Callers normally use stdout as evidence; the only
	// stderr exception is the explicit no-argument gversion fallback above.
	stdoutOutput = append([]byte(nil), stdout.Bytes()...)
	stderrOutput = append([]byte(nil), stderr.Bytes()...)
	if stdout.tooLarge || stderr.tooLarge {
		return stdoutOutput, stderrOutput, errMultiVersionOutputLimit
	}
	if probeCtx.Err() != nil {
		return stdoutOutput, stderrOutput, probeCtx.Err()
	}
	if err != nil {
		return stdoutOutput, stderrOutput, err
	}
	return stdoutOutput, stderrOutput, nil
}

type boundedBuffer struct {
	bytes.Buffer
	tooLarge bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	remaining := maxMultiVersionCommandOutput - b.Len()
	if remaining <= 0 {
		b.tooLarge = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.tooLarge = true
		return len(data), nil
	}
	return b.Buffer.Write(data)
}
