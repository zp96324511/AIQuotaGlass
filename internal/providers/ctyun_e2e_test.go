//go:build ctyune2e

package providers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"aiquotaglass/internal/config"
)

// Real-account end-to-end test. Run explicitly with:
//
//	go test ./internal/providers/ -tags ctyune2e -run TestCtyunE2E -v
//
// Credentials come from env so they never land in the repo.
func TestCtyunE2E(t *testing.T) {
	account := osGetenv("CTYUN_ACCOUNT")
	password := osGetenv("CTYUN_PASSWORD")
	if account == "" || password == "" {
		t.Skip("CTYUN_ACCOUNT / CTYUN_PASSWORD not set")
	}
	cfg := config.ProviderConfig{
		ID: "e2e-ctyun", Type: "ctyun", Name: "天翼云E2E",
		Workspace: account, Cookie: password,
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := p.Query(ctx)
	if err != nil || (res != nil && res.Error != "") {
		t.Fatalf("query failed: err=%v res.Error=%q", err, res.Error)
	}
	for _, w := range res.Windows {
		fmt.Printf("  window %-8s label=%-6s percent=%.3f reset=%ds\n", w.Key, w.Label, w.Percent, w.ResetInSec)
	}
	if len(res.Windows) != 3 {
		t.Fatalf("windows = %d, want 3", len(res.Windows))
	}
}

func TestCtyunE2EUsedTotals(t *testing.T) {
	account := osGetenv("CTYUN_ACCOUNT")
	password := osGetenv("CTYUN_PASSWORD")
	if account == "" || password == "" {
		t.Skip("CTYUN_ACCOUNT / CTYUN_PASSWORD not set")
	}
	cfg := config.ProviderConfig{
		ID: "e2e-ctyun", Type: "ctyun", Name: "天翼云E2E",
		Workspace: account, Cookie: password,
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := p.Query(ctx)
	if err != nil || (res != nil && res.Error != "") {
		t.Fatalf("query failed: err=%v res.Error=%q", err, res.Error)
	}
	for _, w := range res.Windows {
		fmt.Printf("  window %-8s used=%.1f total=%.0f\n", w.Key, w.Used, w.Total)
	}
	// Lite plan: caps must be 1200/9000/18000 and Used = ratio × cap.
	want := map[string]float64{"5h": 1200, "weekly": 9000, "monthly": 18000}
	for _, w := range res.Windows {
		if w.Total != want[w.Key] {
			t.Errorf("%s total = %v, want %v", w.Key, w.Total, want[w.Key])
		}
		if w.Used < 0 || w.Used > w.Total {
			t.Errorf("%s used = %v out of range [0,%v]", w.Key, w.Used, w.Total)
		}
	}
}
