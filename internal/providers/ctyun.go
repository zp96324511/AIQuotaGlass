package providers

import (
	"context"
	"crypto/aes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"aiquotaglass/internal/config"
)

// 天翼云 Coding Plan (云智助手 eaichat.ctyun.cn) usage provider.
//
// The web console sits behind a homegrown gateway auth: every API call needs a
// Web-Signature header = SHA256("[sorted k=v params][&MD5(body)]&sk&ts&random"),
// where sk (sessionKey) is minted by the SSO login dance and never expires on
// its own — but the YL-Token cookie it pairs with lives 7 days. The provider
// fully automates login with the user's account + password:
//
//  1. POST /iam/login on the IAM portal (desk.ctyun.cn) with SHA256(password)
//     and a persistent device code → token_iam cookie. No captcha in practice.
//  2. GET /cas/login?service=… → 302 with a one-time ST ticket.
//  3. GET eaiSysInfo (gwyilian.ctyun.cn) → AES-128-ECB blob (fixed key
//     "chinatelecom@cnn") decrypting to the gateway config incl. the RSA
//     public key (ssopk) + its id (ssopkid).
//  4. POST /sso/login/v2/iam/ticketAuthorize with the ticket and clientKey =
//     RSA-PKCS1v15(random 16 chars, ssopk) hex → YL-Token/YL-Ssid cookies and
//     a sessionKey that is AES-128-ECB ENCRYPTED with the clientKey plaintext
//     — decrypt it to get the real sk.
//
// Usage windows come from two signed endpoints: codingplan/usage/summry lists
// the purchased plans (id, name, expiry) and codingplan/usage/detail?id=…
// reports the three windows 近5小时/本周/套餐总量, each with usage as a 0..1
// ratio and a human-readable Chinese countdown ("3天14时22分后刷新限额") that
// is parsed into ResetInSec. The password is DPAPI-encrypted at rest (cookie
// slot); the account goes in the workspace slot. Sessions (sk + cookies) are
// cached per provider ID and re-minted on 401.
const (
	ctyunIAMLoginURL   = "https://desk.ctyun.cn/cloudB/dy/iam/api/auth/iam/login"
	ctyunCASLoginURL   = "https://desk.ctyun.cn/cloudB/dy/iam/api/auth/iam/cas/login"
	ctyunSysInfoURL    = "https://gwyilian.ctyun.cn/server/eaiSysInfo"
	ctyunAuthorizeURL  = "https://eaichat.ctyun.cn/sso/login/v2/iam/ticketAuthorize"
	ctyunSummaryURL    = "https://eaichat.ctyun.cn/ai/portal/wenc/v1/platform/cp/codingplan/usage/summry"
	ctyunDetailURL     = "https://eaichat.ctyun.cn/ai/portal/wenc/v1/platform/cp/codingplan/usage/detail"
	ctyunService       = "https://eaichat.ctyun.cn:443/chat/#/aichat"
	ctyunAESKey        = "chinatelecom@cnn" // eaiSysInfo + sessionKey AES-128-ECB key
	ctyunUA            = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"
	ctyunClientID      = "eaiapp"
	ctyunSessionMargin = 30 * time.Minute // re-login before cookie expiry
)

type ctyun struct {
	cfg config.ProviderConfig
}

func init() {
	Register(
		"ctyun",
		"天翼云 Coding Plan",
		"天翼云智助手 Coding Plan 用量 (账号密码自动登录)",
		newCtyun,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://eaichat.ctyun.cn 并订购 Coding Plan 套餐\n" +
				"2. 用户名 = 天翼云账号 (注册手机号)\n" +
				"3. 密码 = 账号登录密码, 填入下方「密码」(本地 DPAPI 加密存储)\n" +
				"4. 登录会话由后端自动维护 (约 7 天有效期, 到期自动重登),\n" +
				"   密码不改则长期免维护\n" +
				"5. 用量 = 近5小时 / 本周 / 套餐总量 三窗口限额占比",
		},
		ProviderField{Key: "workspace", Label: "用户名", Kind: "text", Required: true, Placeholder: "天翼云账号 (手机号)"},
		ProviderField{Key: "cookie", Label: "密码", Kind: "password", Required: true, Placeholder: "账号登录密码"},
	)
	RegisterWindows("ctyun", "5h", "weekly", "monthly")
}

