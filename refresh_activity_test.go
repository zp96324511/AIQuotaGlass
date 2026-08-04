package main

import (
	"testing"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/providers"
)

func TestCommitQuotaRound_window_reset_does_not_promote_account(t *testing.T) {
	cfg := &config.AppConfig{Providers: []config.ProviderConfig{
		{ID: "alpha", SortOrder: 1},
		{ID: "beta", SortOrder: 0},
	}}
	previousAlpha := providers.Result{
		ProviderID: "alpha",
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 2, Status: "ok",
		}},
	}
	currentAlpha := providers.Result{
		ProviderID: "alpha",
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 18000, Status: "ok",
		}},
	}
	service := &AppService{
		lastStatus: []providers.Result{{ProviderID: "beta"}, {ProviderID: "alpha"}},
		quotaSnapshots: map[string]quotaSnapshot{
			"alpha": quotaSnapshotOf(previousAlpha),
			"beta":  quotaSnapshotOf(result("beta", 20)),
		},
		lastChangedRound: map[string]uint64{},
	}

	event := service.commitQuotaRound(5, 2, cfg, []providers.Result{
		currentAlpha,
		result("beta", 20),
	})

	if len(event.ChangedProviderIDs) != 0 {
		t.Fatalf("window reset must not count as usage, got %v", event.ChangedProviderIDs)
	}
	if got, want := event.ProviderIDs, []string{"beta", "alpha"}; !sameStrings(got, want) {
		t.Fatalf("reset provider order = %v, want %v", got, want)
	}
	if _, ok := service.lastChangedRound["alpha"]; ok {
		t.Fatalf("window reset must not record a changed round, got %+v", service.lastChangedRound)
	}
}

func TestCommitQuotaRound_usage_growth_promotes_account(t *testing.T) {
	cfg := &config.AppConfig{Providers: []config.ProviderConfig{
		{ID: "alpha", SortOrder: 1},
		{ID: "beta", SortOrder: 0},
	}}
	service := &AppService{
		lastStatus: []providers.Result{{ProviderID: "beta"}, {ProviderID: "alpha"}},
		quotaSnapshots: map[string]quotaSnapshot{
			"alpha": quotaSnapshotOf(result("alpha", 42)),
			"beta":  quotaSnapshotOf(result("beta", 20)),
		},
		lastChangedRound: map[string]uint64{},
	}

	event := service.commitQuotaRound(5, 2, cfg, []providers.Result{
		result("alpha", 43),
		result("beta", 20),
	})

	if got, want := event.ChangedProviderIDs, []string{"alpha"}; !sameStrings(got, want) {
		t.Fatalf("usage growth changed IDs = %v, want %v", got, want)
	}
	if got, want := event.ProviderIDs, []string{"alpha", "beta"}; !sameStrings(got, want) {
		t.Fatalf("usage growth provider order = %v, want %v", got, want)
	}
	if got, want := service.lastChangedRound["alpha"], uint64(5); got != want {
		t.Fatalf("usage growth changed round = %d, want %d", got, want)
	}
}

func TestCommitQuotaRound_rolled_over_then_used_again_promotes(t *testing.T) {
	cfg := &config.AppConfig{Providers: []config.ProviderConfig{
		{ID: "alpha", SortOrder: 1},
		{ID: "beta", SortOrder: 0},
	}}
	service := &AppService{
		lastStatus: []providers.Result{{ProviderID: "beta"}, {ProviderID: "alpha"}},
		quotaSnapshots: map[string]quotaSnapshot{
			"alpha": quotaSnapshotOf(result("alpha", 100)),
			"beta":  quotaSnapshotOf(result("beta", 20)),
		},
		lastChangedRound: map[string]uint64{},
	}

	// The window rolls over: percent drops to 0, no promotion.
	rollover := service.commitQuotaRound(5, 2, cfg, []providers.Result{
		result("alpha", 0),
		result("beta", 20),
	})
	if len(rollover.ChangedProviderIDs) != 0 {
		t.Fatalf("rollover must not count as usage, got %v", rollover.ChangedProviderIDs)
	}

	// Real consumption after the rollover promotes the account again.
	used := service.commitQuotaRound(6, 2, cfg, []providers.Result{
		result("alpha", 3),
		result("beta", 20),
	})
	if got, want := used.ChangedProviderIDs, []string{"alpha"}; !sameStrings(got, want) {
		t.Fatalf("post-rollover usage changed IDs = %v, want %v", got, want)
	}
	if got, want := used.ProviderIDs, []string{"alpha", "beta"}; !sameStrings(got, want) {
		t.Fatalf("post-rollover usage provider order = %v, want %v", got, want)
	}
}

func TestCommitQuotaRound_error_transitions_do_not_promote_account(t *testing.T) {
	cfg := &config.AppConfig{Providers: []config.ProviderConfig{
		{ID: "alpha", SortOrder: 1},
		{ID: "beta", SortOrder: 0},
	}}
	service := &AppService{
		lastStatus: []providers.Result{{ProviderID: "beta"}, {ProviderID: "alpha"}},
		quotaSnapshots: map[string]quotaSnapshot{
			"alpha": quotaSnapshotOf(result("alpha", 42)),
			"beta":  quotaSnapshotOf(result("beta", 20)),
		},
		lastChangedRound: map[string]uint64{},
	}
	errorResult := providers.Result{ProviderID: "alpha", Error: "request failed"}

	first := service.commitQuotaRound(5, 2, cfg, []providers.Result{errorResult, result("beta", 20)})
	if len(first.ChangedProviderIDs) != 0 {
		t.Fatalf("error transition must not count as usage, got %v", first.ChangedProviderIDs)
	}
	if _, ok := service.lastChangedRound["alpha"]; ok {
		t.Fatalf("error transition must not record a changed round, got %+v", service.lastChangedRound)
	}

	repeated := service.commitQuotaRound(6, 2, cfg, []providers.Result{errorResult, result("beta", 20)})
	if len(repeated.ChangedProviderIDs) != 0 {
		t.Fatalf("repeated error must not count as usage, got %v", repeated.ChangedProviderIDs)
	}

	recovered := service.commitQuotaRound(7, 2, cfg, []providers.Result{result("alpha", 42), result("beta", 20)})
	if len(recovered.ChangedProviderIDs) != 0 {
		t.Fatalf("error recovery must not count as usage, got %v", recovered.ChangedProviderIDs)
	}
	if got, want := recovered.ProviderIDs, []string{"beta", "alpha"}; !sameStrings(got, want) {
		t.Fatalf("recovery provider order = %v, want %v", got, want)
	}

	// The account is back and consuming again: it promotes.
	promoted := service.commitQuotaRound(8, 2, cfg, []providers.Result{result("alpha", 43), result("beta", 20)})
	if got, want := promoted.ChangedProviderIDs, []string{"alpha"}; !sameStrings(got, want) {
		t.Fatalf("usage after recovery changed IDs = %v, want %v", got, want)
	}
}
