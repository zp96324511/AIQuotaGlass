package config

import (
	"os"
	"path/filepath"
)

const dirEnv = "AQUOTA_CONFIG_DIR"

// Dir returns the directory used for the config file and other runtime data.
// It honours the AQUOTA_CONFIG_DIR environment variable (useful when the
// system drive is full), otherwise it falls back to the platform config dir.
func Dir() string {
	if d := os.Getenv(dirEnv); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "AIQuotaGlass")
}

func filePath() string {
	return filepath.Join(Dir(), "config.json")
}
