package svcmgr

import (
	"fmt"
	"os"
	"strings"
)

func UpdateClientKey(layout ServiceLayout, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("client key must not be empty")
	}
	if _, err := os.Stat(layout.EnvPath); err != nil {
		return fmt.Errorf("stat client env %s: %w", layout.EnvPath, err)
	}
	if err := setEnvFileValue(layout.EnvPath, "NETSGO_KEY", key); err != nil {
		return fmt.Errorf("update client key in %s: %w", layout.EnvPath, err)
	}
	if err := repairEnvFileOwnership(layout.EnvPath); err != nil {
		return fmt.Errorf("repair client env ownership %s: %w", layout.EnvPath, err)
	}
	return nil
}
