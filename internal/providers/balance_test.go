package providers

import (
	"testing"
)

func TestBalanceWindowMapsBalanceToPercent(t *testing.T) {
	// Given: prepaid balances above, at, and below the 100-unit reference.
	cases := []struct {
		balance float64
		percent float64
	}{
		{120, 0},   // >= 100: full
		{100, 0},   // exactly the reference: full
		{50, 50},   // half left: half consumed
		{12.34, 87.66},
		{0, 100},   // depleted
	}
	for _, c := range cases {
		w := balanceWindow(c.balance, "CNY", "余额")
		if w.Percent != c.percent {
			t.Fatalf("balance %.2f: percent = %v, want %v", c.balance, w.Percent, c.percent)
		}
		if w.Used != c.balance {
			t.Fatalf("balance %.2f: used = %v, want the raw balance", c.balance, w.Used)
		}
		if w.Unit != "CNY" {
			t.Fatalf("unit = %q, want CNY", w.Unit)
		}
	}
}

func TestParseDeepSeekBalance(t *testing.T) {
	body := []byte(`{
		"is_available": true,
		"balance_infos": [
			{"currency": "CNY", "total_balance": 88.5, "granted_balance": 0, "topped_up_balance": 88.5}
		]
	}`)
	windows, err := parseDeepSeekBalance(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(windows))
	}
	w := windows[0]
	if w.Key != "balance" || w.Label != "余额" {
		t.Fatalf("window key/label = %s/%s", w.Key, w.Label)
	}
	if w.Used != 88.5 || w.Percent != 11.5 || w.Unit != "CNY" {
		t.Fatalf("window = %+v, want used 88.5, percent 11.5, unit CNY", w)
	}
}

func TestParseDeepSeekBalanceMissingEntries(t *testing.T) {
	if _, err := parseDeepSeekBalance([]byte(`{"is_available": true, "balance_infos": []}`)); err == nil {
		t.Fatal("want error for empty balance_infos")
	}
}

func TestParseOpenRouterCredits(t *testing.T) {
	body := []byte(`{
		"data": {"total_credits": 60, "total_usage": 15.5}
	}`)
	windows, err := parseOpenRouterCredits(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	w := windows[0]
	if w.Used != 44.5 || w.Percent != 55.5 || w.Unit != "USD" {
		t.Fatalf("window = %+v, want used 44.5, percent 55.5, unit USD", w)
	}
}

func TestParseOpenRouterCreditsDepleted(t *testing.T) {
	body := []byte(`{
		"data": {"total_credits": 10, "total_usage": 20}
	}`)
	windows, err := parseOpenRouterCredits(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if w := windows[0]; w.Used != 0 || w.Percent != 100 {
		t.Fatalf("window = %+v, want used 0, percent 100", w)
	}
}
