package providers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
// short-lived (~3h) OAuth2 access_token; the active SPA does NOT use the
// refresh_token grant (the platform rejects it as invalid_grant), so silent
// refresh is done by replaying the PKCE authorization-code dance with the
// long-lived (~7d) oauth2_authentication_session cookie. The user pastes only
// that session cookie; this provider mints (and caches) fresh access_tokens on
// demand, re-minting when a token is within 60s of expiry or the API rejects it.
//
// Each Coding Plan model has an independent 5-hour rolling window; this
// provider reports the most-consumed model (lowest remaining percent) as a
// single 5h window so the snap bar and threshold alerting stay meaningful — the
// card row label names that model.
const (
	sensenovaBase     = "https://platform.sensenova.cn"
	sensenovaAuthURL  = sensenovaBase + "/oauth2/auth"
	sensenovaTokenURL = sensenovaBase + "/oauth2/token"
	sensenovaQuotaURL = sensenovaBase + "/lite/console/v1/user/coding-plan/usages"
	sensenovaRedirect = sensenovaBase // OAuth2 redirect_uri
	sensenovaClientID = "nova"
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
		"商汤日日新 Coding Plan 用量 (Session Cookie 自动刷新)",
		newSenseNova,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://platform.sensenova.cn 进入控制台\n" +
				"2. 按 F12 打开 DevTools → Application → Cookies →\n" +
				"   https://platform.sensenova.cn → 复制\n" +
				"   oauth2_authentication_session 这一行的 Value\n" +
				"   (或在 Network 面板任一请求的 Request Headers\n" +
				"   里 Cookie 中找 oauth2_authentication_session= 后面那一段)\n" +
				"3. 粘贴到上方「Session Cookie」(account_id 与 access_token\n" +
				"   会自动从登录会话解析并定时刷新, 无需手动维护)\n" +
				"4. Session Cookie 有效期约 7 天, 过期后卡片状态点变红,\n" +
				"   重新登录控制台并重新复制粘贴即可\n" +
				"5. 用量 = 各 Coding Plan 模型中消耗最高者的 5 小时窗口",
		},
		ProviderField{Key: "cookie", Label: "Session Cookie", Kind: "password", Required: true, Placeholder: "oauth2_authentication_session 的值 (MTc4... 开头)"},
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

	session := normalizeSenseNovaSession(p.cfg.Cookie)
	if session == "" {
		res.Error = "未配置 Session Cookie"
		return res, fmt.Errorf("sensenova: empty session cookie")
	}

	tok, mintErr := p.getAccessToken(ctx, session, false)
	if tok == nil {
		res.Error = fmt.Sprintf("获取 Access Token 失败: %v", mintErr)
		return res, mintErr
	}

	body, status, err := p.queryQuota(ctx, tok)
	// A 401 means the cached token expired or the session was revoked; force a
	// fresh mint and retry once before surfacing the error.
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		sensenovaMu.Lock()
		delete(sensenovaCache, p.cfg.ID)
		sensenovaMu.Unlock()
		if tok2, m2 := p.getAccessToken(ctx, session, true); tok2 != nil {
			body, status, err = p.queryQuota(ctx, tok2)
		} else if m2 != nil {
			res.Error = fmt.Sprintf("获取 Access Token 失败: %v", m2)
			return res, m2
		}
	}
	if err != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			res.Error = "Session Cookie 无效或已过期"
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

// getAccessToken returns a usable access_token, minting a new one via the
// silent SSO PKCE dance when no cached token is fresh enough. When force is
// true the cache is bypassed (used after a 401). On mint failure a stale
// cached token (if any) is returned best-effort so the API call can still try.
func (p *sensenova) getAccessToken(ctx context.Context, session string, force bool) (*sensenovaToken, error) {
	sensenovaMu.Lock()
	cached := sensenovaCache[p.cfg.ID]
	sensenovaMu.Unlock()
	if !force && cached != nil && time.Until(cached.expAt) > 60*time.Second {
		return cached, nil
	}
	tok, err := p.mintToken(ctx, session)
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

// mintToken replays the OAuth2 authorization-code flow silently: with only the
// session cookie it walks the authorize redirect chain to obtain a one-time
// code, then exchanges code+code_verifier for an access_token at the token
// endpoint. The dance mirrors the browser SPA's PKCE flow (client_id "nova",
// S256 challenge, scope "openid offline offline_access").
func (p *sensenova) mintToken(ctx context.Context, session string) (*sensenovaToken, error) {
	verifier, challenge, err := sensenovaPKCE()
	if err != nil {
		return nil, fmt.Errorf("pkce: %w", err)
	}
	state := sensenovaHex(20)

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}
	baseURL, _ := url.Parse(sensenovaBase)
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "oauth2_authentication_session", Value: session, Path: "/", Secure: true, HttpOnly: true},
	})

	var (
		authCode string
		authErr  string
	)
	dance := &http.Client{
		Jar:     jar,
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			q := req.URL.Query()
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
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	params := url.Values{
		"response_type":           {"code"},
		"client_id":               {sensenovaClientID},
		"code_challenge_method":   {"S256"},
		"code_challenge":          {challenge},
		"redirect_uri":           {sensenovaRedirect},
		"scope":                   {"openid offline offline_access"},
		"state":                  {state},
	}
	authReq, err := http.NewRequestWithContext(ctx, http.MethodGet, sensenovaAuthURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	authReq.Header.Set("User-Agent", sensenovaUA)
	authResp, err := dance.Do(authReq)
	if authResp != nil {
		authResp.Body.Close()
	}
	if err != nil && authCode == "" && authErr == "" {
		return nil, fmt.Errorf("authorize: %w", err)
	}
	if authCode == "" {
		if authErr == "" {
			authErr = "no code returned (Session Cookie 可能已过期)"
		}
		return nil, fmt.Errorf("authorize: %s", authErr)
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {sensenovaClientID},
		"code":          {authCode},
		"redirect_uri": {sensenovaRedirect},
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
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
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
	tenantID, expAt, err := decodeSenseNovaJWT(payload.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if expAt.IsZero() && payload.ExpiresIn > 0 {
		expAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return &sensenovaToken{accessToken: payload.AccessToken, tenantID: tenantID, expAt: expAt}, nil
}

// normalizeSenseNovaSession strips a leading "oauth2_authentication_session="
// prefix (users often copy the whole cookie pair from the Cookie header) and
// surrounding whitespace.
func normalizeSenseNovaSession(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "oauth2_authentication_session=")
	return strings.TrimSpace(raw)
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
