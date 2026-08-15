package providers

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"aiquotaglass/internal/config"
)

const electronhubSampleHistory = `{
  "subscription": "free",
  "credits": 0.25,
  "usage": {"input_tokens": 9555770, "output_tokens": 50869},
  "history": [
    {"date": "2026-08-14", "requests": 107, "credits": 0.0, "input_tokens": 9555770, "output_tokens": 50869},
    {"date": "2026-08-13", "requests": 42, "credits": 0.0, "input_tokens": 400000, "output_tokens": 2000},
    {"date": "2026-08-12", "requests": 1, "credits": 0.0, "input_tokens": 0, "output_tokens": 0}
  ]
}`

func TestParseElectronhubUserMe(t *testing.T) {
	now := time.Date(2026, 8, 14, 17, 45, 0, 0, time.Local)
	windows, detail, err := parseElectronhubUserMe([]byte(electronhubSampleHistory), now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(windows))
	}
	daily := windows[0]
	if daily.Key != "5h" || daily.Label != "今日" {
		t.Fatalf("daily window key/label = %q/%q", daily.Key, daily.Label)
	}
	if want := 9555770.0 + 50869; daily.Used != want {
		t.Fatalf("daily used = %v, want %v", daily.Used, want)
	}
	if daily.Total != 20_000_000 {
		t.Fatalf("daily total = %v, want 20M", daily.Total)
	}
	if want := (9555770.0 + 50869) / 20_000_000 * 100; daily.Percent != want {
		t.Fatalf("daily percent = %v, want %v", daily.Percent, want)
	}
	if daily.ResetInSec != -1 {
		t.Fatalf("daily resetInSec = %d, want -1 (no countdown)", daily.ResetInSec)
	}
	weekly := windows[1]
	if weekly.Key != "weekly" || weekly.Label != "本周" {
		t.Fatalf("weekly window key/label = %q/%q", weekly.Key, weekly.Label)
	}
	if want := 9555770.0 + 50869 + 400000 + 2000; weekly.Used != want {
		t.Fatalf("weekly used = %v, want %v", weekly.Used, want)
	}
	if weekly.Total != 100_000_000 {
		t.Fatalf("weekly total = %v, want 100M", weekly.Total)
	}
	if detail == nil || !detail.HasUsageMetrics() {
		t.Fatalf("detail missing/not marked: %+v", detail)
	}
	if detail.Requests != 107 {
		t.Fatalf("today requests = %d, want 107", detail.Requests)
	}
	if detail.WeeklyRequests != 150 {
		t.Fatalf("weekly requests = %d, want 150", detail.WeeklyRequests)
	}
}

