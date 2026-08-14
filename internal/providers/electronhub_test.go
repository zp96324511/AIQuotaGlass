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
	windows, detail, err := parseElectronhubUserMe([]byte(electronhubSampleHistory))
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
	body := `{"history":[{"date":"2026-08-14","requests":3,"input_tokens":100,"output_tokens":5}]}`
	windows, detail, err := parseElectronhubUserMe([]byte(body))
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

func TestParseElectronhubUserMeOverLimit(t *testing.T) {
	// DevPass never hard-stops: usage past the reference cap stays at 100%.
	body := `{"history":[{"date":"2026-08-14","requests":5,"input_tokens":25000000,"output_tokens":1}]}`
	windows, _, err := parseElectronhubUserMe([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if windows[0].Percent != 100 {
		t.Fatalf("over-limit daily percent = %v, want 100", windows[0].Percent)
	}
}

func TestParseElectronhubUserMeErrors(t *testing.T) {
	if _, _, err := parseElectronhubUserMe([]byte(`{"history":[]}`)); err == nil {
		t.Fatal("empty history should error")
	}
	if _, _, err := parseElectronhubUserMe([]byte(`{"history":null}`)); err == nil {
		t.Fatal("null history should error")
	}
	if _, _, err := parseElectronhubUserMe([]byte(`not json`)); err == nil {
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
