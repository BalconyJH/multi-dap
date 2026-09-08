package daemon

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/Tacrolimus/multi-dap/internal/core/source"
	"github.com/Tacrolimus/multi-dap/internal/dap"
)

// SourceRewrite maps one debugger-side prefix to its client-workspace
// prefix. Both directions must be unambiguous because breakpoints travel
// client -> debugger while stack frames travel debugger -> client.
type SourceRewrite struct {
	DebugPrefix  string
	ClientPrefix string
}

// SourcePaths is the complete source identity boundary shared by the actor's
// inspection service and the DAP adapter. No other package compares or
// rewrites source path strings.
type SourcePaths interface {
	MapDebugPath(string) (source.Identity, error)
	FromClient(dap.InitializeArguments, dap.Source) (source.Identity, error)
	ToClient(dap.InitializeArguments, source.Identity) (*dap.Source, error)
}

type sourcePaths struct{ rewrites []SourceRewrite }

// NewSourcePaths validates and freezes bidirectional rewrite rules. Empty
// rules provide the direct path mapping used by projects whose build and
// workspace roots are identical.
func NewSourcePaths(rewrites []SourceRewrite) (SourcePaths, error) {
	normalized := make([]SourceRewrite, len(rewrites))
	for index, rewrite := range rewrites {
		debugPrefix := normalizePath(rewrite.DebugPrefix)
		clientPrefix := normalizePath(rewrite.ClientPrefix)
		if debugPrefix == "" || debugPrefix == "." || clientPrefix == "" || clientPrefix == "." {
			return nil, fmt.Errorf("daemon: source rewrite %d requires non-empty prefixes", index)
		}
		normalized[index] = SourceRewrite{DebugPrefix: debugPrefix, ClientPrefix: clientPrefix}
	}
	for left := range normalized {
		for right := left + 1; right < len(normalized); right++ {
			if pathsOverlap(normalized[left].DebugPrefix, normalized[right].DebugPrefix) {
				return nil, fmt.Errorf("daemon: source rewrite %d debugger prefix overlaps rewrite %d", right, left)
			}
			if pathsOverlap(normalized[left].ClientPrefix, normalized[right].ClientPrefix) {
				return nil, fmt.Errorf("daemon: source rewrite %d client prefix overlaps rewrite %d", right, left)
			}
		}
	}
	return &sourcePaths{rewrites: normalized}, nil
}

func (m *sourcePaths) MapDebugPath(debugPath string) (source.Identity, error) {
	debugPath = normalizePath(debugPath)
	if debugPath == "" || debugPath == "." {
		return source.Identity{}, errors.New("daemon: debugger source path is empty")
	}
	clientPath := debugPath
	for _, rewrite := range m.rewrites {
		if mapped, ok := replacePrefix(debugPath, rewrite.DebugPrefix, rewrite.ClientPrefix); ok {
			clientPath = mapped
			break
		}
	}
	return source.Identity{ClientPath: clientPath, DebugPath: debugPath, Key: source.Canonicalize(debugPath)}, nil
}

func (m *sourcePaths) FromClient(initialize dap.InitializeArguments, candidate dap.Source) (source.Identity, error) {
	raw := strings.TrimSpace(candidate.Path)
	if raw == "" {
		return source.Identity{}, errors.New("daemon: source path is empty")
	}
	clientPath, err := decodeClientPath(initialize.PathFormat, raw)
	if err != nil {
		return source.Identity{}, err
	}
	debugPath := clientPath
	for _, rewrite := range m.rewrites {
		if mapped, ok := replacePrefix(clientPath, rewrite.ClientPrefix, rewrite.DebugPrefix); ok {
			debugPath = mapped
			break
		}
	}
	return source.Identity{ClientPath: raw, DebugPath: debugPath, Key: source.Canonicalize(debugPath)}, nil
}

func (m *sourcePaths) ToClient(initialize dap.InitializeArguments, identity source.Identity) (*dap.Source, error) {
	clientPath := strings.TrimSpace(identity.ClientPath)
	if initialize.PathFormat == "uri" && strings.HasPrefix(strings.ToLower(clientPath), "file:") {
		if _, err := decodeClientPath("uri", clientPath); err != nil {
			return nil, err
		}
		return &dap.Source{Path: clientPath}, nil
	}
	if initialize.PathFormat != "uri" && strings.HasPrefix(strings.ToLower(clientPath), "file:") {
		var err error
		clientPath, err = decodeClientPath("uri", clientPath)
		if err != nil {
			return nil, err
		}
	} else {
		clientPath = normalizePath(clientPath)
	}
	if clientPath == "" || clientPath == "." {
		if identity.DebugPath == "" {
			return nil, nil
		}
		mapped, err := m.MapDebugPath(identity.DebugPath)
		if err != nil {
			return nil, err
		}
		clientPath = mapped.ClientPath
	}
	if initialize.PathFormat == "uri" {
		return &dap.Source{Path: encodeFileURI(clientPath)}, nil
	}
	return &dap.Source{Path: clientPath}, nil
}

func decodeClientPath(format, value string) (string, error) {
	if format != "uri" {
		value = normalizePath(value)
		if value == "" || value == "." {
			return "", errors.New("daemon: source path is empty")
		}
		return value, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "file") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("daemon: source URI %q is not a file URI", value)
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return normalizePath("//" + parsed.Host + parsed.Path), nil
	}
	local := parsed.Path
	if len(local) >= 3 && local[0] == '/' && local[2] == ':' {
		local = local[1:]
	}
	local = normalizePath(local)
	if local == "" || local == "." || local == "/" {
		return "", errors.New("daemon: source file URI has an empty path")
	}
	return local, nil
}

func encodeFileURI(value string) string {
	value = normalizePath(value)
	if strings.HasPrefix(value, "//") {
		withoutPrefix := strings.TrimPrefix(value, "//")
		host, rest, found := strings.Cut(withoutPrefix, "/")
		if found && host != "" {
			return (&url.URL{Scheme: "file", Host: host, Path: "/" + rest}).String()
		}
	}
	if len(value) >= 2 && value[1] == ':' {
		value = "/" + value
	}
	return (&url.URL{Scheme: "file", Path: value}).String()
}

func normalizePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	if value == "" {
		return ""
	}
	unc := strings.HasPrefix(value, "//")
	cleaned := path.Clean(value)
	if unc && !strings.HasPrefix(cleaned, "//") {
		cleaned = "/" + cleaned
	}
	return cleaned
}

func replacePrefix(value, from, to string) (string, bool) {
	suffix, ok := pathSuffix(value, from)
	if !ok {
		return "", false
	}
	if suffix == "" {
		return normalizePath(to), true
	}
	return normalizePath(strings.TrimSuffix(to, "/") + "/" + suffix), true
}

func pathSuffix(value, prefix string) (string, bool) {
	value, prefix = normalizePath(value), normalizePath(prefix)
	valueKey, prefixKey := source.Canonicalize(value), source.Canonicalize(prefix)
	if valueKey == prefixKey {
		return "", true
	}
	if !strings.HasPrefix(valueKey, strings.TrimSuffix(prefixKey, "/")+"/") {
		return "", false
	}
	valueParts := strings.Split(value, "/")
	prefixParts := strings.Split(prefix, "/")
	if len(valueParts) <= len(prefixParts) {
		return "", false
	}
	return strings.Join(valueParts[len(prefixParts):], "/"), true
}

func pathsOverlap(left, right string) bool {
	_, leftContainsRight := pathSuffix(right, left)
	_, rightContainsLeft := pathSuffix(left, right)
	return leftContainsRight || rightContainsLeft
}
