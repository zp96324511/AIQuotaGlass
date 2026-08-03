package main

import (
	"testing"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/providers"
)

func TestQuotaSnapshots_ignore_reset_countdown_changes(t *testing.T) {
	previous := quotaSnapshotOf(providers.Result{
		ProviderID: "alpha",
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 3600,
		}},
	})
	current := providers.Result{
		ProviderID: "alpha",
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 3599,
		}},
	}

	if !quotaSnapshotsEqual(previous, quotaSnapshotOf(current)) {
		t.Fatal("reset countdown should not count as a quota change")
	}
}

func TestQuotaSnapshots_detect_used_quota_changes(t *testing.T) {
	previous := quotaSnapshotOf(providers.Result{
		ProviderID: "alpha",
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: 42, Used: 4.2, Total: 10,
		}},
	})
	current := quotaSnapshotOf(providers.Result{
		ProviderID: "alpha",
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: 43, Used: 4.3, Total: 10,
		}},
	})

	if quotaSnapshotsEqual(previous, current) {
		t.Fatal("used quota change should be detected")
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
