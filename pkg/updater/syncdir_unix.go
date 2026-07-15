//go:build !windows

package updater

import (
	"fmt"
	"os"
)

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open destination directory: %w", err)
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return fmt.Errorf("sync destination directory: %w", err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close destination directory: %w", err)
	}
	return nil
}
