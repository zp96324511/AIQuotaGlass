package providers

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWindowFromLimitRemainingPreservesQuotaValues(t *testing.T) {
	// Given: a finite quota with a known remaining amount.
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	// When: the quota window is built for the widget.
	w, ok := windowFromLimitRemaining(10.0, 6.25, nil, "total", "总配额", now)

	// Then: both raw values remain available for the hover display.
	if !ok {
		t.Fatal("want finite quota window")
	}
	if w.Used != 3.75 {
		t.Fatalf("used = %v, want 3.75", w.Used)
	}
	if w.Total != 10 {
		t.Fatalf("total = %v, want 10", w.Total)
	}
}

func TestWindowStatusSerializesZeroUsedQuota(t *testing.T) {
	// Given: a valid quota window with no usage yet.
	w := WindowStatus{Used: 0, Total: 10}

	// When: the window is serialized for the frontend binding.
	body, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal window status: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal window status: %v", err)
	}

	// Then: the zero value is retained so the hover can show 已用 0 / 总量 10.
	if _, ok := payload["used"]; !ok {
		t.Fatalf("used is absent from %s", body)
	}
}
