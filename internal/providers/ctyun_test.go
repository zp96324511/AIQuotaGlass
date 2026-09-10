package providers

import (
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

const ctyunSampleDetail = `{
  "resultCode": 0,
  "resultMsg": "success",
  "data": {
    "updateTime": 1789004450094,
    "usages": [
      {"period": "近5小时", "tips": "14分后刷新限额", "usage": 0.009},
      {"period": "本周", "tips": "3天14时19分后刷新限额", "usage": 0.001},
      {"period": "套餐总量", "tips": "29天14时14分后刷新限额", "usage": 0.001}
    ],
    "expTime": "seconds: 1791561326\n"
  }
}`

func TestParseCtyunUsage(t *testing.T) {
	windows, detail, err := parseCtyunUsage([]byte(ctyunSampleDetail), time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("windows = %d, want 3", len(windows))
	}
	byKey := map[string]WindowStatus{}
	for _, w := range windows {
		byKey[w.Key] = w
	}
	five, ok := byKey["5h"]
	if !ok || five.Label != "近5小时" {
		t.Fatalf("5h window missing or mislabeled: %+v", five)
	}
	if five.Percent < 0.899 || five.Percent > 0.901 {
		t.Fatalf("5h percent = %v, want ~0.9", five.Percent)
	}
	if five.Used != 0.009 || five.Total != 1 {
		t.Fatalf("5h used/total = %v/%v", five.Used, five.Total)
	}
	if five.ResetInSec != 14*60 {
		t.Fatalf("5h resetInSec = %d, want 840", five.ResetInSec)
	}
	weekly := byKey["weekly"]
	if weekly.ResetInSec != 3*86400+14*3600+19*60 {
		t.Fatalf("weekly resetInSec = %d, want %d", weekly.ResetInSec, 3*86400+14*3600+19*60)
	}
	monthly := byKey["monthly"]
	if monthly.Label != "套餐总量" || monthly.Percent < 0.099 || monthly.Percent > 0.101 {
		t.Fatalf("monthly window: %+v", monthly)
	}
	if monthly.ResetInSec != 29*86400+14*3600+14*60 {
		t.Fatalf("monthly resetInSec = %d", monthly.ResetInSec)
	}
	if detail == nil || !detail.HasUsageMetrics() {
		t.Fatalf("detail missing/not marked: %+v", detail)
	}
}

func TestParseCtyunUsageErrors(t *testing.T) {
	if _, _, err := parseCtyunUsage([]byte("not json"), time.Now()); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	badCode, _ := json.Marshal(map[string]any{"resultCode": 500, "resultMsg": "boom"})
	if _, _, err := parseCtyunUsage(badCode, time.Now()); err == nil {
		t.Fatal("expected error for non-zero resultCode")
	}
	noWindows, _ := json.Marshal(map[string]any{"resultCode": 0, "data": map[string]any{"usages": []any{}}})
	if _, _, err := parseCtyunUsage(noWindows, time.Now()); err == nil {
		t.Fatal("expected error for empty usages")
	}
	unknownPeriod, _ := json.Marshal(map[string]any{
		"resultCode": 0,
		"data": map[string]any{"usages": []map[string]any{{"period": "上月", "tips": "", "usage": 0.5}}},
	})
	if _, _, err := parseCtyunUsage(unknownPeriod, time.Now()); err == nil {
		t.Fatal("expected error for unknown period name")
	}
}

func TestCtyunCountdownSec(t *testing.T) {
	cases := []struct {
		tips string
		want int64
	}{
		{"14分后刷新限额", 14 * 60},
		{"4时20分后刷新限额", 4*3600 + 20*60},
		{"3天14时19分后刷新限额", 3*86400 + 14*3600 + 19*60},
		{"29天14时14分后刷新限额", 29*86400 + 14*3600 + 14*60},
		{"", -1},
		{"已刷新", -1},
	}
	for _, c := range cases {
		if got := ctyunCountdownSec(c.tips); got != c.want {
			t.Errorf("ctyunCountdownSec(%q) = %d, want %d", c.tips, got, c.want)
		}
	}
}

func TestCtyunAESDecryptWithKey(t *testing.T) {
	// Round-trip: AES-128-ECB encrypt then decrypt with the same key.
	key := []byte("0123456789abcdef")
	plain := []byte("hello ctyun session key!!") // 25 bytes → 2 blocks with padding
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padded := padPKCS7(plain, 16)
	ct := make([]byte, len(padded))
	for i := 0; i < len(padded); i += 16 {
		block.Encrypt(ct[i:i+16], padded[i:i+16])
	}
	got, err := ctyunAESDecryptWithKey(base64.StdEncoding.EncodeToString(ct), key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("round-trip = %q, want %q", got, plain)
	}

	// Line-wrapped base64 (eaiSysInfo style) must still decode. Note the wrap
	// test uses the fixed gateway key, so encrypt with it.
	gwKey := []byte(ctyunAESKey)
	gwBlock, _ := aes.NewCipher(gwKey)
	gwCT := make([]byte, len(padded))
	for i := 0; i < len(padded); i += 16 {
		gwBlock.Encrypt(gwCT[i:i+16], padded[i:i+16])
	}
	wrapped := insertEvery(base64.StdEncoding.EncodeToString(gwCT), 76)
	if got2, err := ctyunAESDecryptBase64(wrapped); err != nil || string(got2) != string(plain) {
		t.Fatalf("wrapped decode: %v %q", err, got2)
	}

	if _, err := ctyunAESDecryptWithKey("!!!not-base64!!!", key); err == nil {
		t.Fatal("expected base64 error")
	}
	if _, err := ctyunAESDecryptWithKey(base64.StdEncoding.EncodeToString([]byte("short")), key); err == nil {
		t.Fatal("expected length error for non-block-multiple ciphertext")
	}
}

func padPKCS7(b []byte, size int) []byte {
	pad := size - len(b)%size
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func insertEvery(s string, n int) string {
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && i%n == 0 {
			out = append(out, '\r', '\n')
		}
		out = append(out, c)
	}
	return string(out)
}

func TestCtyunRandAlphaNum(t *testing.T) {
	s := ctyunRandAlphaNum(16)
	if len(s) != 16 {
		t.Fatalf("len = %d, want 16", len(s))
	}
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		default:
			t.Fatalf("non-alphanumeric char %q in %q", c, s)
		}
	}
	// Randomness sanity: two draws differ.
	if s == ctyunRandAlphaNum(16) {
		t.Fatal("two draws identical (extremely unlikely)")
	}
	_ = rand.Reader
}
