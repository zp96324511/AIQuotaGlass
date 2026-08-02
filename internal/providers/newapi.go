package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aiquotaglass/internal/config"
)

// New API (https://github.com/QuantumNous/new-api) token quota endpoint.
// The relay panel exposes the quota of a single sk-... token via this route
// (PR #1161); no panel login is required, only the token itself.
const newAPIPath = "/api/usage/token"

type newAPI struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init registers the New API adapter with the provider registry.
func init() {
	Register(
		"new-api",
		"New API",
		"New API 中转面板令牌额度 (API Key 查询)",
		newNewAPI,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 部署 new-api (https://github.com/QuantumNous/new-api)\n" +
				"2. 登录面板 → 令牌 → 添加令牌, 创建带额度的 API Key (sk- 开头)\n" +
				"3. 服务地址填面板地址 (如 https://newapi.xxx.com), API Key 填入上方\n" +
				"4. 显示为「总配额」一条进度 (已用百分比); 需面板支持 /api/usage/token 接口 (较新版本)",
		},
		ProviderField{Key: "workspace", Label: "服务地址 (Base URL)", Kind: "text", Required: true, Placeholder: "https://your-newapi.example.com"},
		ProviderField{Key: "cookie", Label: "API Key", Kind: "password", Required: true, Placeholder: "sk- 开头的 API Key"},
	)
	RegisterWindows("new-api", "total")
}

func newNewAPI(cfg config.ProviderConfig) (Provider, error) {
	return &newAPI{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *newAPI) ID() string   { return p.cfg.ID }
func (p *newAPI) Name() string { return p.cfg.Name }

func (p *newAPI) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	base := normalizeBaseURL(p.cfg.Workspace)
	if base == "" {
		res.Error = "请填写服务地址"
		return res, fmt.Errorf("new-api: empty base url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+newAPIPath, nil)
	if err != nil {
		res.Error = fmt.Sprintf("请求构造失败: %v", err)
		return res, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.cfg.Cookie))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "AIQuotaGlass/0.1 (+https://github.com/zp96324511/AIQuotaGlass)")

	resp, err := p.client.Do(req)
	if err != nil {
		res.Error = fmt.Sprintf("查询失败: %v", err)
		return res, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		res.Error = fmt.Sprintf("读取响应失败: %v", err)
		return res, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		res.Error = "API Key 无效或已过期"
		return res, fmt.Errorf("new-api auth failed: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("查询失败: HTTP %d", resp.StatusCode)
		return res, fmt.Errorf("new-api status %d", resp.StatusCode)
	}

	windows, err := parseNewAPIQuota(body, time.Now())
	if err != nil {
		res.Error = fmt.Sprintf("解析用量数据失败: %v", err)
		return res, err
	}
	res.Windows = windows
	// Token expiry comes free with the same response; best-effort parse.
	if d, err := parseNewAPIUsageDetail(body, time.Now()); err == nil {
		res.Detail = d
	}
	return res, nil
}

// parseNewAPIQuota extracts the token quota from the /api/usage/token body.
// total_granted/total_available are the (used+remaining)/remaining quota in
// new-api units; the widget shows a single "total" window as used percent.
// Unlimited tokens have no finite quota and render as a 0% window.
func parseNewAPIQuota(body []byte, now time.Time) ([]WindowStatus, error) {
	var payload struct {
		Code    bool   `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TotalGranted   any `json:"total_granted"`
			TotalAvailable any `json:"total_available"`
			UnlimitedQuota bool `json:"unlimited_quota"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if !payload.Code {
		msg := payload.Message
		if msg == "" {
			msg = "code != true"
		}
		return nil, fmt.Errorf("%s", msg)
	}

	if payload.Data.UnlimitedQuota {
		return []WindowStatus{{Key: "total", Label: "总配额", Percent: -1, Status: "ok", ResetInSec: -1}}, nil
	}
	w, ok := windowFromLimitRemaining(payload.Data.TotalGranted, payload.Data.TotalAvailable,
		nil, "total", "总配额", now)
	if !ok {
		return nil, fmt.Errorf("no quota data")
	}
	return []WindowStatus{w}, nil
}

// parseNewAPIUsageDetail extracts the token expiry from the /api/usage/token
// body. expires_at is a unix-seconds timestamp; 0 means the token never
// expires (the panel normalizes -1 to 0).
func parseNewAPIUsageDetail(body []byte, now time.Time) (UsageDetail, error) {
	var payload struct {
		Data struct {
			ExpiresAt any `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return UsageDetail{}, fmt.Errorf("invalid JSON: %w", err)
	}
	sec, ok := valueAsFloat(payload.Data.ExpiresAt)
	if !ok || sec <= 0 {
		return UsageDetail{}, fmt.Errorf("no expiry")
	}
	exp := time.Unix(int64(sec), 0)
	d := UsageDetail{ExpiresAt: exp.Format("2006-01-02")}
	if rem := int64(exp.Sub(now).Seconds()); rem > 0 {
		d.ExpiresInSec = rem
	}
	return d, nil
}
