package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const maxConfigFileSize = 1 << 20

// Load reads one strict TOML project configuration and validates it relative
// to the configuration file's directory. It accepts only a regular file no
// larger than maxConfigFileSize so an accidental directory, device, or
// unbounded stream cannot become daemon input.
func Load(path string) (Validated, error) {
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

	var raw Config
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Validated{}, decodeError(configPath, err)
	}

	validated, err := raw.Validate(filepath.Dir(configPath))
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
