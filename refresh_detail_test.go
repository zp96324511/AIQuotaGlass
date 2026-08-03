package main

import (
	"testing"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/providers"
)

func TestCommitQuotaRound_detects_usage_detail_changes_when_quota_window_unchanged(t *testing.T) {
	cfg := &config.AppConfig{Providers: []config.ProviderConfig{
		{ID: "alpha", SortOrder: 1},
		{ID: "beta", SortOrder: 0},
	}}
	previous := result("alpha", 42)
	previous.Detail = &providers.UsageDetail{Requests: 10, Cost: 1.25, CacheHit: 40}
	previous.Detail.MarkUsageMetricsAvailable()
	service := &AppService{
		lastStatus: []providers.Result{{ProviderID: "beta"}, {ProviderID: "alpha"}},
		quotaSnapshots: map[string]quotaSnapshot{
			"alpha": quotaSnapshotOf(previous),
			"beta":  quotaSnapshotOf(result("beta", 20)),
		},
		lastChangedRound: map[string]uint64{},
	}
	current := result("alpha", 42)
	current.Detail = &providers.UsageDetail{Requests: 11, Cost: 1.35, CacheHit: 42}
	current.Detail.MarkUsageMetricsAvailable()

	event := service.commitQuotaRound(5, 2, cfg, []providers.Result{current, result("beta", 20)})

	if got, want := event.ChangedProviderIDs, []string{"alpha"}; !sameStrings(got, want) {
		t.Fatalf("detail-only usage change IDs = %v, want %v", got, want)
	}
	if got, want := event.ProviderIDs, []string{"alpha", "beta"}; !sameStrings(got, want) {
		t.Fatalf("detail-only usage provider order = %v, want %v", got, want)
	}
	if got, want := service.lastChangedRound["alpha"], uint64(5); got != want {
		t.Fatalf("detail-only usage changed round = %d, want %d", got, want)
	}
}

func TestCommitQuotaRound_ignores_missing_usage_detail_as_change(t *testing.T) {
	cfg := &config.AppConfig{Providers: []config.ProviderConfig{{ID: "alpha", SortOrder: 0}}}
	previous := result("alpha", 42)
	previous.Detail = &providers.UsageDetail{Requests: 10, Cost: 1.25, CacheHit: 40}
	previous.Detail.MarkUsageMetricsAvailable()
	service := &AppService{
		lastStatus: []providers.Result{{ProviderID: "alpha"}},
		quotaSnapshots: map[string]quotaSnapshot{
			"alpha": quotaSnapshotOf(previous),
		},
		lastChangedRound: map[string]uint64{},
	}

	event := service.commitQuotaRound(5, 2, cfg, []providers.Result{result("alpha", 42)})

	if len(event.ChangedProviderIDs) != 0 {
		t.Fatalf("missing usage detail must not count as a change, got %v", event.ChangedProviderIDs)
	}
	if _, ok := service.lastChangedRound["alpha"]; ok {
		t.Fatalf("missing usage detail must not record a changed round, got %+v", service.lastChangedRound)
	}
	if got, want := service.quotaSnapshots["alpha"].Detail, quotaSnapshotOf(previous).Detail; got != want {
		t.Fatalf("missing usage detail replaced the last valid baseline: got %+v, want %+v", got, want)
	}
	if !service.quotaSnapshots["alpha"].HasDetail {
		t.Fatal("missing usage detail cleared the availability marker")
	}
}

func TestQuotaSnapshots_detect_each_supported_usage_detail_change(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*providers.UsageDetail)
	}{
		{name: "requests", mutate: func(d *providers.UsageDetail) { d.Requests = 11 }},
		{name: "cost", mutate: func(d *providers.UsageDetail) { d.Cost = 1.1 }},
		{name: "cache hit", mutate: func(d *providers.UsageDetail) { d.CacheHit = 41 }},
		{name: "today cost", mutate: func(d *providers.UsageDetail) { d.TodayCost = 0.2 }},
		{name: "period cost", mutate: func(d *providers.UsageDetail) { d.PeriodCost = 2.1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := &providers.UsageDetail{Requests: 10, Cost: 1, CacheHit: 40, TodayCost: 0.1, PeriodCost: 2}
			previous.MarkUsageMetricsAvailable()
			current := *previous
			tt.mutate(&current)
			current.MarkUsageMetricsAvailable()

			if quotaSnapshotsEqual(
				quotaSnapshotOf(providers.Result{Detail: previous}),
				quotaSnapshotOf(providers.Result{Detail: &current}),
			) {
				t.Fatalf("%s detail change was not detected", tt.name)
			}
		})
	}
}

func TestQuotaSnapshots_ignore_usage_detail_metadata_changes(t *testing.T) {
	previous := &providers.UsageDetail{
		Requests: 10, Cost: 1, CacheHit: 40, GroupName: "standard", ExpiresAt: "2026-08-01",
	}
	current := &providers.UsageDetail{
		Requests: 10, Cost: 1, CacheHit: 40, GroupName: "premium", ExpiresAt: "2026-08-02",
	}
	previous.MarkUsageMetricsAvailable()
	current.MarkUsageMetricsAvailable()

	if !quotaSnapshotsEqual(
		quotaSnapshotOf(providers.Result{Detail: previous}),
		quotaSnapshotOf(providers.Result{Detail: current}),
	) {
		t.Fatal("usage detail metadata should not count as an activity change")
	}
}

func TestQuotaSnapshots_treats_valid_zero_usage_detail_as_available(t *testing.T) {
	previous := &providers.UsageDetail{}
	current := &providers.UsageDetail{Requests: 1}
	previous.MarkUsageMetricsAvailable()
	current.MarkUsageMetricsAvailable()

	left := quotaSnapshotOf(providers.Result{Detail: previous})
	right := quotaSnapshotOf(providers.Result{Detail: current})

	if quotaSnapshotsEqual(left, right) {
		t.Fatal("valid zero usage detail should be compared with the next detail reading")
	}
}
