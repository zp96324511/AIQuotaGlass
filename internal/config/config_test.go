package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSaveStripsAuthPrefix verifies a cookie pasted with the DevTools "auth="
// prefix is normalized to the bare value when persisted.
func TestSaveStripsAuthPrefix(t *testing.T) {
	dir := t.TempDir()
	old := os.Getenv("AQUOTA_CONFIG_DIR")
	t.Setenv("AQUOTA_CONFIG_DIR", dir)
	os.Remove(filepath.Join(dir, "config.json"))
	// reset in-memory cache for a clean test
	mu.Lock()
	current = nil
	mu.Unlock()
	defer os.Setenv("AQUOTA_CONFIG_DIR", old)

	cfg := Default()
	cfg.Providers[0].Cookie = "auth=Fe26.2**abc"
	cfg.Providers[0].Enabled = true
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Get()
	if got.Providers[0].Cookie != "Fe26.2**abc" {
		t.Fatalf("in-memory cookie not normalized: %q", got.Providers[0].Cookie)
	}

	// Reload from disk (clear cache) and verify the round-trip strips too.
	mu.Lock()
	current = nil
	mu.Unlock()
	reloaded := Get()
	if reloaded.Providers[0].Cookie != "Fe26.2**abc" {
		t.Fatalf("cookie not stripped on reload: %q", reloaded.Providers[0].Cookie)
	}
}
