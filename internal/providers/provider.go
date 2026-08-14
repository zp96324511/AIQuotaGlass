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
	Key        string  `json:"key"`            // "5h", "weekly", "monthly"
	Label      string  `json:"label"`          // human readable label
	Percent    float64 `json:"percent"`        // 0..100 usage of the limit; -1 = unlimited (UI shows 无限)
	Used       float64 `json:"used"`           // raw consumed quota; Total=0 means unavailable
	Total      float64 `json:"total"`          // raw quota limit; 0 means unavailable
	ResetInSec int64   `json:"resetInSec"`     // seconds until the window resets; -1 = no auto reset (countdown hidden)
	Status     string  `json:"status"`         // "ok" or error marker
	Unit       string  `json:"unit,omitempty"` // currency of Used/Total, e.g. "CNY", "USD" (balance windows)
}

// UsageDetail holds optional statistics parsed from a provider's history page.
type UsageDetail struct {
	Requests int     `json:"requests"`
	Cost     float64 `json:"cost"`     // USD
	CacheHit float64 `json:"cacheHit"` // percent of input tokens served from cache
	// WeeklyRequests complements Requests for providers that report both a
	// daily and a rolling-weekly request count (ElectronHub).
	WeeklyRequests int `json:"weeklyRequests,omitempty"`
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
	ExpiresAt        string `json:"expiresAt,omitempty"`    // "2006-01-02"; empty = never expires
	ExpiresInSec     int64  `json:"expiresInSec,omitempty"` // seconds until expiry (>0)
	metricsAvailable bool
}

// MarkUsageMetricsAvailable marks detail metrics as successfully parsed.
// The marker is internal and is not serialized to the frontend.
func (d *UsageDetail) MarkUsageMetricsAvailable() {
	d.metricsAvailable = true
}

// HasUsageMetrics reports whether activity metrics were successfully parsed.
func (d UsageDetail) HasUsageMetrics() bool {
	return d.metricsAvailable
}

// ErrorInfo carries structured details of a failed HTTP request so the card's
// status dot can flip to red and open the request info modal on click. nil when
// the failure produced no HTTP response (network error) or no useful payload.
type ErrorInfo struct {
	Method     string `json:"method,omitempty"` // HTTP method of the failing request
	URL        string `json:"url,omitempty"`    // request URL (credentials live in headers, never here)
	StatusCode int    `json:"statusCode"`       // HTTP status; 0 = no HTTP response
	Body       string `json:"body,omitempty"`   // printable, truncated response-body snippet
}

// Result is the outcome of a single provider query.
type Result struct {
	ProviderID   string         `json:"providerId"`
	ProviderName string         `json:"providerName"`
	Windows      []WindowStatus `json:"windows"`
	Detail       *UsageDetail   `json:"detail,omitempty"` // nil when optional detail is unavailable
	UpdatedAt    string         `json:"updatedAt"`        // local time "HH:MM:SS"
	Error        string         `json:"error,omitempty"`  // non-empty when the query failed
	ErrorInfo    *ErrorInfo     `json:"errorInfo,omitempty"`
}

// httpErrorInfo builds the ErrorInfo for a failed HTTP response. Binary and
// control characters are stripped from the body snippet so it renders safely
// in the widget; method/url are recorded for the request-info modal.
func httpErrorInfo(method, url string, status int, body []byte) *ErrorInfo {
	return &ErrorInfo{
		Method:     method,
		URL:        url,
		StatusCode: status,
		Body:       truncatedErrorBody(body),
	}
}

// truncatedErrorBody returns a printable, whitespace-compacted snippet of the
// response body (max 500 chars) for inline error display, or "" when the body
// is empty or has no printable content.
func truncatedErrorBody(body []byte) string {
	const max = 500
	var b strings.Builder
	b.Grow(len(body))
	spacePending := false
	escape := false
	for _, c := range body {
		if escape {
			// ANSI escape sequence: skip until the terminating byte.
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
				escape = false
			}
			continue
		}
		if c == 0x1b {
			escape = true
			continue
		}
		if c == '\n' || c == '\r' || c == '\t' {
			spacePending = true
			continue
		}
		if c < 0x20 || c == 0x7f {
			continue // strip control characters
		}
		if spacePending && b.Len() > 0 && b.Len() < max {
			b.WriteByte(' ')
			spacePending = false
		}
		if b.Len() >= max {
			break
		}
		b.WriteByte(c)
	}
	out := b.String()
	if len(out) >= max {
		out = out[:max] + "…"
	}
	return out
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
	// WindowKeys lists the quota window keys this provider emits (e.g. "5h",
	// "weekly", "monthly", "total"). The settings UI renders one threshold
	// input per key, scoped to the provider type so it never asks for a value
	// the provider never reports. Empty (or nil) means "no quota windows".
	WindowKeys []string `json:"windowKeys"`
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

// RegisterWindows declares the quota window keys a registered provider type
// emits (e.g. "5h", "weekly", "monthly", "total"). The settings UI uses this
// to render only the threshold inputs that actually apply to the type. Call
// from the provider's init() right after Register(); panics if the type is
// not registered or if a duplicate window key is given.
func RegisterWindows(typeKey string, keys ...string) {
	info, ok := typeInfo[typeKey]
	if !ok {
		panic("providers: RegisterWindows for unregistered type " + typeKey)
	}
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			panic("providers: RegisterWindows duplicate key " + k + " for type " + typeKey)
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	info.WindowKeys = out
	typeInfo[typeKey] = info
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
