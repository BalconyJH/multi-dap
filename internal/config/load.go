package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const maxConfigFileSize = 1 << 20

// LoadOptions controls optional work performed after strict TOML validation.
// The zero value is deliberately metadata-only: loading an untrusted project
// file must not execute a program selected by that file.
type LoadOptions struct {
	AllowExecutableProbe bool
}

// Load reads one strict TOML project configuration and validates it relative
// to the configuration file's directory. It accepts only a regular file no
// larger than maxConfigFileSize so an accidental directory, device, or
// unbounded stream cannot become daemon input. MULTI release detection uses
// textual installation metadata and does not execute an installation tool.
func Load(path string) (Validated, error) {
	return LoadWithOptionsContext(context.Background(), path, LoadOptions{})
}

// LoadContext is the cancellable form of Load. It is useful to command
// frontends that need Ctrl-C to interrupt a version probe without changing the
// metadata-only default of Load.
func LoadContext(ctx context.Context, path string) (Validated, error) {
	return LoadWithOptionsContext(ctx, path, LoadOptions{})
}

// LoadWithExecutableProbe is the explicit opt-in form for callers that need to
// recover a release from an installation which has no textual release record.
// It may execute that installation's gversion utility; ordinary config-bound
// commands should use Load instead.
func LoadWithExecutableProbe(path string) (Validated, error) {
	return LoadWithExecutableProbeContext(context.Background(), path)
}

// LoadWithExecutableProbeContext is the explicit, cancellable opt-in form for
// loading a configuration that may execute its installation's gversion tool.
func LoadWithExecutableProbeContext(ctx context.Context, path string) (Validated, error) {
	return LoadWithOptionsContext(ctx, path, LoadOptions{AllowExecutableProbe: true})
}

// LoadWithOptions reads and validates one project configuration with the
// requested release-probe policy.
func LoadWithOptions(path string, options LoadOptions) (Validated, error) {
	return LoadWithOptionsContext(context.Background(), path, options)
}

// LoadWithOptionsContext is the context-aware implementation shared by the
// metadata-only and explicit executable-probe entry points.
func LoadWithOptionsContext(ctx context.Context, path string, options LoadOptions) (Validated, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Validated{}, err
	}
	configPath, err := absoluteConfigPath(path)
	if err != nil {
		return Validated{}, err
	}

	file, err := os.Open(configPath)
	if err != nil {
		return Validated{}, fmt.Errorf("open configuration %q: %w", configPath, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return Validated{}, fmt.Errorf("stat configuration %q: %w", configPath, err)
	}
	if !info.Mode().IsRegular() {
		return Validated{}, fmt.Errorf("open configuration %q: must be a regular file", configPath)
	}
	if info.Size() > maxConfigFileSize {
		return Validated{}, fmt.Errorf("read configuration %q: exceeds %d byte limit", configPath, maxConfigFileSize)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxConfigFileSize+1))
	if err != nil {
		return Validated{}, fmt.Errorf("read configuration %q: %w", configPath, err)
	}
	if len(data) > maxConfigFileSize {
		return Validated{}, fmt.Errorf("read configuration %q: exceeds %d byte limit", configPath, maxConfigFileSize)
	}
	if err := ctx.Err(); err != nil {
		return Validated{}, err
	}

	var raw Config
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Validated{}, decodeError(configPath, err)
	}
	// The parser-facing field remains an int for callers that construct Config
	// values directly. A parallel pointer decode records whether TOML actually
	// supplied the optional key, allowing Validate to reject an explicit zero
	// while retaining the omitted-field default.
	var presence struct {
		Multi struct {
			Version       *string `toml:"version"`
			PythonVersion *int    `toml:"python_version"`
		} `toml:"multi"`
	}
	presenceDecoder := toml.NewDecoder(bytes.NewReader(data))
	if err := presenceDecoder.Decode(&presence); err != nil {
		// The strict decode above already accepted the document. Keep this
		// defensive error distinct in case the dependency changes behavior.
		return Validated{}, decodeError(configPath, err)
	}
	raw.Multi.versionSet = presence.Multi.Version != nil
	raw.Multi.pythonVersionSet = presence.Multi.PythonVersion != nil

	validated, err := raw.validateWithOptions(ctx, filepath.Dir(configPath), options.AllowExecutableProbe)
	if err != nil {
		return Validated{}, fmt.Errorf("validate configuration %q: %w", configPath, err)
	}
	return validated, nil
}

func absoluteConfigPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("resolve configuration path: is required")
	}

	configPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve configuration path %q: %w", path, err)
	}
	configPath = filepath.Clean(configPath)

	info, err := os.Lstat(configPath)
	if err != nil {
		return "", fmt.Errorf("stat configuration %q: %w", configPath, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("open configuration %q: must be a regular file", configPath)
	}
	return configPath, nil
}

func decodeError(configPath string, err error) error {
	var unknown *toml.StrictMissingError
	if errors.As(err, &unknown) {
		if len(unknown.Errors) != 0 {
			row, column := unknown.Errors[0].Position()
			return fmt.Errorf("decode configuration %q: contains unknown fields (first at line %d, column %d)", configPath, row, column)
		}
		return fmt.Errorf("decode configuration %q: contains unknown fields", configPath)
	}
	var decode *toml.DecodeError
	if errors.As(err, &decode) {
		row, column := decode.Position()
		return fmt.Errorf("decode configuration %q: invalid TOML at line %d, column %d", configPath, row, column)
	}
	return fmt.Errorf("decode configuration %q: invalid TOML", configPath)
}
