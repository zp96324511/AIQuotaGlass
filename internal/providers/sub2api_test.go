package providers

import (
	"testing"
	"time"
)

func TestParseSub2APIUsageQuotaLimited(t *testing.T) {
	now := time.UnixMilli(1754000000000)
	body := []byte(`{
		"mode": "quota_limited",
		"isValid": true,
		"status": "active",
		"quota": {"limit": 10.0, "used": 4.0, "remaining": 6.0, "unit": "USD"},
		"remaining": 6.0,
		"unit": "USD",
		"rate_limits": [
			{"window": "5h", "limit": 50000, "used": 10000, "remaining": 40000, "reset_at": "2026-08-01T12:00:00Z"},
			{"window": "1d", "limit": 100000, "used": 25000, "remaining": 75000, "reset_at": "2026-08-02T00:00:00Z"},
			{"window": "7d", "limit": 700000, "used": 70000, "remaining": 630000}
		],
		"expires_at": null
	}`)

	windows, err := parseSub2APIUsage(body, now)
	if err != nil {
		t.Fatalf("parseSub2APIUsage: %v", err)
	}
	if len(windows) != 4 {
		t.Fatalf("want 4 windows, got %d: %+v", len(windows), windows)
	}
	byKey := map[string]WindowStatus{}
	for _, w := range windows {
		byKey[w.Key] = w
	}

	total, ok := byKey["total"]
	if !ok || total.Percent != 40 {
		t.Fatalf("total window wrong: %+v", byKey["total"])
	}
	// key quota has no auto reset (manual admin top-up only): countdown hidden
	if total.ResetInSec != -1 {
		t.Fatalf("total window should carry no-reset sentinel, got %d", total.ResetInSec)
	}
	five, ok := byKey["5h"]
	if !ok || five.Percent != 20 || five.ResetInSec == 0 {
		t.Fatalf("5h window wrong: %+v", byKey["5h"])
	}
	day, ok := byKey["1d"]
	if !ok || day.Percent != 25 {
		t.Fatalf("1d window wrong: %+v", byKey["1d"])
	}
	week, ok := byKey["7d"]
	if !ok || week.Percent != 10 {
		t.Fatalf("7d window wrong: %+v", byKey["7d"])
	}
}

func TestParseSub2APIUsageSubscription(t *testing.T) {
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	body := []byte(`{
		"mode": "unrestricted",
		"isValid": true,
		"planName": "Grok 订阅",
		"unit": "USD",
		"remaining": 12.0,
		"subscription": {
			"daily_usage_usd": 0.8,
			"weekly_usage_usd": 4.2,
			"monthly_usage_usd": 12.0,
			"daily_limit_usd": 5.0,
			"weekly_limit_usd": 25.0,
			"monthly_limit_usd": 100.0,
			"weekly_window_start": "2026-07-19T00:00:00Z",
			"expires_at": "2026-08-19T00:00:00Z"
		}
	}`)

	windows, err := parseSub2APIUsage(body, now)
	if err != nil {
		t.Fatalf("parseSub2APIUsage: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("want 3 windows, got %d: %+v", len(windows), windows)
	}
	byKey := map[string]WindowStatus{}
	for _, w := range windows {
		byKey[w.Key] = w
	}
	// daily 0.8/5 = 16%, weekly 4.2/25 = 16.8%, monthly 12/100 = 12%
	if day := byKey["1d"]; day.Percent < 15.99 || day.Percent > 16.01 {
		t.Fatalf("daily window wrong: %+v", byKey["1d"])
	}
	if week := byKey["weekly"]; week.Percent < 16.79 || week.Percent > 16.81 {
		t.Fatalf("weekly window wrong: %+v", byKey["weekly"])
	}
	// weekly window resets 7d after weekly_window_start (2026-07-19) = 2026-07-26T00:00:00Z
	if week := byKey["weekly"]; week.ResetInSec != int64(time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC).Sub(now).Seconds()) {
		t.Fatalf("weekly reset wrong: %+v", week)
	}
	if month := byKey["monthly"]; month.Percent != 12 {
		t.Fatalf("monthly window wrong: %+v", byKey["monthly"])
	}
}

