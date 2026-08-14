package providers

import (
	"encoding/base64"
	"encoding/json"
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
	if len(windows) != 4 {
		t.Fatalf("want 4 windows (one per model), got %d", len(windows))
	}
	// Ordered by used percent desc → glm-5.2 (3.33%) first, then the 0% models
	// tie-broken by lexicographic name (deepseek-v4-flash, sen...6.8, sen...u1).
	if windows[0].Key != "glm-5.2" || windows[0].Label != "glm-5.2" {
		t.Fatalf("first window = %+v, want glm-5.2 (most consumed)", windows[0])
	}
	if !approx(windows[0].Percent, 3.33, 1e-2) {
		t.Fatalf("glm-5.2 percent = %v, want ~3.33", windows[0].Percent)
	}
	if windows[0].ResetInSec != -1 {
		t.Fatalf("resetInSec = %d, want -1 (no reset time)", windows[0].ResetInSec)
	}
	if windows[1].Key != "deepseek-v4-flash" {
		t.Fatalf("second window = %+v, want deepseek-v4-flash (0%%, smallest name)", windows[1])
	}
	for _, w := range windows[1:] {
		if w.Percent != 0 {
			t.Fatalf("%s percent = %v, want 0", w.Label, w.Percent)
		}
	}
}

func TestParseSenseNovaQuotaAllFull(t *testing.T) {
	// Every model at 100% remaining → all 0% used; ordered by used desc (all
	// 0) then lexicographic model name, so deepseek-v4-flash is first.
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
	if len(windows) != 4 {
		t.Fatalf("want 4 windows, got %d", len(windows))
	}
	for _, w := range windows {
		if w.Percent != 0 {
			t.Fatalf("%s percent = %v, want 0", w.Label, w.Percent)
		}
	}
	if windows[0].Key != "deepseek-v4-flash" {
		t.Fatalf("first = %q, want deepseek-v4-flash (smallest name on tie)", windows[0].Key)
	}
}

func TestParseSenseNovaQuotaStringValues(t *testing.T) {
	// Remaining percents may arrive as numeric strings; ordered by used desc.
	body := []byte(`{"model_remaining_percent": {"glm-5.2": "80", "deepseek-v4-flash": "100"}}`)
	windows, err := parseSenseNovaQuota(body)
	if err != nil {
		t.Fatalf("parseSenseNovaQuota: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("want 2 windows, got %d", len(windows))
	}
	if windows[0].Key != "glm-5.2" || !approx(windows[0].Percent, 20, 1e-9) {
		t.Fatalf("first window = %+v, want glm-5.2 @ 20%%", windows[0])
	}
	if windows[1].Key != "deepseek-v4-flash" || windows[1].Percent != 0 {
		t.Fatalf("second window = %+v, want deepseek-v4-flash @ 0%%", windows[1])
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

// TestSenseNovaJWEStructure verifies the JWE token shape the SenseNova login
// API expects: four base64url parts, protected header {"alg":"RSA-OAEP",
// "enc":"A256GCM"}, no padding anywhere. It uses the real IdP public key shape
// (a fresh 2048-bit RSA key) so RSA-OAEP + AES-GCM run for real.
func TestSenseNovaJWEStructure(t *testing.T) {
	// Generate an RSA key pair, export the public part as JWK (n, e).
	priv, err := rsaGenerateKey()
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	nB64 := base64.RawURLEncoding.EncodeToString(priv.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(bigIntBytes(priv.E))

	token, err := sensenovaJWEEncrypt(nB64, eB64, []byte("hunter2"))
	if err != nil {
		t.Fatalf("sensenovaJWEEncrypt: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 5 {
		t.Fatalf("want 5 JWE parts (protected.encrypted_key.iv.ciphertext.tag), got %d", len(parts))
	}
	for i, p := range parts {
		if strings.ContainsAny(p, "+/=") {
			t.Fatalf("part %d not base64url-no-pad: %q", i, p)
		}
	}
	hdr, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode protected header: %v", err)
	}
	var h struct {
		Alg string `json:"alg"`
		Enc string `json:"enc"`
	}
	if err := json.Unmarshal(hdr, &h); err != nil {
		t.Fatalf("parse protected header: %v", err)
	}
	if h.Alg != "RSA-OAEP" || h.Enc != "A256GCM" {
		t.Fatalf("header = %+v, want RSA-OAEP/A256GCM", h)
	}
	// IV part is 12 bytes once decoded.
	iv, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode iv: %v", err)
	}
	if len(iv) != 12 {
		t.Fatalf("iv length = %d, want 12", len(iv))
	}
	// Ciphertext (part 4) equals plaintext length (no tag inlined); the 16-byte
	// GCM tag is a separate 5th part.
	ct, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	if len(ct) != len("hunter2") {
		t.Fatalf("ciphertext length = %d, want %d (plaintext length)", len(ct), len("hunter2"))
	}
	tag, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decode tag: %v", err)
	}
	if len(tag) != 16 {
		t.Fatalf("tag length = %d, want 16", len(tag))
	}
	// Two encryptions of the same plaintext must differ (random CEK + IV).
	tok2, _ := sensenovaJWEEncrypt(nB64, eB64, []byte("hunter2"))
	if token == tok2 {
		t.Fatal("JWE not randomized")
	}
}

// TestSenseNovaJWERoundTrip decrypts a token we produced, proving the CEK
// wrapping (RSA-OAEP/SHA-1) and A256GCM AEAD (AAD = base64url protected header)
// match the JWE spec the server expects.
func TestSenseNovaJWERoundTrip(t *testing.T) {
	priv, err := rsaGenerateKey()
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	nB64 := base64.RawURLEncoding.EncodeToString(priv.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(bigIntBytes(priv.E))

	secret := "ObioxWyflB7U$kO"
	token, err := sensenovaJWEEncrypt(nB64, eB64, []byte(secret))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	pt, err := sensenovaJWEDecrypt(priv, token)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(pt) != secret {
		t.Fatalf("round-trip = %q, want %q", pt, secret)
	}
}
