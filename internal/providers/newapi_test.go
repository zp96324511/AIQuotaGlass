package providers

import (
	"fmt"
	"testing"
	"time"
)

func TestParseNewAPIQuota(t *testing.T) {
	now := time.UnixMilli(1754000000000)
	body := []byte(`{
		"code": true,
		"message": "ok",
		"data": {
			"object": "token_usage",
			"name": "Default Token",
			"total_granted": 1000000,
			"total_used": 12345,
			"total_available": 987655,
			"unlimited_quota": false,
			"expires_at": 0
		}
	}`)

	windows, err := parseNewAPIQuota(body, now)
	if err != nil {
		t.Fatalf("parseNewAPIQuota: %v", err)
	}
	if len(windows) != 1 {
		t.Fatalf("want 1 window, got %d", len(windows))
	}
	w := windows[0]
	if w.Key != "total" || w.Label != "总配额" || w.Status != "ok" {
		t.Fatalf("window wrong: %+v", w)
	}
	if w.Percent < 1.2344 || w.Percent > 1.2346 {
		t.Fatalf("percent = %v, want ~1.2345", w.Percent)
	}
}

func TestParseNewAPIQuotaUnlimited(t *testing.T) {
	body := []byte(`{"code": true, "message": "ok", "data": {
		"object": "token_usage", "total_granted": 0, "total_used": 0,
		"total_available": 0, "unlimited_quota": true, "expires_at": 0
	}}`)
	windows, err := parseNewAPIQuota(body, time.Now())
	if err != nil {
		t.Fatalf("parseNewAPIQuota: %v", err)
	}
	if len(windows) != 1 || windows[0].Key != "total" || windows[0].Label != "总配额" || windows[0].Percent != -1 || windows[0].ResetInSec != -1 {
		t.Fatalf("want unlimited total window, got %+v", windows)
	}
}

func TestParseNewAPIUsageDetail(t *testing.T) {
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	// expires_at is unix seconds; expires Aug 19 2026 00:00:00 UTC.
	exp := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC).Unix()
	body := []byte(`{"code": true, "message": "ok", "data": {"expires_at": ` + fmt.Sprint(exp) + `}}`)
	d, err := parseNewAPIUsageDetail(body, now)
	if err != nil {
		t.Fatalf("parseNewAPIUsageDetail: %v", err)
	}
	if d.ExpiresAt != "2026-08-19" {
		t.Fatalf("expiresAt = %q, want 2026-08-19", d.ExpiresAt)
	}
	if want := int64(time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC).Sub(now).Seconds()); d.ExpiresInSec != want {
		t.Fatalf("expiresInSec = %d, want %d", d.ExpiresInSec, want)
	}
}

func TestParseNewAPIUsageDetailNeverExpires(t *testing.T) {
	// expires_at 0 means the token never expires → no detail.
	if _, err := parseNewAPIUsageDetail([]byte(`{"code": true, "message": "ok", "data": {"expires_at": 0}}`), time.Now()); err == nil {
		t.Fatal("want error when token never expires")
	}
	if _, err := parseNewAPIUsageDetail([]byte(`{"code": true, "message": "ok", "data": {}}`), time.Now()); err == nil {
		t.Fatal("want error when expires_at is missing")
	}
	if _, err := parseNewAPIUsageDetail([]byte(`not json`), time.Now()); err == nil {
		t.Fatal("want error for malformed JSON")
	}
}

func TestParseNewAPIQuotaErrors(t *testing.T) {
	// Business-level failure (code != true).
	if _, err := parseNewAPIQuota([]byte(`{"code": false, "message": "token not found"}`), time.Now()); err == nil {
		t.Fatal("want error for code=false")
	}
	// Malformed JSON.
	if _, err := parseNewAPIQuota([]byte(`not json`), time.Now()); err == nil {
		t.Fatal("want error for malformed JSON")
	}
	// No quota data (zero grant without unlimited flag).
	if _, err := parseNewAPIQuota([]byte(`{"code": true, "message": "ok", "data": {"total_granted": 0, "total_available": 0, "unlimited_quota": false}}`), time.Now()); err == nil {
		t.Fatal("want error when no quota data")
	}
}
