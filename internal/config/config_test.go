package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSaveSortsProvidersByOrder verifies Save reorders providers by SortOrder.
func TestSaveSortsProvidersByOrder(t *testing.T) {
	dir := t.TempDir()
	old := os.Getenv("AQUOTA_CONFIG_DIR")
	t.Setenv("AQUOTA_CONFIG_DIR", dir)
	mu.Lock()
	current = nil
	mu.Unlock()
	defer os.Setenv("AQUOTA_CONFIG_DIR", old)

	cfg := Default()
	cfg.Providers = []ProviderConfig{
		{ID: "c", Name: "C", SortOrder: 3, AlertThresholds: map[string]int{}},
		{ID: "a", Name: "A", SortOrder: 1, AlertThresholds: map[string]int{}},
		{ID: "b", Name: "B", SortOrder: 2, AlertThresholds: map[string]int{}},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Get().Providers
	if len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("providers not sorted by SortOrder: %v", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}
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
