package providers

import (
	"testing"
	"time"
)

func TestParseZhipuQuota(t *testing.T) {
	now := time.UnixMilli(1754000000000)
	body := []byte(`{
		"success": true,
		"data": {
			"level": "PRO",
			"limits": [
				{"type": "TOKENS_LIMIT", "percentage": 27.5, "nextResetTime": 1754018000000, "unit": 3, "number": 5},
				{"type": "TOKENS_LIMIT", "percentage": 12, "nextResetTime": 1754600000000, "unit": 6, "number": 7}
			]
		}
	}`)

	windows, err := parseZhipuQuota(body, now)
	if err != nil {
		t.Fatalf("parseZhipuQuota: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("want 2 windows, got %d", len(windows))
	}
	five := windows[0]
	if five.Key != "5h" || five.Percent != 27.5 || five.ResetInSec != 18000 {
		t.Fatalf("5h window wrong: %+v", five)
	}
	weekly := windows[1]
	if weekly.Key != "weekly" || weekly.Percent != 12 || weekly.ResetInSec != 600000 {
		t.Fatalf("weekly window wrong: %+v", weekly)
	}
}

func TestParseZhipuQuotaLegacySingleWindow(t *testing.T) {
	// Legacy plans return a single TOKENS_LIMIT entry (5h only).
	body := []byte(`{"success": true, "data": {"level": "LITE", "limits": [
		{"type": "TOKENS_LIMIT", "percentage": 0, "nextResetTime": 1754018000000, "unit": 3}
	]}}`)
	windows, err := parseZhipuQuota(body, time.UnixMilli(1754000000000))
	if err != nil {
		t.Fatalf("parseZhipuQuota: %v", err)
	}
	if len(windows) != 1 || windows[0].Key != "5h" {
		t.Fatalf("want single 5h window, got %+v", windows)
	}
}

func TestParseZhipuQuotaErrors(t *testing.T) {
	// Business-level failure.
	if _, err := parseZhipuQuota([]byte(`{"success": false, "msg": "invalid key"}`), time.Now()); err == nil {
		t.Fatal("want error for success=false")
	}
	// Malformed JSON.
	if _, err := parseZhipuQuota([]byte(`not json`), time.Now()); err == nil {
		t.Fatal("want error for malformed JSON")
	}
	// Unknown unit only.
	if _, err := parseZhipuQuota([]byte(`{"success": true, "data": {"limits": [
		{"type": "TOKENS_LIMIT", "percentage": 10, "unit": 99}
	]}}`), time.Now()); err == nil {
		t.Fatal("want error when no recognizable window")
	}
}
