package main

import (
	"testing"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/providers"
)

func TestQuotaUseChanged_detects_percent_growth(t *testing.T) {
	if !quotaUseChanged(
		quotaSnapshotOf(providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 42, Used: 4.2, Total: 10}}}),
		quotaSnapshotOf(providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 43, Used: 4.3, Total: 10}}}),
	) {
		t.Fatal("percent growth should count as real usage")
	}
}

func TestQuotaUseChanged_ignores_passive_changes(t *testing.T) {
	// Percent is unchanged; every other field shifts the way rollovers and
	// availability transitions do. None of these is user activity.
	tests := []struct {
		name     string
		previous providers.Result
		current  providers.Result
	}{
		{
			name:     "window reset countdown restart",
			previous: providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 2, Status: "ok"}}},
			current:  providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 18000, Status: "ok"}}},
		},
		{
			name:     "window rolled over to zero",
			previous: providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 100, Used: 10, Total: 10, Status: "ok"}}},
			current:  providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 0, Used: 0, Total: 10, Status: "ok"}}},
		},
		{
			name:     "countdown ticking down",
			previous: providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 3600}}},
			current:  providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 3599}}},
		},
		{
			name:     "status flip without numeric change",
			previous: providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 42, Used: 4.2, Total: 10, Status: "ok"}}},
			current:  providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 42, Used: 4.2, Total: 10, Status: "error"}}},
		},
		{
			name:     "balance-type account spending (Used shrinks)",
			previous: providers.Result{Windows: []providers.WindowStatus{{Key: "balance", Percent: 42, Used: 58, Total: 0}}},
			current:  providers.Result{Windows: []providers.WindowStatus{{Key: "balance", Percent: 42, Used: 57, Total: 0}}},
		},
		{
			name:     "percent drop from quota limit adjustment",
			previous: providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 50, Used: 5, Total: 10}}},
			current:  providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 45, Used: 5, Total: 11}}},
		},
		{
			name:     "error enter and recover",
			previous: providers.Result{Windows: []providers.WindowStatus{{Key: "5h", Percent: 42}}, Error: ""},
			current:  providers.Result{Error: "request failed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if quotaUseChanged(quotaSnapshotOf(tt.previous), quotaSnapshotOf(tt.current)) {
				t.Fatalf("%s must not count as usage", tt.name)
			}
		})
	}
}

func TestSortResultsByRecentChange_prefers_round_over_config_order(t *testing.T) {
	results := []providers.Result{
		{ProviderID: "beta"},
		{ProviderID: "alpha"},
		{ProviderID: "gamma"},
	}
	changedRounds := map[string]uint64{
		"alpha": 6,
		"beta":  7,
	}
	configs := []config.ProviderConfig{
		{ID: "alpha", SortOrder: 0},
		{ID: "beta", SortOrder: 1},
		{ID: "gamma", SortOrder: 2},
	}

	sorted := sortResultsByRecentChange(results, changedRounds, configs)
	got := []string{sorted[0].ProviderID, sorted[1].ProviderID, sorted[2].ProviderID}
	want := []string{"beta", "alpha", "gamma"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted provider IDs = %v, want %v", got, want)
		}
	}
}

