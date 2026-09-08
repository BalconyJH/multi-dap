package multi

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tacrolimus/multi-dap/internal/core/source"
)

const (
	sourceListHeader   = "--------  File names  --------"
	maxSourceListBytes = 1 << 20
	maxSourceListLines = 4096
)

// MULTI right-aligns the decimal row number to a five-character field. p12
// observed only the 1-, 2-, and 3-digit forms below; do not accept a wider
// grammar until a bounded capture establishes it.
var sourceListRow = regexp.MustCompile(`^((?: {4}[0-9]| {3}[0-9]{2}| {2}[0-9]{3})): ([A-Za-z]:\\[A-Za-z0-9_.\\-]+\.[A-Za-z0-9_+\-]+)$`)

// ErrSourceListLossy and ErrSourceListFormat are request-scoped diagnostics.
// They deliberately contain no target output or source path. Callers retain
// PresenceUnknown and must not replace or clear physical breakpoints.
var (
	ErrSourceListLossy  = errors.New("multi: source-file listing is lossy")
	ErrSourceListFormat = errors.New("multi: source-file listing has unsupported format")
)

// SourceListProbe implements the documented read-only `l f` command. A
// listing is a per-Resolve snapshot, never a cache across breakpoint requests:
// a warm MULTI window can reload the same ELF without changing its path.
type SourceListProbe struct{}

func NewSourceListProbe() *SourceListProbe { return &SourceListProbe{} }

func (p *SourceListProbe) ProbeSource(ctx context.Context, driver *Driver, topology *Topology, core int, identity source.Identity) (source.Presence, error) {
	if p == nil {
		return source.PresenceUnknown, errors.New("multi: nil source-list probe")
	}
	if err := source.ValidateTargetIdentity(identity); err != nil {
		return source.PresenceUnknown, fmt.Errorf("multi: source identity: %w", err)
	}
	result, err := topology.routed(ctx, driver, core, "l f")
	if err != nil {
		return source.PresenceUnknown, err
	}
	if result.RawLossy {
		return source.PresenceUnknown, ErrSourceListLossy
	}
	files, err := parseSourceFileListing(result.Raw)
	if err != nil {
		return source.PresenceUnknown, fmt.Errorf("multi: parse source-file listing: %w", ErrSourceListFormat)
	}
	if _, found := files[identity.Key]; found {
		return source.PresencePresent, nil
	}
	return source.PresenceAbsent, nil
}

func parseSourceFileListing(raw string) (map[string]struct{}, error) {
	if len(raw) == 0 || len(raw) > maxSourceListBytes {
		return nil, ErrSourceListFormat
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 2 || len(lines) > maxSourceListLines {
		return nil, ErrSourceListFormat
	}
	for i := range lines {
		if strings.HasSuffix(lines[i], "\r") {
			lines[i] = strings.TrimSuffix(lines[i], "\r")
		}
		if strings.ContainsRune(lines[i], '\r') || len(lines[i]) > 16<<10 {
			return nil, ErrSourceListFormat
		}
	}
	if lines[0] != sourceListHeader {
		return nil, ErrSourceListFormat
	}
	files := make(map[string]struct{}, len(lines)-1)
	for expected, line := range lines[1:] {
		match := sourceListRow.FindStringSubmatch(line)
		if match == nil {
			return nil, ErrSourceListFormat
		}
		index, err := strconv.Atoi(strings.TrimSpace(match[1]))
		if err != nil || index != expected {
			return nil, ErrSourceListFormat
		}
		key, err := canonicalSourceListPath(match[2])
		if err != nil {
			return nil, err
		}
		// The listing is evidence of canonical source membership, not a
		// source of raw path spelling for a later command. Once both rows have
		// independently passed the exact grammar and path checks, a Windows
		// case-fold duplicate is idempotent set membership.
		files[key] = struct{}{}
	}
	return files, nil
}

func canonicalSourceListPath(path string) (string, error) {
	if strings.Contains(path, "/") {
		return "", ErrSourceListFormat
	}
	if len(path) < 4 || !isASCIIAlpha(path[0]) || path[1:3] != `:\` {
		return "", ErrSourceListFormat
	}
	parts := strings.Split(path[3:], `\`)
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", ErrSourceListFormat
		}
	}
	return source.Canonicalize(path), nil
}
