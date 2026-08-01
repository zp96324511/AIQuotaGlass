package providers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"aiquotaglass/internal/config"
)

// WindowStatus is the quota state of a single usage window (5h/weekly/monthly).
type WindowStatus struct {
	Key        string  `json:"key"`        // "5h", "weekly", "monthly"
	Label      string  `json:"label"`      // human readable label
	Percent    float64 `json:"percent"`    // 0..100 usage of the limit; -1 = unlimited (UI shows 无限)
	ResetInSec int64   `json:"resetInSec"` // seconds until the window resets; -1 = no auto reset (countdown hidden)
	Status     string  `json:"status"`     // "ok" or error marker
}

// UsageDetail holds optional statistics parsed from a provider's history page.
type UsageDetail struct {
	Requests int     `json:"requests"`
	Cost     float64 `json:"cost"`     // USD
	CacheHit float64 `json:"cacheHit"` // percent of input tokens served from cache
	// TodayCost/PeriodCost are spend figures for relay panels that report
	// them (Sub2API: today's cost and the rolling ~30d cost). When set, the
	// widget renders them instead of the generic Cost line.
	TodayCost  float64 `json:"todayCost,omitempty"`  // USD spent today
	PeriodCost float64 `json:"periodCost,omitempty"` // USD spent over the recent period (~30d)
	// GroupName/RateMultiplier/PeakActive describe the billing group of a
	// relay-panel key (Sub2API). RateMultiplier is the effective multiplier
	// including the peak-rate window when one is active.
	GroupName      string  `json:"groupName,omitempty"`      // group/plan name
	RateMultiplier float64 `json:"rateMultiplier,omitempty"` // effective billing multiplier (incl. peak)
	PeakActive     bool    `json:"peakActive,omitempty"`     // peak-rate window is currently active
	// ExpiresAt/ExpiresInSec carry the key/subscription expiry.
	ExpiresAt    string `json:"expiresAt,omitempty"`  // "2006-01-02"; empty = never expires
	ExpiresInSec int64  `json:"expiresInSec,omitempty"` // seconds until expiry (>0)
}

// Result is the outcome of a single provider query.
type Result struct {
	ProviderID   string         `json:"providerId"`
	ProviderName string         `json:"providerName"`
	Windows      []WindowStatus `json:"windows"`
	Detail       UsageDetail    `json:"detail,omitempty"`
	UpdatedAt    string         `json:"updatedAt"`       // local time "HH:MM:SS"
	Error        string         `json:"error,omitempty"` // non-empty when the query failed
}

// Provider queries the usage of a single plan/API account.
type Provider interface {
	ID() string
	Name() string
	Query(ctx context.Context) (*Result, error)
}

// ProviderField describes one configurable parameter of a provider, rendered
// as a dynamic form control in the settings UI. Key is a known slot of
// config.ProviderConfig: "workspace", "cookie" or "detail.showUsageDetail".
// Kind "help" renders a collapsible tutorial box (Label holds the text).
type ProviderField struct {
	Key         string `json:"key"`      // config slot ("" for kind=help)
	Label       string `json:"label"`    // UI label, or help text for kind=help
	Kind        string `json:"kind"`     // "text" | "password" | "checkbox" | "help"
	Required    bool   `json:"required"` // mark the control as required
	Placeholder string `json:"placeholder,omitempty"`
}

// ProviderType describes a provider implementation available for configuration.
// New providers register themselves here at package init (see Register).
type ProviderType struct {
	Type        string          `json:"type"`        // stable key, e.g. "opencode-go"
	Name        string          `json:"name"`        // display name
	Description string          `json:"description"` // one-line summary shown in the UI
	Fields      []ProviderField `json:"fields"`      // dynamic parameter form for this type
}

// Factory builds a provider instance from its persisted configuration.
type Factory func(cfg config.ProviderConfig) (Provider, error)

var (
	registry = map[string]Factory{}
	typeInfo = map[string]ProviderType{}
)

// Register adds a provider implementation to the registry. It is meant to be
// called from a provider file's init(). Duplicate or nil factories panic so a
// mis-wired provider is caught at startup. The optional fields describe the
// dynamic parameter form the settings UI renders for this type.
func Register(typeKey, name, description string, factory Factory, fields ...ProviderField) {
	if factory == nil {
		panic("providers: nil factory for type " + typeKey)
	}
	if _, dup := registry[typeKey]; dup {
		panic("providers: duplicate provider type " + typeKey)
	}
	registry[typeKey] = factory
	typeInfo[typeKey] = ProviderType{
		Type: typeKey, Name: name, Description: description, Fields: fields,
	}
}

// New builds a provider instance for the given config, dispatching on Type
// through the registry.
func New(cfg config.ProviderConfig) (Provider, error) {
	factory, ok := registry[cfg.Type]
	if !ok {
		return nil, &UnknownProviderError{Type: cfg.Type}
	}
	return factory(cfg)
}

// Types returns the registered provider types, sorted by key, for the settings
// UI to enumerate (only coded providers appear here).
func Types() []ProviderType {
	list := make([]ProviderType, 0, len(typeInfo))
	for _, pt := range typeInfo {
		list = append(list, pt)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Type < list[j].Type })
	return list
}

// UnknownProviderError is returned for provider types that have no registered
// implementation.
type UnknownProviderError struct{ Type string }

func (e *UnknownProviderError) Error() string {
	known := make([]string, 0, len(registry))
	for k := range registry {
		known = append(known, k)
	}
	sort.Strings(known)
	return fmt.Sprintf("unknown provider type %q (known: %s)", e.Type, strings.Join(known, ", "))
}