func newCtyun(cfg config.ProviderConfig) (Provider, error) {
	return &ctyun{cfg: cfg}, nil
}

func (p *ctyun) ID() string   { return p.cfg.ID }
func (p *ctyun) Name() string { return p.cfg.Name }

// ctyunSession is a cached login: the signing key (sk) plus the cookie header
// value and its expiry. sk itself has no known TTL; the YL-Token cookie does.
type ctyunSession struct {
	sk         string
	cookie     string    // prebuilt Cookie header value
	expiresAt  time.Time // YL-Token cookie expiry
	planID     string    // first purchased Coding Plan id (detail query param)
	planName   string
	planExpiry string
}

// ctyunPlanLimits maps a plan tier to its reference request limits per window
// (5h / weekly / monthly). The API reports usage as a 0..1 ratio only, so the
// bars show "已用/参考上限" against these published caps. Unknown tiers fall
// back to the Lite caps (the only tier with a known mapping).
type ctyunPlanLimits struct {
	fiveHourly, weekly, monthly float64
}

func ctyunLimitsForPlan(name string) ctyunPlanLimits {
	if strings.Contains(name, "Pro") {
		return ctyunPlanLimits{fiveHourly: 6000, weekly: 45000, monthly: 90000}
	}
	return ctyunPlanLimits{fiveHourly: 1200, weekly: 9000, monthly: 18000}
}

var (
	ctyunMu    sync.Mutex
	ctyunCache = map[string]*ctyunSession{} // key = provider ID
)

func (p *ctyun) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	account := strings.TrimSpace(p.cfg.Workspace)
	password := p.cfg.Cookie
	if account == "" || password == "" {
		res.Error = "未配置用户名或密码"
		return res, fmt.Errorf("ctyun: missing account or password")
	}

	sess, mintErr := p.getSession(ctx, account, password, false)
	if sess == nil {
		res.Error = fmt.Sprintf("登录失败: %v", mintErr)
		return res, mintErr
	}

	body, status, err := p.fetchUsage(ctx, sess)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		ctyunMu.Lock()
		delete(ctyunCache, p.cfg.ID)
		ctyunMu.Unlock()
		if s2, m2 := p.getSession(ctx, account, password, true); s2 != nil {
			body, status, err = p.fetchUsage(ctx, s2)
		} else if m2 != nil {
			res.Error = fmt.Sprintf("登录失败: %v", m2)
			return res, m2
		}
	}
	if err != nil || status != http.StatusOK {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			res.Error = "登录后仍被拒绝, 请检查账号密码"
		} else if status != 0 {
			res.Error = fmt.Sprintf("查询失败: HTTP %d", status)
		} else {
			res.Error = fmt.Sprintf("查询失败: %v", err)
		}
		res.ErrorInfo = httpErrorInfo(http.MethodGet, ctyunDetailURL, status, body)
		return res, err
	}

	windows, detail, perr := parseCtyunUsage(body, sess.planName, time.Now())
	if perr != nil {
		res.Error = fmt.Sprintf("解析用量数据失败: %v", perr)
		return res, perr
	}
	if sess.planName != "" {
		detail.GroupName = sess.planName
	}
	if sess.planExpiry != "" {
		detail.ExpiresAt = sess.planExpiry
	}
	res.Windows = windows
	res.Detail = detail
	return res, nil
}

