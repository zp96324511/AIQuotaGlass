package providers

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestParseSenseNovaQuota(t *testing.T) {
	body := []byte(`{
		"model_remaining_percent": {
			"deepseek-v4-flash": 100,
			"glm-5.2": 96.67,
			"sensenova-6.8-flash-lite": 100,
			"sensenova-u1-fast": 100
		}
	}`)
	windows, err := parseSenseNovaQuota(body)
	if err != nil {
		t.Fatalf("parseSenseNovaQuota: %v", err)
	}
	if len(windows) != 1 {
		t.Fatalf("want 1 window, got %d", len(windows))
	}
	w := windows[0]
	if w.Key != "5h" {
		t.Fatalf("key = %q, want 5h", w.Key)
	}
	if w.Label != "glm-5.2" {
		t.Fatalf("label = %q, want glm-5.2 (lowest remaining)", w.Label)
	}
	if !approx(w.Percent, 3.33, 1e-2) {
		t.Fatalf("percent = %v, want ~3.33 (100-96.67)", w.Percent)
	}
	if w.ResetInSec != -1 {
		t.Fatalf("resetInSec = %d, want -1 (no reset time)", w.ResetInSec)
	}
}

func TestParseSenseNovaQuotaAllFull(t *testing.T) {
	// Every model at 100% remaining → 0% used; the label is the
	// lexicographically smallest model (deterministic across map order).
	body := []byte(`{
		"model_remaining_percent": {
			"sensenova-u1-fast": 100,
			"deepseek-v4-flash": 100,
			"glm-5.2": 100,
			"sensenova-6.8-flash-lite": 100
		}
	}`)
	windows, err := parseSenseNovaQuota(body)
	if err != nil {
		t.Fatalf("parseSenseNovaQuota: %v", err)
	}
	if len(windows) != 1 || windows[0].Percent != 0 {
		t.Fatalf("want single 0%% window, got %+v", windows)
	}
	if windows[0].Label != "deepseek-v4-flash" {
		t.Fatalf("label = %q, want deepseek-v4-flash (smallest name on tie)", windows[0].Label)
	}
}

func TestParseSenseNovaQuotaStringValues(t *testing.T) {
	// Remaining percents may arrive as numeric strings.
	body := []byte(`{"model_remaining_percent": {"glm-5.2": "80", "deepseek-v4-flash": "100"}}`)
	windows, err := parseSenseNovaQuota(body)
	if err != nil {
		t.Fatalf("parseSenseNovaQuota: %v", err)
	}
	if windows[0].Label != "glm-5.2" || !approx(windows[0].Percent, 20, 1e-9) {
		t.Fatalf("wrong window: %+v", windows[0])
	}
}

func TestParseSenseNovaQuotaErrors(t *testing.T) {
	if _, err := parseSenseNovaQuota([]byte(`not json`)); err == nil {
		t.Fatal("want error for malformed JSON")
	}
	if _, err := parseSenseNovaQuota([]byte(`{"model_remaining_percent": {}}`)); err == nil {
		t.Fatal("want error when no models")
	}
	if _, err := parseSenseNovaQuota([]byte(`{"model_remaining_percent": {"m": "not-a-number"}}`)); err == nil {
		t.Fatal("want error when no usable remaining percent")
	}
}

func TestDecodeSenseNovaJWT(t *testing.T) {
	payload := `{"exp":1786644684,"ext":{"tenant_id":"019ffb97-e78a-7551-8c7d-446b53db3c9b"},"sub":"u"}`
	seg := base64.RawURLEncoding.EncodeToString([]byte(payload))
	token := "header." + seg + ".signature"

	tenantID, expAt, err := decodeSenseNovaJWT(token)
	if err != nil {
		t.Fatalf("decodeSenseNovaJWT: %v", err)
	}
	if tenantID != "019ffb97-e78a-7551-8c7d-446b53db3c9b" {
		t.Fatalf("tenantID = %q", tenantID)
	}
	if want := time.Unix(1786644684, 0); !expAt.Equal(want) {
		t.Fatalf("expAt = %v, want %v", expAt, want)
	}
}

func TestDecodeSenseNovaJWTErrors(t *testing.T) {
	if _, _, err := decodeSenseNovaJWT("not-a-jwt"); err == nil {
		t.Fatal("want error for non-JWT input")
	}
	payload := `{"sub":"u"}`
	seg := base64.RawURLEncoding.EncodeToString([]byte(payload))
	if _, _, err := decodeSenseNovaJWT("h." + seg + ".s"); err == nil {
		t.Fatal("want error when tenant_id claim missing")
	}
	if _, _, err := decodeSenseNovaJWT("h.@@@.s"); err == nil {
		t.Fatal("want error for undecodable payload")
	}
}

func TestNormalizeSenseNovaSession(t *testing.T) {
	const want = "MTc4NjYzMzg4M3xEabc=="
	cases := []string{
		want,
		"  " + want + "  ",
		"oauth2_authentication_session=" + want,
		" oauth2_authentication_session=" + want + " ",
	}
	for _, c := range cases {
		if got := normalizeSenseNovaSession(c); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", c, got, want)
		}
	}
	if got := normalizeSenseNovaSession(""); got != "" {
		t.Fatalf("normalize empty = %q", got)
	}
}

func TestSenseNovaPKCE(t *testing.T) {
	v, c, err := sensenovaPKCE()
	if err != nil {
		t.Fatalf("sensenovaPKCE: %v", err)
	}
	if len(v) != 50 {
		t.Fatalf("verifier length = %d, want 50", len(v))
	}
	// Challenge is base64url(sha256) = 43 chars, no padding, no + or /.
	if len(c) != 43 || strings.ContainsAny(c, "+/=") {
		t.Fatalf("challenge invalid: %q", c)
	}
	for _, r := range v {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
			t.Fatalf("verifier has non-alphanumeric char %q", r)
		}
	}
	// Different calls must yield different verifiers (random).
	v2, _, _ := sensenovaPKCE()
	if v == v2 {
		t.Fatal("verifier not random")
	}
}