func TestParseSub2APIUsageWallet(t *testing.T) {
	body := []byte(`{
		"mode": "unrestricted",
		"isValid": true,
		"planName": "钱包余额",
		"remaining": 3.5,
		"unit": "USD",
		"balance": 3.5
	}`)
	windows, err := parseSub2APIUsage(body, time.Now())
	if err != nil {
		t.Fatalf("parseSub2APIUsage: %v", err)
	}
	if len(windows) != 1 || windows[0].Key != "total" || windows[0].Label != "余额" || windows[0].Percent != -1 || windows[0].ResetInSec != -1 {
		t.Fatalf("want unlimited wallet window, got %+v", windows)
	}
}

func TestParseSub2APIUsageErrors(t *testing.T) {
	// Invalid key state.
	if _, err := parseSub2APIUsage([]byte(`{"mode": "quota_limited", "isValid": false, "status": "disabled"}`), time.Now()); err == nil {
		t.Fatal("want error for isValid=false")
	}
	// Malformed JSON.
	if _, err := parseSub2APIUsage([]byte(`not json`), time.Now()); err == nil {
		t.Fatal("want error for malformed JSON")
	}
	// quota_limited with no quota and no rate limits.
	if _, err := parseSub2APIUsage([]byte(`{"mode": "quota_limited", "isValid": true, "status": "active"}`), time.Now()); err == nil {
		t.Fatal("want error when no quota data")
	}
}

func TestParseSub2APIUsageDetail(t *testing.T) {
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	body := []byte(`{
		"mode": "quota_limited",
		"isValid": true,
		"planName": "",
		"expires_at": "2026-08-19T00:00:00Z",
		"usage": {
			"today": {
				"requests": 12,
				"input_tokens": 1000,
				"output_tokens": 500,
				"cache_creation_tokens": 0,
				"cache_read_tokens": 1500,
				"total_tokens": 3000,
				"cost": 0.20,
				"actual_cost": 0.1878
			},
			"total": {
				"requests": 100,
				"cost": 15.0,
				"actual_cost": 13.9
			}
		},
		"daily_usage": [
			{"date": "2026-07-31", "requests": 5, "cost": 0.9, "actual_cost": 0.85},
			{"date": "2026-08-01", "requests": 7, "cost": 1.1, "actual_cost": 1.05}
		]
	}`)
	d, err := parseSub2APIUsageDetail(body, now)
	if err != nil {
		t.Fatalf("parseSub2APIUsageDetail: %v", err)
	}
	if d.Requests != 12 {
		t.Fatalf("requests = %d, want 12", d.Requests)
	}
	if d.TodayCost != 0.1878 {
		t.Fatalf("todayCost = %v, want 0.1878", d.TodayCost)
	}
	if d.PeriodCost != 1.9 {
		t.Fatalf("periodCost = %v, want 1.9", d.PeriodCost)
	}
	if d.CacheHit != 50 {
		t.Fatalf("cacheHit = %v, want 50", d.CacheHit)
	}
	if d.ExpiresAt != "2026-08-19" {
		t.Fatalf("expiresAt = %q, want 2026-08-19", d.ExpiresAt)
	}
	if want := int64(time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC).Sub(now).Seconds()); d.ExpiresInSec != want {
		t.Fatalf("expiresInSec = %d, want %d", d.ExpiresInSec, want)
	}
}

