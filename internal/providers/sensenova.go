package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"aiquotaglass/internal/config"
)

// SenseNova (商汤日日新) Coding Plan usage. The console authenticates with a
// short-lived (~3h) OAuth2 access_token; the platform rejects the
// refresh_token grant, so the provider fully automates login: with the user's
// account username + password it performs the OAuth2 authorization-code flow
// end-to-end — fetch the SenseCore IdP JWKS, JWE-encrypt (RSA-OAEP+A256GCM)
// the password, POST it to the login API, then walk the login_verifier →
// consent → code redirect chain and exchange the code for an access_token.
// The password is DPAPI-encrypted at rest (it lives in the cookie slot); the
// username is in the workspace slot. Tokens are cached per provider ID and
// re-minted only when within 60s of expiry or the API rejects them.
//
// Each Coding Plan model has an independent 5-hour rolling window; this
// provider reports the most-consumed model (lowest remaining percent) as a
// single 5h window so the snap bar and threshold alerting stay meaningful —
// the card row label names that model.
const (
	sensenovaBase     = "https://platform.sensenova.cn"
	sensenovaAuthURL  = sensenovaBase + "/oauth2/auth"
	sensenovaTokenURL = sensenovaBase + "/oauth2/token"
	sensenovaQuotaURL = sensenovaBase + "/lite/console/v1/user/coding-plan/usages"
	sensenovaRedirect = sensenovaBase // OAuth2 redirect_uri
	sensenovaClientID = "nova"
	sensenovaJWKURL   = "https://signin.sensecore.cn/.well-known/jwks.json"
	sensenovaLoginURL = "https://iam.sensecoreapi.cn/iam/authn/v1/auth/nova/login"
	sensenovaJWKID    = "public:hydra.openid.id-token"
	sensenovaUA       = "AIQuotaGlass/0.1 (+https://github.com/zp96324511/AIQuotaGlass)"
)

type sensenova struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// sensenovaToken is a cached minted access_token with its JWT exp and the
// account_id (tenant_id) extracted from the token.
type sensenovaToken struct {
	accessToken string
	tenantID    string
	expAt       time.Time
}

var (
	sensenovaMu    sync.Mutex
	sensenovaCache = map[string]*sensenovaToken{} // key = provider ID
)

// init registers the SenseNova adapter with the provider registry.
func init() {
	Register(
		"sensenova",
		"SenseNova 日日新",
		"商汤日日新 Coding Plan 用量 (账号密码自动登录)",
		newSenseNova,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://platform.sensenova.cn 注册并完成手机号验证\n" +
				"2. 用户名 = 注册手机号 (或控制台「账号密码登录」用的用户名)\n" +
				"3. 密码 = 控制台登录密码, 填入下方「密码」(本地 DPAPI 加密存储)\n" +
				"4. account_id 与 access_token 由后端自动登录获取并定时刷新,\n" +
				"   无需手动维护; 密码不改则长期免维护\n" +
				"5. 用量 = 各 Coding Plan 模型中消耗最高者的 5 小时窗口",
		},
		ProviderField{Key: "workspace", Label: "用户名", Kind: "text", Required: true, Placeholder: "注册手机号 / 控制台用户名"},
		ProviderField{Key: "cookie", Label: "密码", Kind: "password", Required: true, Placeholder: "控制台登录密码"},
	)
	RegisterWindows("sensenova", "5h")
}

func newSenseNova(cfg config.ProviderConfig) (Provider, error) {
	return &sensenova{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (p *sensenova) ID() string   { return p.cfg.ID }
func (p *sensenova) Name() string { return p.cfg.Name }

func (p *sensenova) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	username := strings.TrimSpace(p.cfg.Workspace)
	password := p.cfg.Cookie
	if username == "" || password == "" {
		res.Error = "未配置用户名或密码"
		return res, fmt.Errorf("sensenova: missing username or password")
	}

	tok, mintErr := p.getAccessToken(ctx, username, password, false)
	if tok == nil {
		res.Error = fmt.Sprintf("登录失败: %v", mintErr)
		return res, mintErr
	}

	body, status, err := p.queryQuota(ctx, tok)
	// A 401 means the cached token expired or was revoked; force a fresh
	// login and retry once before surfacing the error.
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		sensenovaMu.Lock()
		delete(sensenovaCache, p.cfg.ID)
		sensenovaMu.Unlock()
		if tok2, m2 := p.getAccessToken(ctx, username, password, true); tok2 != nil {
			body, status, err = p.queryQuota(ctx, tok2)
		} else if m2 != nil {
			res.Error = fmt.Sprintf("登录失败: %v", m2)
			return res, m2
		}
	}
	if err != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			res.Error = "登录后仍被拒绝, 请检查账号密码"
		} else if status != 0 {
			res.Error = fmt.Sprintf("查询失败: HTTP %d", status)
		} else {
			res.Error = fmt.Sprintf("查询失败: %v", err)
		}
		res.ErrorInfo = httpErrorInfo(http.MethodGet, sensenovaQuotaURL, status, body)
		return res, err
	}

	windows, perr := parseSenseNovaQuota(body)
	if perr != nil {
		res.Error = fmt.Sprintf("解析用量数据失败: %v", perr)
		return res, perr
	}
	res.Windows = windows
	return res, nil
}

