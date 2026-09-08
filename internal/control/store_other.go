//go:build !windows

package control

import (
	"fmt"
	"os"
)

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("control: create record directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("control: restrict record directory: %w", err)
	}
	return nil
}

func secureFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("control: restrict daemon record: %w", err)
	}
	return nil
}

func openStoreFile(path string) (*os.File, error) { return os.Open(path) }

func isTransientStoreReadError(error) bool { return false }

func replaceFile(source, target string) error { return os.Rename(source, target) }