// getSession returns a cached session, minting a fresh one when absent or
// within the expiry margin. force bypasses the cache (after a 401).
func (p *ctyun) getSession(ctx context.Context, account, password string, force bool) (*ctyunSession, error) {
	ctyunMu.Lock()
	cached := ctyunCache[p.cfg.ID]
	ctyunMu.Unlock()
	if !force && cached != nil && time.Until(cached.expiresAt) > ctyunSessionMargin {
		return cached, nil
	}
	sess, err := p.mintSession(ctx, account, password)
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return nil, err
	}
	ctyunMu.Lock()
	ctyunCache[p.cfg.ID] = sess
	ctyunMu.Unlock()
	return sess, nil
}

// ctyunClient builds the login client: a cookie jar plus manual redirects so
// the CAS 302 Location (carrying the ticket) can be captured.
func ctyunClient() (*http.Client, *cookiejar.Jar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, nil, err
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, jar, nil
}

func ctyunDo(ctx context.Context, client *http.Client, method, u string, body io.Reader, hdr map[string]string) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("User-Agent", ctyunUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return resp.StatusCode, b, resp.Header, err
}

// mintSession performs the full login dance and returns the signing session.
func (p *ctyun) mintSession(ctx context.Context, account, password string) (*ctyunSession, error) {
	client, jar, err := ctyunClient()
	if err != nil {
		return nil, err
	}

	// Step 1: IAM password login → token_iam cookie.
	pwdHash := sha256.Sum256([]byte(password))
	deviceCode := "iam:" + ctyunRandHex(16)
	loginBody, _ := json.Marshal(map[string]string{
		"userAccount": account,
		"password":    hex.EncodeToString(pwdHash[:]),
		"deviceCode":  deviceCode,
		"deviceName":  "iam:web",
	})
	code, body, _, err := ctyunDo(ctx, client, http.MethodPost, ctyunIAMLoginURL,
		strings.NewReader(string(loginBody)), map[string]string{
			"Content-Type": "application/json",
			"Origin":       "https://desk.ctyun.cn",
			"Referer":      "https://desk.ctyun.cn/cloudB/dy/iam/",
		})
	if err != nil {
		return nil, fmt.Errorf("iam login: %w", err)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("iam login: HTTP %d %s", code, truncatedErrorBody(body))
	}
	var loginRes struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &loginRes); err != nil {
		return nil, fmt.Errorf("iam login: %w", err)
	}
	if loginRes.Code != 0 {
		msg := loginRes.Msg
		if msg == "" {
			msg = fmt.Sprintf("code %d", loginRes.Code)
		}
		return nil, fmt.Errorf("iam login: %s", msg)
	}

	// Step 2: CAS login → ST ticket in the 302 Location.
	casURL := ctyunCASLoginURL + "?service=" + url.QueryEscape(ctyunService) + "&consent=false"
	code, _, hdr, err := ctyunDo(ctx, client, http.MethodGet, casURL, nil, map[string]string{
		"Referer": "https://desk.ctyun.cn/cloudB/dy/iam/",
	})
	if err != nil {
		return nil, fmt.Errorf("cas login: %w", err)
	}
	if code != http.StatusFound {
		return nil, fmt.Errorf("cas login: HTTP %d", code)
	}
	ticket := strings.TrimPrefix(hdr.Get("Location"), ctyunService+"?ticket=")
	if !strings.HasPrefix(ticket, "ST-") {
		return nil, fmt.Errorf("cas login: no ticket in redirect")
	}

	// Step 3: eaiSysInfo → gateway config (RSA public key + id).
	code, body, _, err = ctyunDo(ctx, client, http.MethodGet, ctyunSysInfoURL, nil, map[string]string{
		"Origin":  "https://eaichat.ctyun.cn",
		"Referer": "https://eaichat.ctyun.cn/",
	})
	if err != nil {
		return nil, fmt.Errorf("eaiSysInfo: %w", err)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("eaiSysInfo: HTTP %d", code)
	}
	var sysRes struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &sysRes); err != nil {
		return nil, fmt.Errorf("eaiSysInfo: %w", err)
	}
	cfgPlain, err := ctyunAESDecryptBase64(sysRes.Data)
	if err != nil {
		return nil, fmt.Errorf("eaiSysInfo decrypt: %w", err)
	}
	var gwCfg struct {
		SSO struct {
			SSOPK   string `json:"ssopk"`
			SSOPKID string `json:"ssopkid"`
		} `json:"sso"`
	}
	if err := json.Unmarshal(cfgPlain, &gwCfg); err != nil {
		return nil, fmt.Errorf("eaiSysInfo parse: %w", err)
	}
	if gwCfg.SSO.SSOPK == "" {
		return nil, fmt.Errorf("eaiSysInfo: no sso public key")
	}

	// Step 4: clientKey = RSA-PKCS1v15(random 16 chars) hex.
	pubDER, err := base64.StdEncoding.DecodeString(gwCfg.SSO.SSOPK)
	if err != nil {
		return nil, fmt.Errorf("ssopk decode: %w", err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		return nil, fmt.Errorf("ssopk parse: %w", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("ssopk: not an RSA key")
	}
	clientKeyRaw := ctyunRandAlphaNum(16)
	encKey, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(clientKeyRaw))
	if err != nil {
		return nil, fmt.Errorf("clientKey encrypt: %w", err)
	}

	// Step 5: ticketAuthorize → YL-Token cookies + encrypted sessionKey.
	form := url.Values{}
	form.Set("loginType", "iamTicket")
	form.Set("clientId", ctyunClientID)
	form.Set("iamTicket", ticket)
	form.Set("redirectUri", ctyunService)
	form.Set("clientKey", hex.EncodeToString(encKey))
	form.Set("clientKeyId", gwCfg.SSO.SSOPKID)
	code, body, _, err = ctyunDo(ctx, client, http.MethodPost, ctyunAuthorizeURL,
		strings.NewReader(form.Encode()), map[string]string{
			"Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
			"Origin":       "https://eaichat.ctyun.cn",
			"Referer":      "https://eaichat.ctyun.cn/chat/",
		})
	if err != nil {
		return nil, fmt.Errorf("ticketAuthorize: %w", err)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("ticketAuthorize: HTTP %d %s", code, truncatedErrorBody(body))
	}
	var authRes struct {
		ResultCode int    `json:"resultCode"`
		ResultMsg  string `json:"resultMsg"`
		Data       struct {
			SessionKey string `json:"sessionKey"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &authRes); err != nil {
		return nil, fmt.Errorf("ticketAuthorize: %w", err)
	}
	if authRes.ResultCode != 0 {
		return nil, fmt.Errorf("ticketAuthorize: %s", authRes.ResultMsg)
	}
	skPlain, err := ctyunAESDecryptWithKey(authRes.Data.SessionKey, []byte(clientKeyRaw))
	if err != nil {
		return nil, fmt.Errorf("sessionKey decrypt: %w", err)
	}

	// Build the Cookie header from the jar and read YL-Token's expiry.
	eaichatURL, _ := url.Parse("https://eaichat.ctyun.cn/")
	var cookieParts []string
	expiresAt := time.Now().Add(24 * time.Hour) // fallback if cookie has no expiry
	for _, c := range jar.Cookies(eaichatURL) {
		cookieParts = append(cookieParts, c.Name+"="+c.Value)
		if c.Name == "YL-Token" && !c.Expires.IsZero() {
			expiresAt = c.Expires
		}
	}
	if len(cookieParts) == 0 {
		return nil, fmt.Errorf("ticketAuthorize: no cookies set")
	}
	sess := &ctyunSession{
		sk:        string(skPlain),
		cookie:    strings.Join(cookieParts, "; "),
		expiresAt: expiresAt,
	}

	// Step 6: plan summary → first paid plan id/name/expiry for the detail call.
	sumBody, status, err := p.signedGet(ctx, sess, ctyunSummaryURL, nil)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("usage summry: HTTP %d %v", status, err)
	}
	var sumRes struct {
		ResultCode int `json:"resultCode"`
		Data       struct {
			Paid []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				State   string `json:"state"`
				ExpTime string `json:"expTime"`
			} `json:"paid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(sumBody, &sumRes); err != nil {
		return nil, fmt.Errorf("usage summry parse: %w", err)
	}
	for _, plan := range sumRes.Data.Paid {
		if plan.ID != "" {
			sess.planID = plan.ID
			sess.planName = plan.Name
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", plan.ExpTime, time.Local); err == nil {
				sess.planExpiry = t.Format("2006-01-02")
			}
			break
		}
	}
	if sess.planID == "" {
		return nil, fmt.Errorf("未找到已订购的 Coding Plan 套餐")
	}
	return sess, nil
}