// queryQuota calls the coding-plan usages endpoint and returns the body, HTTP
// status, and any transport error. The body is always returned (possibly nil)
// so the caller can build an ErrorInfo on failure.
func (p *sensenova) queryQuota(ctx context.Context, tok *sensenovaToken) ([]byte, int, error) {
	u := sensenovaQuotaURL + "?" + url.Values{"account_id": {tok.tenantID}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", sensenovaUA)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return body, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// getAccessToken returns a usable access_token, minting a new one via a full
// password login when no cached token is fresh enough. When force is true the
// cache is bypassed (used after a 401). On mint failure a stale cached token
// (if any) is returned best-effort so the API call can still try.
func (p *sensenova) getAccessToken(ctx context.Context, username, password string, force bool) (*sensenovaToken, error) {
	sensenovaMu.Lock()
	cached := sensenovaCache[p.cfg.ID]
	sensenovaMu.Unlock()
	if !force && cached != nil && time.Until(cached.expAt) > 60*time.Second {
		return cached, nil
	}
	tok, err := p.mintToken(ctx, username, password)
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return nil, err
	}
	sensenovaMu.Lock()
	sensenovaCache[p.cfg.ID] = tok
	sensenovaMu.Unlock()
	return tok, nil
}

// mintToken performs the full OAuth2 password login flow:
//  1. Start authorize (PKCE) with no session → capture login_challenge.
//  2. Fetch the IdP JWKS and pick the public:hydra.openid.id-token RSA key.
//  3. JWE-encrypt the password (RSA-OAEP + A256GCM).
//  4. POST credentials to the SenseCore login API → get a login_verifier URL.
//  5. GET that URL (cookie jar carries the csrf cookie) → consent → code.
//  6. Exchange the code at /oauth2/token → access_token.
//  7. Decode the JWT → tenant_id (account_id) + exp.
func (p *sensenova) mintToken(ctx context.Context, username, password string) (*sensenovaToken, error) {
	verifier, challenge, err := sensenovaPKCE()
	if err != nil {
		return nil, fmt.Errorf("pkce: %w", err)
	}
	state := sensenovaHex(20)

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}

	// Step 1: start authorize, capture login_challenge from the first redirect
	// (platform → iam). Stop there — the iam GET would just render the SPA.
	var loginChallenge string
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {sensenovaClientID},
		"code_challenge_method": {"S256"},
		"code_challenge":        {challenge},
		"redirect_uri":          {sensenovaRedirect},
		"scope":                 {"openid offline offline_access"},
		"state":                 {state},
	}
	authReq, err := http.NewRequestWithContext(ctx, http.MethodGet, sensenovaAuthURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	authReq.Header.Set("User-Agent", sensenovaUA)

	var authCode, authErr string
	dance := &http.Client{
		Jar:     jar,
		Timeout: 25 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			q := req.URL.Query()
			if lc := q.Get("login_challenge"); lc != "" && loginChallenge == "" {
				loginChallenge = lc
				return http.ErrUseLastResponse
			}
			if c := q.Get("code"); c != "" {
				authCode = c
				return http.ErrUseLastResponse
			}
			if e := q.Get("error"); e != "" {
				authErr = q.Get("error_description")
				if authErr == "" {
					authErr = e
				}
				return http.ErrUseLastResponse
			}
			if len(via) >= 12 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	authResp, err := dance.Do(authReq)
	if authResp != nil {
		authResp.Body.Close()
	}
	if err != nil && loginChallenge == "" && authErr == "" {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	if loginChallenge == "" {
		if authErr == "" {
			authErr = "no login_challenge returned"
		}
		return nil, fmt.Errorf("authorize: %s", authErr)
	}

	// Step 2: fetch the IdP JWKS and pick the encryption RSA public key.
	jwkN, jwkE, err := p.fetchSenseNovaJWK(ctx)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}

	// Step 3: JWE-encrypt the password.
	encPwd, err := sensenovaJWEEncrypt(jwkN, jwkE, []byte(password))
	if err != nil {
		return nil, fmt.Errorf("encrypt password: %w", err)
	}

	// Step 4: POST credentials → login_verifier redirect URL.
	loginBody, _ := json.Marshal(map[string]any{
		"username":   username,
		"password":   encPwd,
		"challenge":  loginChallenge,
		"is_encrypt": true,
	})
	loginReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sensenovaLoginURL, strings.NewReader(string(loginBody)))
	if err != nil {
		return nil, err
	}
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Accept", "application/json")
	loginReq.Header.Set("User-Agent", sensenovaUA)
	loginReq.Header.Set("Origin", sensenovaBase)
	loginReq.Header.Set("Referer", sensenovaBase+"/")
	loginResp, err := p.client.Do(loginReq)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	lbody, _ := io.ReadAll(io.LimitReader(loginResp.Body, 1<<20))
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login: HTTP %d %s", loginResp.StatusCode, truncatedErrorBody(lbody))
	}
	var loginRes struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(lbody, &loginRes); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	if loginRes.Redirect == "" {
		return nil, fmt.Errorf("login: no redirect in response (%s)", truncatedErrorBody(lbody))
	}

	// Step 5: GET the redirect URL (login_verifier) → consent → code.
	redReq, err := http.NewRequestWithContext(ctx, http.MethodGet, loginRes.Redirect, nil)
	if err != nil {
		return nil, err
	}
	redReq.Header.Set("User-Agent", sensenovaUA)
	redResp, err := dance.Do(redReq)
	if redResp != nil {
		redResp.Body.Close()
	}
	if err != nil && authCode == "" && authErr == "" {
		return nil, fmt.Errorf("authorize code: %w", err)
	}
	if authCode == "" {
		if authErr == "" {
			authErr = "no code returned"
		}
		return nil, fmt.Errorf("authorize code: %s", authErr)
	}

	// Step 6: exchange the code for an access_token.
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {sensenovaClientID},
		"code":          {authCode},
		"redirect_uri":  {sensenovaRedirect},
		"code_verifier": {verifier},
		"state":         {state},
	}
	tokReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sensenovaTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokReq.Header.Set("User-Agent", sensenovaUA)
	tokResp, err := p.client.Do(tokReq)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer tokResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(tokResp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tokResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange: HTTP %d %s", tokResp.StatusCode, truncatedErrorBody(body))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if payload.AccessToken == "" {
		msg := payload.Error
		if payload.ErrorDesc != "" {
			msg = msg + ": " + payload.ErrorDesc
		}
		return nil, fmt.Errorf("token exchange: %s", msg)
	}

	// Step 7: decode the JWT → tenant_id + exp.
	tenantID, expAt, err := decodeSenseNovaJWT(payload.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if expAt.IsZero() && payload.ExpiresIn > 0 {
		expAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return &sensenovaToken{accessToken: payload.AccessToken, tenantID: tenantID, expAt: expAt}, nil
}

// fetchSenseNovaJWK fetches the IdP JWKS and returns the RSA public key (n, e,
// base64url) with kid "public:hydra.openid.id-token" — the key the console uses
// to JWE-encrypt the password at login.
func (p *sensenova) fetchSenseNovaJWK(ctx context.Context) (n, e string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sensenovaJWKURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", sensenovaUA)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("jwks HTTP %d", resp.StatusCode)
	}
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		return "", "", fmt.Errorf("parse jwks: %w", err)
	}
	for _, k := range jwks.Keys {
		if k.Kid == sensenovaJWKID && k.Kty == "RSA" {
			return k.N, k.E, nil
		}
	}
	return "", "", fmt.Errorf("no key with kid %q", sensenovaJWKID)
}