func TestCommitQuotaRound_groups_same_round_changes_and_keeps_tie_order(t *testing.T) {
	config := &config.AppConfig{Providers: []config.ProviderConfig{
		{ID: "alpha", SortOrder: 1},
		{ID: "beta", SortOrder: 0},
		{ID: "gamma", SortOrder: 2},
	}}
	service := &AppService{
		lastStatus: []providers.Result{
			{ProviderID: "alpha"},
			{ProviderID: "beta"},
			{ProviderID: "gamma"},
		},
		quotaSnapshots: map[string]quotaSnapshot{
			"alpha": quotaSnapshotOf(result("alpha", 10)),
			"beta":  quotaSnapshotOf(result("beta", 20)),
			"gamma": quotaSnapshotOf(result("gamma", 30)),
		},
		lastChangedRound: map[string]uint64{},
	}

	first := service.commitQuotaRound(7, 3, config, []providers.Result{
		result("alpha", 11),
		result("beta", 21),
		result("gamma", 30),
	})

	if service.lastChangedRound["alpha"] != 7 || service.lastChangedRound["beta"] != 7 {
		t.Fatalf("same-round changes = %+v, want alpha=7 and beta=7", service.lastChangedRound)
	}
	if first.ChangedAt["alpha"] <= 0 || first.ChangedAt["beta"] <= 0 {
		t.Fatalf("changedAt must record unix seconds for changed providers, got %+v", first.ChangedAt)
	}
	if _, ok := first.ChangedAt["gamma"]; ok {
		t.Fatalf("unchanged provider must not appear in changedAt, got %+v", first.ChangedAt)
	}
	if first.ConfigVersion != 3 || first.RoundID != 7 {
		t.Fatalf("first event metadata = config %d, round %d; want config 3, round 7", first.ConfigVersion, first.RoundID)
	}
	if got, want := first.ProviderIDs, []string{"beta", "alpha", "gamma"}; !sameStrings(got, want) {
		t.Fatalf("first provider order = %v, want %v", got, want)
	}
	if got, want := first.ChangedProviderIDs, []string{"beta", "alpha"}; !sameStrings(got, want) {
		t.Fatalf("first changed IDs = %v, want %v", got, want)
	}

	second := service.commitQuotaRound(8, 0, config, []providers.Result{
		result("alpha", 12),
		result("beta", 21),
		result("gamma", 30),
	})

	if service.lastChangedRound["alpha"] != 8 || service.lastChangedRound["beta"] != 7 {
		t.Fatalf("cross-round changes = %+v, want alpha=8 and beta=7", service.lastChangedRound)
	}
	if second.ChangedAt["alpha"] < first.ChangedAt["alpha"] {
		t.Fatalf("alpha changedAt must not go backwards: first %d, second %d", first.ChangedAt["alpha"], second.ChangedAt["alpha"])
	}
	if got, want := second.ChangedAt["beta"], first.ChangedAt["beta"]; got != want {
		t.Fatalf("beta changedAt must stay stable across rounds: first %d, second %d", want, got)
	}
	if got, want := second.ProviderIDs, []string{"alpha", "beta", "gamma"}; !sameStrings(got, want) {
		t.Fatalf("second provider order = %v, want %v", got, want)
	}
	if got, want := second.ChangedProviderIDs, []string{"alpha"}; !sameStrings(got, want) {
		t.Fatalf("second changed IDs = %v, want %v", got, want)
	}
}

func TestCommitQuotaRound_ignores_provider_completion_order(t *testing.T) {
	cfg := &config.AppConfig{Providers: []config.ProviderConfig{
		{ID: "alpha", SortOrder: 1},
		{ID: "beta", SortOrder: 0},
		{ID: "gamma", SortOrder: 2},
	}}
	newService := func(completionOrder []providers.Result) *AppService {
		service := &AppService{
			quotaSnapshots: map[string]quotaSnapshot{
				"alpha": quotaSnapshotOf(result("alpha", 10)),
				"beta":  quotaSnapshotOf(result("beta", 20)),
				"gamma": quotaSnapshotOf(result("gamma", 30)),
			},
			lastChangedRound: map[string]uint64{},
		}
		for _, res := range completionOrder {
			service.lastStatus = upsertResult(service.lastStatus, res)
		}
		return service
	}

	first := []providers.Result{result("alpha", 11), result("beta", 21), result("gamma", 30)}
	second := []providers.Result{result("gamma", 30), result("beta", 21), result("alpha", 11)}
	firstEvent := newService(first).commitQuotaRound(9, 4, cfg, first)
	secondEvent := newService(second).commitQuotaRound(9, 4, cfg, second)

	if !sameStrings(firstEvent.ProviderIDs, secondEvent.ProviderIDs) {
		t.Fatalf("provider order depends on completion order: first=%v second=%v", firstEvent.ProviderIDs, secondEvent.ProviderIDs)
	}
	if !sameStrings(firstEvent.ChangedProviderIDs, secondEvent.ChangedProviderIDs) {
		t.Fatalf("changed IDs depend on completion order: first=%v second=%v", firstEvent.ChangedProviderIDs, secondEvent.ChangedProviderIDs)
	}
}

