package providers

import (
	"testing"
	"time"
)

func TestParseMiniMaxQuota(t *testing.T) {
	now := time.UnixMilli(1754000000000)
	body := []byte(`{
		"base_resp": {"status_code": 0, "status_msg": "success"},
		"model_remains": [
			{"model_name": "general", "current_interval_remaining_percent": 72.5,
			 "end_time": 1754018000000, "current_weekly_status": 1,
			 "current_weekly_remaining_percent": 88, "weekly_end_time": 1754600000000},
			{"model_name": "video", "current_interval_remaining_percent": 10,
			 "current_weekly_status": 3, "current_weekly_remaining_percent": 100}
		]
	}`)

	windows, err := parseMiniMaxQuota(body, now)
	if err != nil {
		t.Fatalf("parseMiniMaxQuota: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("want 2 windows, got %d", len(windows))
	}
	five := windows[0]
	if five.Key != "5h" || five.Percent != 27.5 {
		t.Fatalf("5h window wrong: %+v", five)
	}
	if five.ResetInSec != 18000 {
		t.Fatalf("5h reset wrong: %d", five.ResetInSec)
	}
	weekly := windows[1]
	if weekly.Key != "weekly" || weekly.Percent != 12 {
		t.Fatalf("weekly window wrong: %+v", weekly)
	}
	if weekly.ResetInSec != 600000 {
		t.Fatalf("weekly reset wrong: %d", weekly.ResetInSec)
	}
}

func TestParseMiniMaxQuotaWeeklyInactive(t *testing.T) {
	// current_weekly_status != 1 means no weekly limit; only the 5h bucket.
	body := []byte(`{
		"base_resp": {"status_code": 0},
		"model_remains": [
			{"model_name": "general", "current_interval_remaining_percent": 50,
			 "current_weekly_status": 3, "current_weekly_remaining_percent": 100}
		]
	}`)
	windows, err := parseMiniMaxQuota(body, time.Now())
	if err != nil {
		t.Fatalf("parseMiniMaxQuota: %v", err)
	}
	if len(windows) != 1 || windows[0].Key != "5h" || windows[0].Percent != 50 {
		t.Fatalf("want single 5h window, got %+v", windows)
	}
}

func TestParseMiniMaxQuotaErrors(t *testing.T) {
	// Business-level failure in base_resp.
	if _, err := parseMiniMaxQuota([]byte(`{
		"base_resp": {"status_code": 2001, "status_msg": "invalid api key"}
	}`), time.Now()); err == nil {
		t.Fatal("want error for base_resp failure")
	}
	// Missing general model.
	if _, err := parseMiniMaxQuota([]byte(`{
		"base_resp": {"status_code": 0},
		"model_remains": [{"model_name": "video"}]
	}`), time.Now()); err == nil {
		t.Fatal("want error when no general entry")
	}
	// Malformed JSON.
	if _, err := parseMiniMaxQuota([]byte(`not json`), time.Now()); err == nil {
		t.Fatal("want error for malformed JSON")
	}
}
