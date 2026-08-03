package main

import (
	"testing"

	"aiquotaglass/internal/config"
	"aiquotaglass/internal/providers"
)

func TestCommitQuotaRound_detects_window_reset_when_countdown_restarts(t *testing.T) {
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

	if got, want := event.ChangedProviderIDs, []string{"alpha"}; !sameStrings(got, want) {
		t.Fatalf("reset changed IDs = %v, want %v", got, want)
	}
	if got, want := event.ProviderIDs, []string{"alpha", "beta"}; !sameStrings(got, want) {
		t.Fatalf("reset provider order = %v, want %v", got, want)
	}
}

func TestQuotaSnapshots_detect_window_status_changes_without_numeric_change(t *testing.T) {
	previous := quotaSnapshotOf(providers.Result{
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: 42, Used: 4.2, Total: 10, Status: "ok",
		}},
	})
	current := quotaSnapshotOf(providers.Result{
		Windows: []providers.WindowStatus{{
			Key: "5h", Percent: 42, Used: 4.2, Total: 10, Status: "error",
		}},
	})

	if quotaSnapshotsEqual(previous, current) {
		t.Fatal("window status change should be treated as activity")
	}
}

func TestQuotaSnapshots_detect_reset_for_each_standard_window(t *testing.T) {
	for _, key := range []string{"5h", "weekly", "monthly"} {
		t.Run(key, func(t *testing.T) {
			previous := quotaSnapshotOf(providers.Result{
				Windows: []providers.WindowStatus{{
					Key: key, Percent: 42, Used: 4.2, Total: 10, ResetInSec: 2, Status: "ok",
				}},
			})
			current := quotaSnapshotOf(providers.Result{
				Windows: []providers.WindowStatus{{
					Key: key, Percent: 42, Used: 4.2, Total: 10, ResetInSec: 18000, Status: "ok",
				}},
			})

			if quotaSnapshotsEqual(previous, current) {
				t.Fatalf("%s reset should be treated as activity", key)
			}
		})
	}
}

func TestQuotaSnapshots_ignore_small_reset_countdown_jitter(t *testing.T) {
	previous := quotaSnapshotOf(providers.Result{
		Windows: []providers.WindowStatus{{
			Key: "weekly", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 3600, Status: "ok",
		}},
	})
	current := quotaSnapshotOf(providers.Result{
		Windows: []providers.WindowStatus{{
			Key: "weekly", Percent: 42, Used: 4.2, Total: 10, ResetInSec: 3601, Status: "ok",
		}},
	})

	if !quotaSnapshotsEqual(previous, current) {
		t.Fatal("small reset countdown jitter should not count as activity")
	}
}

func TestQuotaResetChanged_negative_sentinel_transitions(t *testing.T) {
	tests := []struct {
		name     string
		previous int64
		current  int64
		want     bool
	}{
		{name: "no reset to no reset", previous: -1, current: -1, want: false},
		{name: "no reset to countdown", previous: -1, current: 18000, want: true},
		{name: "countdown to no reset", previous: 18000, current: -1, want: true},
		{name: "countdown decrease", previous: 3600, current: 3599, want: false},
		{name: "countdown small increase", previous: 3600, current: 3601, want: false},
		{name: "countdown reset restart", previous: 2, current: 18000, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quotaResetChanged(tt.previous, tt.current); got != tt.want {
				t.Fatalf("quotaResetChanged(%d, %d) = %v, want %v", tt.previous, tt.current, got, tt.want)
			}
		})
	}
}

func TestCommitQuotaRound_prioritizes_error_transition_and_recovery(t *testing.T) {
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
	if got, want := first.ChangedProviderIDs, []string{"alpha"}; !sameStrings(got, want) {
		t.Fatalf("error transition changed IDs = %v, want %v", got, want)
	}

	repeated := service.commitQuotaRound(6, 2, cfg, []providers.Result{errorResult, result("beta", 20)})
	if len(repeated.ChangedProviderIDs) != 0 {
		t.Fatalf("repeated identical error should not retrigger sorting, got %v", repeated.ChangedProviderIDs)
	}

	recovered := service.commitQuotaRound(7, 2, cfg, []providers.Result{result("alpha", 42), result("beta", 20)})
	if got, want := recovered.ChangedProviderIDs, []string{"alpha"}; !sameStrings(got, want) {
		t.Fatalf("error recovery changed IDs = %v, want %v", got, want)
	}
	if got, want := service.lastChangedRound["alpha"], uint64(7); got != want {
		t.Fatalf("error recovery changed round = %d, want %d", got, want)
	}
}
