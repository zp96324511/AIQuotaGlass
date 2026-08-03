package main

import (
	"math"
	"sort"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/providers"
)

type quotaWindowSnapshot struct {
	Percent    float64
	Used       float64
	Total      float64
	Unit       string
	Status     string
	ResetInSec int64
}

type usageDetailSnapshot struct {
	Requests   int
	Cost       float64
	CacheHit   float64
	TodayCost  float64
	PeriodCost float64
}

type quotaWindowsSnapshot map[string]quotaWindowSnapshot

type quotaSnapshot struct {
	Windows quotaWindowsSnapshot
	// Detail tracks activity-sensitive numeric fields, not countdown or descriptive metadata.
	Detail    usageDetailSnapshot
	HasDetail bool
	HasError  bool
}

// Small increases can come from provider rounding or request timing; a larger
// increase means the rolling window reset and should count as recent activity.
const resetCountdownJitterSec int64 = 5

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
	windows := make(quotaWindowsSnapshot, len(res.Windows))
	for i := range res.Windows {
		w := res.Windows[i]
		windows[w.Key] = quotaWindowSnapshot{
			Percent:    w.Percent,
			Used:       w.Used,
			Total:      w.Total,
			Unit:       w.Unit,
			Status:     w.Status,
			ResetInSec: w.ResetInSec,
		}
	}
	snapshot := quotaSnapshot{Windows: windows, HasError: res.Error != ""}
	if res.Detail != nil && res.Detail.HasUsageMetrics() {
		snapshot.Detail = usageDetailSnapshot{
			Requests:   res.Detail.Requests,
			Cost:       res.Detail.Cost,
			CacheHit:   res.Detail.CacheHit,
			TodayCost:  res.Detail.TodayCost,
			PeriodCost: res.Detail.PeriodCost,
		}
		snapshot.HasDetail = true
	}
	return snapshot
}

func quotaSnapshotsEqual(left, right quotaSnapshot) bool {
	if left.HasError != right.HasError {
		return false
	}
	if len(left.Windows) != len(right.Windows) {
		return false
	}
	for key, l := range left.Windows {
		r, ok := right.Windows[key]
		if !ok || l.Unit != r.Unit || l.Status != r.Status || quotaResetChanged(l.ResetInSec, r.ResetInSec) ||
			!quotaValueEqual(l.Percent, r.Percent) || !quotaValueEqual(l.Used, r.Used) ||
			!quotaValueEqual(l.Total, r.Total) {
			return false
		}
	}
	if !left.HasDetail || !right.HasDetail {
		return true
	}
	return left.Detail.Requests == right.Detail.Requests &&
		quotaValueEqual(left.Detail.Cost, right.Detail.Cost) &&
		quotaValueEqual(left.Detail.CacheHit, right.Detail.CacheHit) &&
		quotaValueEqual(left.Detail.TodayCost, right.Detail.TodayCost) &&
		quotaValueEqual(left.Detail.PeriodCost, right.Detail.PeriodCost)
}

func quotaResetChanged(previous, current int64) bool {
	if previous < 0 || current < 0 {
		return previous != current
	}
	return current > previous+resetCountdownJitterSec
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
