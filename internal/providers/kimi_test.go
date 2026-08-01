package providers

import (
	"math"
	"testing"
	"time"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestParseKimiQuota(t *testing.T) {
	now := time.UnixMilli(1754000000000)
	body := []byte(`{
		"limits": [
			{"detail": {"limit": 1000000, "remaining": 725000, "resetTime": "2026-08-01T05:00:00Z"}}
		],
		"usage": {"limit": "10000000", "remaining": 8800000, "resetTime": 1754600000000}
	}`)

	windows, err := parseKimiQuota(body, now)
	if err != nil {
		t.Fatalf("parseKimiQuota: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("want 2 windows, got %d", len(windows))
	}
	five := windows[0]
	if five.Key != "5h" || !approx(five.Percent, 27.5, 1e-9) {
		t.Fatalf("5h window wrong: %+v", five)
	}
	if five.ResetInSec <= 0 {
		t.Fatalf("5h reset should be set, got %d", five.ResetInSec)
	}
	weekly := windows[1]
	if weekly.Key != "weekly" || weekly.Percent != 12 {
		t.Fatalf("weekly window wrong: %+v", weekly)
	}
	if weekly.ResetInSec != 600000 {
		t.Fatalf("weekly reset wrong: %d", weekly.ResetInSec)
	}
}

func TestParseKimiQuotaWeeklyOnly(t *testing.T) {
	// Plans without a 5h limit have no limits entries; weekly still works.
	body := []byte(`{"limits": [], "usage": {"limit": 100, "remaining": 50}}`)
	windows, err := parseKimiQuota(body, time.Now())
	if err != nil {
		t.Fatalf("parseKimiQuota: %v", err)
	}
	if len(windows) != 1 || windows[0].Key != "weekly" || windows[0].Percent != 50 {
		t.Fatalf("want single weekly window, got %+v", windows)
	}
}

func TestParseKimiQuotaNumericResetSeconds(t *testing.T) {
	// Numeric resetTime in seconds (not milliseconds).
	now := time.Unix(1754000000, 0)
	body := []byte(`{"limits": [{"detail": {"limit": 100, "remaining": 0, "resetTime": 1754001800}}]}`)
	windows, err := parseKimiQuota(body, now)
	if err != nil {
		t.Fatalf("parseKimiQuota: %v", err)
	}
	if windows[0].ResetInSec != 1800 {
		t.Fatalf("want 1800s reset, got %d", windows[0].ResetInSec)
	}
}

func TestParseKimiQuotaErrors(t *testing.T) {
	if _, err := parseKimiQuota([]byte(`not json`), time.Now()); err == nil {
		t.Fatal("want error for malformed JSON")
	}
	if _, err := parseKimiQuota([]byte(`{"limits": [], "usage": {}}`), time.Now()); err == nil {
		t.Fatal("want error when no usage entries")
	}
}