// sensenovaJWEEncrypt produces a JWE compact-serialization token (RSA-OAEP +
// A256GCM) of the plaintext, using the RSA public key given by its JWK base64url
// modulus (n) and exponent (e). This mirrors the browser's WebCrypto path: the
// protected header is {"alg":"RSA-OAEP","enc":"A256GCM"} (no kid), the CEK is
// 32 random bytes RSA-OAEP-encrypted (SHA-1, matching JWE's "RSA-OAEP"), and
// the payload is AES-256-GCM sealed with the base64url protected header as AAD.
func sensenovaJWEEncrypt(jwkN, jwkE string, plaintext []byte) (string, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(jwkN)
	if err != nil {
		if nBytes, err = base64.URLEncoding.DecodeString(jwkN); err != nil {
			return "", fmt.Errorf("decode n: %w", err)
		}
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwkE)
	if err != nil {
		if eBytes, err = base64.URLEncoding.DecodeString(jwkE); err != nil {
			return "", fmt.Errorf("decode e: %w", err)
		}
	}
	pub := &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: 0,
	}
	for _, b := range eBytes {
		pub.E = pub.E<<8 | int(b)
	}
	if pub.E == 0 || pub.N.Sign() <= 0 {
		return "", fmt.Errorf("invalid rsa public key")
	}

	protected := []byte(`{"alg":"RSA-OAEP","enc":"A256GCM"}`)
	protectedB64 := base64.RawURLEncoding.EncodeToString(protected)

	cek := make([]byte, 32)
	if _, err := rand.Read(cek); err != nil {
		return "", err
	}
	encKey, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, cek, nil)
	if err != nil {
		return "", fmt.Errorf("rsa-oaep: %w", err)
	}

	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	// JWE AAD is the ASCII bytes of the base64url-encoded protected header.
	// SenseNova's login API uses a non-standard 5-part compact serialization
	// (protected.encrypted_key.iv.ciphertext.tag) where the 16-byte GCM tag is
	// a SEPARATE segment instead of inlined into the ciphertext — so split it
	// out of the aead.Seal output (which appends the tag).
	sealed := aead.Seal(nil, iv, plaintext, []byte(protectedB64))
	tag := sealed[len(sealed)-aead.Overhead():]
	ciphertext := sealed[:len(sealed)-aead.Overhead()]

	return protectedB64 + "." +
		base64.RawURLEncoding.EncodeToString(encKey) + "." +
		base64.RawURLEncoding.EncodeToString(iv) + "." +
		base64.RawURLEncoding.EncodeToString(ciphertext) + "." +
		base64.RawURLEncoding.EncodeToString(tag), nil
}

