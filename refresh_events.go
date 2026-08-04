package main

import (
	"strconv"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/notify"
	"aiquotaglass/internal/providers"
)

// usageLoadingEvent reserves all cards for one logical refresh round.
type usageLoadingEvent struct {
	ConfigVersion uint64   `json:"configVersion"`
	RoundID       uint64   `json:"roundId"`
	ProviderIDs   []string `json:"providerIds"`
}

// usageUpdateEvent carries one provider's result without losing its round.
type usageUpdateEvent struct {
	ConfigVersion uint64             `json:"configVersion"`
	RoundID       uint64             `json:"roundId"`
	Results       []providers.Result `json:"results"`
}

// usageCompleteEvent commits the order only after all providers finish.
// ChangedAt carries each provider's last quota-change time (unix seconds) so
// the widget can show the recent-activity label next to the account name.
type usageCompleteEvent struct {
	ConfigVersion      uint64           `json:"configVersion"`
	RoundID            uint64           `json:"roundId"`
	ChangedProviderIDs []string         `json:"changedProviderIds"`
	ProviderIDs        []string         `json:"providerIds"`
	ChangedAt          map[string]int64 `json:"changedAt,omitempty"`
}

type configSavedEvent struct {
	Version uint64            `json:"version"`
	RoundID uint64            `json:"roundId"`
	Config  *config.AppConfig `json:"config"`
}

// upsertResult replaces one provider while preserving the current position.
// Error results carry no quota data, so the last good windows/detail are kept
// and only the error state is applied (the card shows a red status dot).
func upsertResult(list []providers.Result, r providers.Result) []providers.Result {
	for i := range list {
		if list[i].ProviderID != r.ProviderID {
			continue
		}
		if r.Error == "" || len(r.Windows) > 0 {
			list[i] = r
			return list
		}
		prev := list[i]
		merged := r
		if len(prev.Windows) > 0 {
			merged.Windows = prev.Windows
		}
		if merged.Detail == nil && prev.Detail != nil {
			merged.Detail = prev.Detail
		}
		list[i] = merged
		return list
	}
	return append(list, r)
}

func (s *AppService) checkAlerts(res *providers.Result, cfg *config.AppConfig) {
	if res.Error != "" || cfg == nil {
		return
	}
	p := cfg.ProviderConfig(res.ProviderID)
	if p == nil {
		return
	}
	for i := range res.Windows {
		w := res.Windows[i]
		threshold, ok := p.AlertThresholds[w.Key]
		if !ok {
			continue
		}
		key := res.ProviderID + "/" + w.Key
		above := w.Percent >= float64(threshold)
		s.mu.Lock()
		armed := s.alertArmed[key]
		s.alertArmed[key] = above
		s.mu.Unlock()
		if above && !armed {
			msg := alertMessage(p.Name, w, threshold)
			s.app.Event.Emit("usage:alert", map[string]any{
				"provider": p.Name, "window": w.Label, "percent": w.Percent, "threshold": threshold,
			})
			if cfg.NativeNotify {
				notify.Show("AIQuotaGlass 用量告警", msg)
			}
		}
	}
}

func alertMessage(name string, w providers.WindowStatus, threshold int) string {
	return name + " " + w.Label + " 用量已达 " + formatPercent(w.Percent) + " (阈值 " + strconv.Itoa(threshold) + "%)"
}
