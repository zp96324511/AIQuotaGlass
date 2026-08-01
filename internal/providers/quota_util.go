package providers

import (
	"strconv"
	"strings"
	"time"
)

// valueAsFloat reads a JSON number or a numeric string ("100") from an any.
// JSON numbers unmarshal into float64.
func valueAsFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// resetInSec converts a reset time value into seconds remaining from now.
// Accepts an ISO8601 string or a numeric epoch in seconds (<1e12) or
// milliseconds (>=1e12); zero/negative values mean "no reset time".
func resetInSec(v any, now time.Time) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case string:
		ts, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return 0, false
		}
		sec := int64(ts.Sub(now).Seconds())
		if sec < 0 {
			sec = 0
		}
		return sec, true
	case float64:
		if t <= 0 {
			return 0, false
		}
		ms := t
		if t < 1e12 {
			ms = t * 1000
		}
		sec := int64(ms/1000) - now.Unix()
		if sec < 0 {
			sec = 0
		}
		return sec, true
	}
	return 0, false
}

// windowFromLimitRemaining builds a WindowStatus from a (limit, remaining)
// quota pair, converting to used percent. limit <= 0 means the window is not
// applicable and is skipped.
func windowFromLimitRemaining(limitV, remainV, resetV any, key, label string, now time.Time) (WindowStatus, bool) {
	limit, ok := valueAsFloat(limitV)
	if !ok || limit <= 0 {
		return WindowStatus{}, false
	}
	remaining, ok := valueAsFloat(remainV)
	if !ok {
		remaining = 0
	}
	used := limit - remaining
	if used < 0 {
		used = 0
	}
	percent := used / limit * 100
	if percent > 100 {
		percent = 100
	}
	w := WindowStatus{Key: key, Label: label, Percent: percent, Status: "ok"}
	if sec, ok := resetInSec(resetV, now); ok {
		w.ResetInSec = sec
	}
	return w, true
}