func TestParseElectronhubUserMeSingleDay(t *testing.T) {
	// Fresh account: only one day of history — weekly == today.
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	body := `{"history":[{"date":"2026-08-14","requests":3,"input_tokens":100,"output_tokens":5}]}`
	windows, detail, err := parseElectronhubUserMe([]byte(body), now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if windows[0].Used != 105 || windows[1].Used != 105 {
		t.Fatalf("single-day used = %v/%v, want 105/105", windows[0].Used, windows[1].Used)
	}
	if detail.Requests != 3 || detail.WeeklyRequests != 3 {
		t.Fatalf("requests = %d/%d, want 3/3", detail.Requests, detail.WeeklyRequests)
	}
}

func TestParseElectronhubUserMeUnsortedHistory(t *testing.T) {
	// Server may return history in insertion order (oldest first) — the
	// parser must pick the local-today entry by date, not by index.
	now := time.Date(2026, 8, 15, 9, 57, 0, 0, time.Local)
	body := `{"history":[
		{"date":"2026-08-14","requests":433,"input_tokens":49725537,"output_tokens":210278},
		{"date":"2026-08-15","requests":124,"input_tokens":17117521,"output_tokens":29940}
	]}`
	windows, detail, err := parseElectronhubUserMe([]byte(body), now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := 17117521.0 + 29940; windows[0].Used != want {
		t.Fatalf("today used = %v, want %v (must be the 08-15 entry)", windows[0].Used, want)
	}
	if detail.Requests != 124 {
		t.Fatalf("today requests = %d, want 124", detail.Requests)
	}
}

func TestParseElectronhubUserMeTodayNotBucketedYet(t *testing.T) {
	// Early morning UTC+8: the server has not bucketed the local day (UTC
	// boundary lag). The newest entry is shown instead of zeros.
	now := time.Date(2026, 8, 15, 6, 30, 0, 0, time.Local)
	body := `{"history":[{"date":"2026-08-14","requests":433,"input_tokens":1000,"output_tokens":10}]}`
	windows, detail, err := parseElectronhubUserMe([]byte(body), now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if windows[0].Used != 1010 {
		t.Fatalf("fallback today used = %v, want 1010 (newest bucket)", windows[0].Used)
	}
	if detail.Requests != 433 {
		t.Fatalf("fallback requests = %d, want 433", detail.Requests)
	}
}

func TestParseElectronhubUserMeWeeklyWindow(t *testing.T) {
	// Weekly sums only buckets within 7 days of the newest entry; older
	// entries (subscription >7 days) are excluded.
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.Local)
	body := `{"history":[
		{"date":"2026-08-20","requests":1,"input_tokens":100,"output_tokens":0},
		{"date":"2026-08-19","requests":2,"input_tokens":200,"output_tokens":0},
		{"date":"2026-08-14","requests":3,"input_tokens":300,"output_tokens":0},
		{"date":"2026-08-13","requests":4,"input_tokens":400,"output_tokens":0}
	]}`
	_, detail, err := parseElectronhubUserMe([]byte(body), now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if detail.WeeklyRequests != 6 {
		t.Fatalf("weekly requests = %d, want 6 (08-14..08-20 inclusive, 08-13 excluded)", detail.WeeklyRequests)
	}
}

func TestParseElectronhubUserMeOverLimit(t *testing.T) {
	// DevPass never hard-stops: usage past the reference cap stays at 100%.
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	body := `{"history":[{"date":"2026-08-14","requests":5,"input_tokens":25000000,"output_tokens":1}]}`
	windows, _, err := parseElectronhubUserMe([]byte(body), now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if windows[0].Percent != 100 {
		t.Fatalf("over-limit daily percent = %v, want 100", windows[0].Percent)
	}
}

func TestParseElectronhubUserMeErrors(t *testing.T) {
	now := time.Now()
	if _, _, err := parseElectronhubUserMe([]byte(`{"history":[]}`), now); err == nil {
		t.Fatal("empty history should error")
	}
	if _, _, err := parseElectronhubUserMe([]byte(`{"history":null}`), now); err == nil {
		t.Fatal("null history should error")
	}
	if _, _, err := parseElectronhubUserMe([]byte(`not json`), now); err == nil {
		t.Fatal("invalid JSON should error")
	}
}

// TestElectronhubLive runs a real query when AQUOTA_EH_KEY is set
// (master key, ek-..., from Console → API keys):
//
//	$env:AQUOTA_EH_KEY='ek-...'; go test ./internal/providers/ -run TestElectronhubLive -v
func TestElectronhubLive(t *testing.T) {
	key := os.Getenv("AQUOTA_EH_KEY")
	if key == "" {
		t.Skip("AQUOTA_EH_KEY not set")
	}
	p := &electronhub{
		cfg: config.ProviderConfig{
			ID: "electronhub-live", Name: "ElectronHub Live", Type: "electronhub",
			Enabled: true, Cookie: key,
		},
		client: &http.Client{Timeout: 20 * time.Second},
	}
	res, err := p.Query(t.Context())
	if err != nil {
		t.Fatalf("query: %v (res.Error=%s)", err, res.Error)
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("result:\n%s", out)
	for _, w := range res.Windows {
		t.Logf("window %s(%s): %.1f%% used=%.0f total=%.0f reset=%ds", w.Key, w.Label, w.Percent, w.Used, w.Total, w.ResetInSec)
	}
}