// sensenovaPKCE generates an S256 PKCE pair: a 50-char alphanumeric verifier
// and its base64url(sha256(verifier)) challenge, matching the nova-auth-sdk.
func sensenovaPKCE() (verifier, challenge string, err error) {
	const alpha = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 50)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	v := make([]byte, 50)
	for i, c := range b {
		v[i] = alpha[int(c)%len(alpha)]
	}
	verifier = string(v)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// sensenovaHex returns n random bytes as a lowercase hex string (OAuth2 state).
func sensenovaHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	const hex = "0123456789abcdef"
	s := make([]byte, n*2)
	for i, c := range b {
		s[i*2] = hex[c>>4]
		s[i*2+1] = hex[c&0x0f]
	}
	return string(s)
}

// decodeSenseNovaJWT decodes the JWT middle segment and returns the
// ext.tenant_id claim (the account_id) and the exp claim. The signature is not
// verified — we only read claims; the server rejects expired/tampered tokens.
func decodeSenseNovaJWT(token string) (tenantID string, expAt time.Time, err error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", time.Time{}, fmt.Errorf("not a valid JWT (expected 3 dot-separated segments)")
	}
	seg := parts[1]
	raw, e := base64.RawURLEncoding.DecodeString(seg)
	if e != nil {
		if raw, e = base64.URLEncoding.DecodeString(seg); e != nil {
			return "", time.Time{}, fmt.Errorf("decode JWT payload: %w", e)
		}
	}
	var payload struct {
		Exp int64 `json:"exp"`
		Ext struct {
			TenantID string `json:"tenant_id"`
		} `json:"ext"`
	}
	if e := json.Unmarshal(raw, &payload); e != nil {
		return "", time.Time{}, fmt.Errorf("parse JWT payload: %w", e)
	}
	if payload.Ext.TenantID == "" {
		return "", time.Time{}, fmt.Errorf("token has no ext.tenant_id claim")
	}
	if payload.Exp > 0 {
		expAt = time.Unix(payload.Exp, 0)
	}
	return payload.Ext.TenantID, expAt, nil
}

// parseSenseNovaQuota extracts the coding-plan remaining percent per model and
// reports the most-consumed model (lowest remaining) as a single 5h window.
// The API returns REMAINING percent, so used percent = 100 - remaining. Ties
// on remaining resolve to the lexicographically smallest model name so the
// output is deterministic across the randomized map iteration order.
func parseSenseNovaQuota(body []byte) ([]WindowStatus, error) {
	var payload struct {
		ModelRemainingPercent map[string]any `json:"model_remaining_percent"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(payload.ModelRemainingPercent) == 0 {
		return nil, fmt.Errorf("no coding plan models")
	}
	type entry struct {
		model     string
		remaining float64
	}
	entries := make([]entry, 0, len(payload.ModelRemainingPercent))
	for model, v := range payload.ModelRemainingPercent {
		remaining, ok := valueAsFloat(v)
		if !ok {
			continue
		}
		entries = append(entries, entry{model, remaining})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no usable remaining percent")
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].remaining != entries[j].remaining {
			return entries[i].remaining < entries[j].remaining
		}
		return entries[i].model < entries[j].model
	})
	worst := entries[0]
	used := 100 - worst.remaining
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	return []WindowStatus{{
		Key:        "5h",
		Label:      worst.model,
		Percent:    used,
		Status:     "ok",
		ResetInSec: -1,
	}}, nil
}