// signedGet performs a gateway-signed GET. Signature payload:
// "k1=v1&k2=v2&sk&timestamp&random" (params sorted by key, no body for GET).
func (p *ctyun) signedGet(ctx context.Context, sess *ctyunSession, u string, params map[string]string) ([]byte, int, error) {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ps []string
	for _, k := range keys {
		ps = append(ps, k+"="+params[k])
	}
	paramStr := strings.Join(ps, "&")

	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	random := ctyunRandAlphaNum(8)
	payload := paramStr
	if payload != "" {
		payload += "&"
	}
	payload += sess.sk + "&" + ts + "&" + random
	sum := sha256.Sum256([]byte(payload))

	fullURL := u
	if paramStr != "" {
		fullURL += "?" + paramStr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", ctyunUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://eaichat.ctyun.cn/chat/")
	req.Header.Set("Cookie", sess.cookie)
	req.Header.Set("Web-Signature", hex.EncodeToString(sum[:]))
	req.Header.Set("Web-Random", random)
	req.Header.Set("Web-Timestamp", ts)
	req.Header.Set("YL-Main-Version", "202060404")
	req.Header.Set("YL-Product-Id", "5")
	req.Header.Set("x-eai-env", "pubWeb")
	req.Header.Set("x-eai-tenant-id", "14")
	req.Header.Set("x-eai-mode", "eai")
	req.Header.Set("x-eai-version", "202060404")
	req.Header.Set("x-eai-source", "web-eai")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
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

// fetchUsage queries the plan detail (three windows) with the cached session.
func (p *ctyun) fetchUsage(ctx context.Context, sess *ctyunSession) ([]byte, int, error) {
	return p.signedGet(ctx, sess, ctyunDetailURL, map[string]string{"id": sess.planID})
}

// ctyunAESDecryptBase64 decodes and AES-128-ECB decrypts a base64 blob that
// may contain line breaks (the eaiSysInfo response wraps at 76 chars) using
// the fixed gateway key.
func ctyunAESDecryptBase64(b64 string) ([]byte, error) {
	clean := strings.NewReplacer("\r", "", "\n", "", " ", "").Replace(b64)
	return ctyunAESDecryptWithKey(clean, []byte(ctyunAESKey))
}

// ctyunAESDecryptWithKey AES-128-ECB decrypts (PKCS7-unpadding) a base64
// ciphertext with the given 16-byte key.
func ctyunAESDecryptWithKey(b64 string, key []byte) ([]byte, error) {
	ct, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\r", "", "\n", "", " ", "").Replace(b64))
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	if len(ct) == 0 || len(ct)%16 != 0 {
		return nil, fmt.Errorf("ciphertext length %d not a multiple of 16", len(ct))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pt := make([]byte, len(ct))
	for i := 0; i < len(ct); i += 16 {
		block.Decrypt(pt[i:i+16], ct[i:i+16])
	}
	pad := int(pt[len(pt)-1])
	if pad <= 0 || pad > 16 || pad > len(pt) {
		return nil, fmt.Errorf("bad pkcs7 padding")
	}
	return pt[:len(pt)-pad], nil
}

// ctyunCountdownRe parses the console's human countdown, e.g.
// "14分后刷新限额" / "3天14时22分后刷新限额" / "29天14时18分后刷新限额".
var ctyunCountdownRe = regexp.MustCompile(`(?:(\d+)天)?(?:(\d+)时)?(?:(\d+)分)?后刷新限额`)

// ctyunCountdownSec parses tips into seconds until reset; -1 when unparseable.
func ctyunCountdownSec(tips string) int64 {
	m := ctyunCountdownRe.FindStringSubmatch(tips)
	if m == nil {
		return -1
	}
	var d, h, min int64
	if m[1] != "" {
		d, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m[2] != "" {
		h, _ = strconv.ParseInt(m[2], 10, 64)
	}
	if m[3] != "" {
		min, _ = strconv.ParseInt(m[3], 10, 64)
	}
	if d == 0 && h == 0 && min == 0 {
		return -1
	}
	return d*86400 + h*3600 + min*60
}

// parseCtyunUsage maps the usage/detail payload to quota windows. usage is a
// 0..1 ratio of the window limit; the period names 近5小时/本周/套餐总量 map to
// the 5h/weekly/monthly window keys. The plan tier's reference request caps
// (ctyunLimitsForPlan) scale the ratio into 已用/参考上限 request counts. The
// countdown in tips becomes ResetInSec.
func parseCtyunUsage(body []byte, planName string, now time.Time) ([]WindowStatus, *UsageDetail, error) {
	var payload struct {
		ResultCode int    `json:"resultCode"`
		ResultMsg  string `json:"resultMsg"`
		Data       struct {
			Usages []struct {
				Period string  `json:"period"`
				Tips   string  `json:"tips"`
				Usage  float64 `json:"usage"`
			} `json:"usages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if payload.ResultCode != 0 {
		msg := payload.ResultMsg
		if msg == "" {
			msg = fmt.Sprintf("resultCode %d", payload.ResultCode)
		}
		return nil, nil, fmt.Errorf("%s", msg)
	}
	if len(payload.Data.Usages) == 0 {
		return nil, nil, fmt.Errorf("无用量窗口数据")
	}

	keyByPeriod := map[string]string{"近5小时": "5h", "本周": "weekly", "套餐总量": "monthly"}
	labelByPeriod := map[string]string{"近5小时": "近5小时", "本周": "本周", "套餐总量": "套餐总量"}
	limits := ctyunLimitsForPlan(planName)
	totalByKey := map[string]float64{"5h": limits.fiveHourly, "weekly": limits.weekly, "monthly": limits.monthly}
	var windows []WindowStatus
	for _, u := range payload.Data.Usages {
		key, ok := keyByPeriod[u.Period]
		if !ok {
			continue
		}
		percent := u.Usage * 100
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		total := totalByKey[key]
		windows = append(windows, WindowStatus{
			Key:        key,
			Label:      labelByPeriod[u.Period],
			Percent:    percent,
			Used:       u.Usage * total,
			Total:      total,
			ResetInSec: ctyunCountdownSec(u.Tips),
			Status:     "ok",
		})
	}
	if len(windows) == 0 {
		return nil, nil, fmt.Errorf("用量窗口名无法识别: %q", payload.Data.Usages[0].Period)
	}
	detail := &UsageDetail{}
	detail.MarkUsageMetricsAvailable()
	return windows, detail, nil
}

// ctyunRandHex returns n random bytes as lowercase hex (2n chars).
func ctyunRandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ctyunRandAlphaNum returns n random alphanumeric chars (device code, web
// random, clientKey plaintext).
func ctyunRandAlphaNum(n int) string {
	const cs = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	out := make([]byte, n)
	for i, v := range b {
		out[i] = cs[int(v)%len(cs)]
	}
	return string(out)
}
