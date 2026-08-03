package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ProviderConfig describes a single quota/plan provider instance.
type ProviderConfig struct {
	ID              string            `json:"id"`                    // stable identifier, e.g. "opencode-go"
	Name            string            `json:"name"`                  // display name
	Type            string            `json:"type"`                  // provider type, e.g. "opencode-go"
	Enabled         bool              `json:"enabled"`               // whether to query on refresh
	Workspace       string            `json:"workspace,omitempty"`   // e.g. wrk_...
	Cookie          string            `json:"cookie,omitempty"`      // session cookie (encrypted on disk, plaintext in memory)
	AlertThresholds map[string]int    `json:"alertThresholds"`       // window key -> percent threshold
	Detail          ProviderDetailCfg `json:"detail,omitempty"`      // optional extended fields
	SortOrder       int               `json:"sortOrder"`             // lower sorts first in the list and snap default
	DynamicSort     *bool             `json:"dynamicSort,omitempty"` // sort by recent quota change; nil = enabled
}

// ProviderDetailCfg holds provider specific knobs.
type ProviderDetailCfg struct {
	ShowUsageDetail bool `json:"showUsageDetail,omitempty"` // fetch usage history page too
	International   bool `json:"international,omitempty"`   // use the international host (z.ai) instead of bigmodel.cn
}

// AppConfig is the persisted application configuration.
type AppConfig struct {
	RefreshIntervalSec int              `json:"refreshIntervalSec"` // seconds between automatic refreshes
	NativeNotify       bool             `json:"nativeNotify"`       // emit OS notifications on alert
	EdgeDock           bool             `json:"edgeDock"`           // snap window to screen edges
	AlwaysOnTop        bool             `json:"alwaysOnTop"`        // keep window above others
	Opacity            float64          `json:"opacity"`            // 0..1 window transparency
	SnapProviderID     string           `json:"snapProviderID"`     // account shown by the edge snap bar ("" = first enabled)
	Providers          []ProviderConfig `json:"providers"`
}

// Default returns a sane starting configuration.
func Default() *AppConfig {
	return &AppConfig{
		RefreshIntervalSec: 300,
		NativeNotify:       true,
		EdgeDock:           true,
		AlwaysOnTop:        true,
		Opacity:            1.0,
		Providers: []ProviderConfig{
			{
				ID:      "opencode-go",
				Name:    "OpenCode Go",
				Type:    "opencode-go",
				Enabled: false,
				AlertThresholds: map[string]int{
					"5h":      80,
					"weekly":  80,
					"monthly": 80,
				},
				DynamicSort: boolPtr(true),
			},
		},
		// No default thresholds for other provider types — the settings UI
		// seeds them based on the type's registered window keys (see
		// providers.RegisterWindows). Saving back drops irrelevant keys.
	}
}

var (
	mu      sync.Mutex
	current *AppConfig
)

// Load reads the persisted config, creating a default one if none exists.
func Load() (*AppConfig, error) {
	mu.Lock()
	defer mu.Unlock()
	if current != nil {
		return current, nil
	}
	cfg := Default()
	data, err := os.ReadFile(filePath())
	if err != nil {
		if os.IsNotExist(err) {
			current = cfg
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.RefreshIntervalSec <= 0 {
		cfg.RefreshIntervalSec = 300
	}
	if cfg.Providers == nil {
		cfg.Providers = Default().Providers
	}
	// Decrypt cookies into memory; keep the raw value if it was stored plaintext.
	for i := range cfg.Providers {
		if cfg.Providers[i].Cookie == "" {
			continue
		}
		if plain, err := Decrypt(cfg.Providers[i].Cookie); err == nil && len(plain) > 0 {
			cfg.Providers[i].Cookie = string(plain)
		}
	}
	current = cfg
	return cfg, nil
}

// Get returns the in-memory config (loaded on first call).
func Get() *AppConfig {
	cfg, err := Load()
	if err != nil {
		cfg = Default()
	}
	return cfg
}

// ProviderConfig returns the configuration of a provider by ID.
func (c *AppConfig) ProviderConfig(id string) *ProviderConfig {
	for i := range c.Providers {
		if c.Providers[i].ID == id {
			return &c.Providers[i]
		}
	}
	return nil
}

// Save persists cfg and updates the in-memory copy.
// Cookie fields are encrypted before writing.
func Save(cfg *AppConfig) error {
	mu.Lock()
	defer mu.Unlock()

	out := clone(cfg)
	// Sort providers by SortOrder (stable) so the list order and the
	// snap-default account follow the user's numbering.
	sortByOrder(out.Providers)
	sortByOrder(cfg.Providers)
	for i := range out.Providers {
		// Normalize: strip a leading "auth=" prefix users often copy from the
		// DevTools Cookie header (the provider re-adds it when sending).
		out.Providers[i].Cookie = strings.TrimPrefix(out.Providers[i].Cookie, "auth=")
		enc, err := Encrypt([]byte(out.Providers[i].Cookie))
		if err != nil {
			return fmt.Errorf("encrypt cookie for %s: %w", out.Providers[i].ID, err)
		}
		out.Providers[i].Cookie = enc
	}
	// Keep the in-memory copy consistent (plaintext, no prefix).
	for i := range cfg.Providers {
		cfg.Providers[i].Cookie = strings.TrimPrefix(cfg.Providers[i].Cookie, "auth=")
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filePath(), data, 0o600); err != nil {
		return err
	}

	current = cfg
	return nil
}

func clone(cfg *AppConfig) *AppConfig {
	data, _ := json.Marshal(cfg)
	var out AppConfig
	_ = json.Unmarshal(data, &out)
	for i := range out.Providers {
		if out.Providers[i].AlertThresholds == nil {
			out.Providers[i].AlertThresholds = map[string]int{}
		}
	}
	return &out
}

// sortByOrder stably orders providers by SortOrder ascending; equal keys keep
// their relative order.
func sortByOrder(ps []ProviderConfig) {
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].SortOrder < ps[j].SortOrder })
}

func boolPtr(v bool) *bool {
	return &v
}