func TestSortResultsByRecentChange_ignores_round_for_disabled_dynamic_sort(t *testing.T) {
	disabled := false
	enabled := true
	results := []providers.Result{
		{ProviderID: "beta"},
		{ProviderID: "alpha"},
		{ProviderID: "gamma"},
	}
	changedRounds := map[string]uint64{
		"alpha": 10,
		"beta":  9,
	}
	configs := []config.ProviderConfig{
		{ID: "alpha", SortOrder: 1, DynamicSort: &disabled},
		{ID: "beta", SortOrder: 0, DynamicSort: &enabled},
		{ID: "gamma", SortOrder: 2, DynamicSort: &enabled},
	}

	sorted := sortResultsByRecentChange(results, changedRounds, configs)
	got := []string{sorted[0].ProviderID, sorted[1].ProviderID, sorted[2].ProviderID}
	want := []string{"beta", "alpha", "gamma"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted provider IDs = %v, want %v", got, want)
		}
	}
}

func TestCommitQuotaRound_disabled_dynamic_sort_not_counted_as_changed(t *testing.T) {
	disabled := false
	cfg := &config.AppConfig{Providers: []config.ProviderConfig{
		{ID: "alpha", SortOrder: 0, DynamicSort: &disabled},
		{ID: "beta", SortOrder: 1},
	}}
	service := &AppService{
		lastStatus: []providers.Result{
			{ProviderID: "alpha"},
			{ProviderID: "beta"},
		},
		quotaSnapshots: map[string]quotaSnapshot{
			"alpha": quotaSnapshotOf(result("alpha", 10)),
			"beta":  quotaSnapshotOf(result("beta", 20)),
		},
		lastChangedRound: map[string]uint64{},
	}

	event := service.commitQuotaRound(5, 2, cfg, []providers.Result{
		result("alpha", 11),
		result("beta", 21),
	})

	if _, ok := service.lastChangedRound["alpha"]; ok {
		t.Fatalf("alpha with disabled dynamic sort must not record a changed round, got %+v", service.lastChangedRound)
	}
	if got, want := event.ChangedProviderIDs, []string{"beta"}; !sameStrings(got, want) {
		t.Fatalf("changed IDs = %v, want %v", got, want)
	}
}

func result(providerID string, used float64) providers.Result {
	return providers.Result{
		ProviderID: providerID,
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: used, Used: used, Total: 100,
		}},
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestUpsertResult_error_keeps_last_good_stats(t *testing.T) {
	good := providers.Result{
		ProviderID: "alpha", ProviderName: "Alpha",
		Windows: []providers.WindowStatus{{Key: "5h", Percent: 42, Used: 4.2, Total: 10}},
		Detail:  &providers.UsageDetail{Requests: 5, Cost: 0.5},
	}
	bad := providers.Result{
		ProviderID: "alpha", ProviderName: "Alpha",
		Error:     "request failed",
		UpdatedAt: "12:00:00",
	}

	got := upsertResult([]providers.Result{good}, bad)
	if len(got) != 1 || got[0].Error != "request failed" {
		t.Fatalf("error state not applied: %+v", got)
	}
	if len(got[0].Windows) != 1 || got[0].Windows[0].Percent != 42 {
		t.Fatalf("last good windows not preserved: %+v", got[0].Windows)
	}
	if got[0].Detail == nil || got[0].Detail.Requests != 5 {
		t.Fatalf("last good detail not preserved: %+v", got[0].Detail)
	}
	if got[0].UpdatedAt != "12:00:00" {
		t.Fatalf("error result fields not applied: %+v", got[0])
	}

	ok := providers.Result{
		ProviderID: "alpha", ProviderName: "Alpha",
		Windows: []providers.WindowStatus{{Key: "5h", Percent: 50, Used: 5, Total: 10}},
	}
	got = upsertResult(got, ok)
	if got[0].Error != "" || len(got[0].Windows) != 1 || got[0].Windows[0].Percent != 50 {
		t.Fatalf("success result should replace in full: %+v", got[0])
	}
	if got[0].Detail != nil {
		t.Fatalf("stale detail should be dropped on success: %+v", got[0].Detail)
	}
}