func TestParseSub2APIUsageDetailSubscription(t *testing.T) {
	// Subscription mode: group name from planName, expiry from subscription.
	body := []byte(`{
		"mode": "unrestricted",
		"isValid": true,
		"planName": "Grok 订阅",
		"remaining": 12.0,
		"subscription": {
			"daily_usage_usd": 0.8,
			"weekly_usage_usd": 4.2,
			"monthly_usage_usd": 12.0,
			"daily_limit_usd": 5.0,
			"weekly_limit_usd": 25.0,
			"monthly_limit_usd": 100.0,
			"weekly_window_start": "2026-07-19T00:00:00Z",
			"expires_at": "2026-08-01T00:00:00Z"
		}
	}`)
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	d, err := parseSub2APIUsageDetail(body, now)
	if err != nil {
		t.Fatalf("parseSub2APIUsageDetail: %v", err)
	}
	if d.GroupName != "Grok 订阅" {
		t.Fatalf("groupName = %q, want Grok 订阅", d.GroupName)
	}
	if d.ExpiresAt != "2026-08-01" {
		t.Fatalf("expiresAt = %q, want 2026-08-01", d.ExpiresAt)
	}
}

func TestParseSub2APIUsageDetailWallet(t *testing.T) {
	// Wallet mode: planName is a placeholder, no spend/group/expiry detail —
	// the parser reports "no usage data" so the widget skips the detail line.
	body := []byte(`{
		"mode": "unrestricted",
		"isValid": true,
		"planName": "钱包余额",
		"remaining": 3.5,
		"unit": "USD",
		"balance": 3.5
	}`)
	if _, err := parseSub2APIUsageDetail(body, time.Now()); err == nil {
		t.Fatal("want error (no detail data) for wallet mode")
	}
}

func TestParseSub2APIBilling(t *testing.T) {
	rate, peak, err := parseSub2APIBilling([]byte(`{
		"object": "sub2api.key_billing",
		"schema_version": 1,
		"billing_scope": "token",
		"group_rate_multiplier": 1.0,
		"resolved_rate_multiplier": 1.0,
		"peak_rate_enabled": true,
		"peak_start": "21:00",
		"peak_end": "07:00",
		"peak_rate_multiplier": 2.0,
		"applied_peak_multiplier": 2.0,
		"effective_rate_multiplier": 2.0
	}`))
	if err != nil {
		t.Fatalf("parseSub2APIBilling: %v", err)
	}
	if rate != 2.0 || !peak {
		t.Fatalf("rate=%v peak=%v, want 2.0/true", rate, peak)
	}

	// Outside the peak window the applied multiplier is 1.0.
	rate, peak, err = parseSub2APIBilling([]byte(`{
		"peak_rate_enabled": true,
		"applied_peak_multiplier": 1.0,
		"effective_rate_multiplier": 1.0
	}`))
	if err != nil {
		t.Fatalf("parseSub2APIBilling: %v", err)
	}
	if peak {
		t.Fatal("peak should be inactive when applied multiplier is 1.0")
	}
	if rate != 1.0 {
		t.Fatalf("rate = %v, want 1.0", rate)
	}
}

func TestParseSub2APIBillingErrors(t *testing.T) {
	if _, _, err := parseSub2APIBilling([]byte(`not json`)); err == nil {
		t.Fatal("want error for malformed JSON")
	}
	if _, _, err := parseSub2APIBilling([]byte(`{"effective_rate_multiplier": 0}`)); err == nil {
		t.Fatal("want error when no rate multiplier")
	}
}

func TestParseSub2APIUsageDetailFallbacks(t *testing.T) {
	// actual_cost <= 0 falls back to standard cost; empty usage errors out.
	body := []byte(`{"usage": {"today": {"requests": 3, "cost": 0.5, "actual_cost": 0}},
		"daily_usage": [{"date": "2026-08-01", "cost": 2.0, "actual_cost": 0}]}`)
	d, err := parseSub2APIUsageDetail(body, time.Now())
	if err != nil {
		t.Fatalf("parseSub2APIUsageDetail: %v", err)
	}
	if d.TodayCost != 0.5 || d.PeriodCost != 2.0 {
		t.Fatalf("fallback cost wrong: %+v", d)
	}
	if _, err := parseSub2APIUsageDetail([]byte(`{"mode": "quota_limited"}`), time.Now()); err == nil {
		t.Fatal("want error when no usage data")
	}
	if _, err := parseSub2APIUsageDetail([]byte(`not json`), time.Now()); err == nil {
		t.Fatal("want error for malformed JSON")
	}
}
