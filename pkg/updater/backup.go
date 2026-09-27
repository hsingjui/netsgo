package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const upgradeBackupPrefix = "netsgo.pre-upgrade-"

var upgradeBackupDir = "/var/lib/netsgo-upgrade"

func persistUpgradeBackup(srcPath, version string) (string, error) {
	if err := os.MkdirAll(upgradeBackupDir, 0o755); err != nil {
		return "", fmt.Errorf("create upgrade backup directory: %w", err)
	}

	backupPath := filepath.Join(upgradeBackupDir, upgradeBackupPrefix+backupVersionFileName(version))
	if err := replaceBinary(srcPath, backupPath); err != nil {
		return "", fmt.Errorf("retain old binary at %s: %w", backupPath, err)
	}

	entries, err := os.ReadDir(upgradeBackupDir)
	if err != nil {
		return "", fmt.Errorf("list upgrade backups: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(backupPath) || !strings.HasPrefix(entry.Name(), upgradeBackupPrefix) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(upgradeBackupDir, entry.Name())); err != nil {
			return "", fmt.Errorf("remove previous upgrade backup %s: %w", entry.Name(), err)
		}
	}
	if err := syncDirectory(upgradeBackupDir); err != nil {
		return "", fmt.Errorf("sync upgrade backup directory: %w", err)
	}
	return backupPath, nil
}

func backupVersionFileName(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "unknown"
	}
	version = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, version)
	version = strings.Trim(version, ".")
	if version == "" {
		return "unknown"
	}
	return version
}
