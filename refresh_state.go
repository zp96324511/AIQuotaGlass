package main

import (
	"math"
	"sort"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/providers"
)

type quotaWindowSnapshot struct {
	Percent float64
	Used    float64
	Total   float64
	Unit    string
}

type quotaSnapshot map[string]quotaWindowSnapshot

func (s *AppService) configSnapshot() *config.AppConfig {
	cfg, _ := s.configSnapshotWithVersion()
	return cfg
}

func (s *AppService) configSnapshotWithVersion() (*config.AppConfig, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneAppConfig(s.cfg), s.configVersion
}

func cloneAppConfig(cfg *config.AppConfig) *config.AppConfig {
	if cfg == nil {
		return config.Default()
	}
	out := *cfg
	out.Providers = append([]config.ProviderConfig(nil), cfg.Providers...)
	for i := range out.Providers {
		out.Providers[i].AlertThresholds = cloneThresholds(cfg.Providers[i].AlertThresholds)
	}
	return &out
}

func cloneThresholds(thresholds map[string]int) map[string]int {
	if thresholds == nil {
		return nil
	}
	cloned := make(map[string]int, len(thresholds))
	for key, value := range thresholds {
		cloned[key] = value
	}
	return cloned
}

func enabledProviderIDs(cfg *config.AppConfig) []string {
	ids := make([]string, 0, len(cfg.Providers))
	for i := range cfg.Providers {
		if cfg.Providers[i].Enabled {
			ids = append(ids, cfg.Providers[i].ID)
		}
	}
	return ids
}

func dynamicSortEnabled(cfg *config.AppConfig, providerID string) bool {
	if cfg == nil {
		return true
	}
	p := cfg.ProviderConfig(providerID)
	if p == nil || p.DynamicSort == nil {
		return true
	}
	return *p.DynamicSort
}

func quotaSnapshotOf(res providers.Result) quotaSnapshot {
	snapshot := make(quotaSnapshot, len(res.Windows))
	for i := range res.Windows {
		w := res.Windows[i]
		snapshot[w.Key] = quotaWindowSnapshot{
			Percent: w.Percent,
			Used:    w.Used,
			Total:   w.Total,
			Unit:    w.Unit,
		}
	}
	return snapshot
}

func quotaSnapshotsEqual(left, right quotaSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for key, l := range left {
		r, ok := right[key]
		if !ok || l.Unit != r.Unit || !quotaValueEqual(l.Percent, r.Percent) ||
			!quotaValueEqual(l.Used, r.Used) || !quotaValueEqual(l.Total, r.Total) {
			return false
		}
	}
	return true
}

func sortResultsByRecentChange(list []providers.Result, changedRounds map[string]uint64, configs []config.ProviderConfig) []providers.Result {
	result := append([]providers.Result(nil), list...)
	order := make(map[string]configOrder, len(configs))
	for i := range configs {
		order[configs[i].ID] = configOrder{
			known:               true,
			sortOrder:           configs[i].SortOrder,
			index:               i,
			dynamicSortDisabled: configs[i].DynamicSort != nil && !*configs[i].DynamicSort,
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		leftRound := changedRounds[result[i].ProviderID]
		rightRound := changedRounds[result[j].ProviderID]
		if leftOrder := order[result[i].ProviderID]; leftOrder.dynamicSortDisabled {
			leftRound = 0
		}
		if rightOrder := order[result[j].ProviderID]; rightOrder.dynamicSortDisabled {
			rightRound = 0
		}
		if leftRound != rightRound {
			return leftRound > rightRound
		}
		leftOrder := order[result[i].ProviderID]
		rightOrder := order[result[j].ProviderID]
		if leftOrder.known != rightOrder.known {
			return leftOrder.known
		}
		if leftOrder.sortOrder != rightOrder.sortOrder {
			return leftOrder.sortOrder < rightOrder.sortOrder
		}
		return leftOrder.index < rightOrder.index
	})
	return result
}

type configOrder struct {
	known               bool
	sortOrder           int
	index               int
	dynamicSortDisabled bool
}

func quotaValueEqual(left, right float64) bool {
	return math.Abs(left-right) <= 1e-9*math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
}
