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

// OpenRouter is pay-as-you-go (prepaid credits): the official API key exposes
// no quota/reset windows, only credit totals via GET /api/v1/credits. The
// remaining credits are rendered as a 100-unit quota window in the UI.
const openrouterCreditsURL = "https://openrouter.ai/api/v1/credits"

type openrouter struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init registers the OpenRouter adapter with the provider registry.
func init() {
	Register(
		"openrouter",
		"OpenRouter",
		"OpenRouter 官方 API 余额 (API Key 查询)",
		newOpenRouter,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://openrouter.ai/settings/keys 创建 API Key\n" +
				"2. 在 https://openrouter.ai/settings/credits 充值 credits\n" +
				"3. 复制 API Key 填入上方「API Key」\n" +
				"4. 余额按 100 美元参考线换算进度 (≥$100 满血, $0 耗尽);\n" +
				"   鼠标悬停进度条查看真实余额",
		},
		ProviderField{Key: "cookie", Label: "API Key", Kind: "password", Required: true, Placeholder: "sk-or- 开头的 API Key"},
	)
	RegisterWindows("openrouter", "balance")
}

func newOpenRouter(cfg config.ProviderConfig) (Provider, error) {
	return &openrouter{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *openrouter) ID() string   { return p.cfg.ID }
func (p *openrouter) Name() string { return p.cfg.Name }

func (p *openrouter) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openrouterCreditsURL, nil)
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
		return res, fmt.Errorf("openrouter auth failed: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("查询失败: HTTP %d", resp.StatusCode)
		return res, fmt.Errorf("openrouter status %d", resp.StatusCode)
	}

	windows, err := parseOpenRouterCredits(body)
	if err != nil {
		res.Error = fmt.Sprintf("解析余额数据失败: %v", err)
		return res, err
	}
	res.Windows = windows
	return res, nil
}

// parseOpenRouterCredits extracts remaining credits from /api/v1/credits.
// remaining = total_credits - total_usage; a key with no credits left reads as
// 100% used.
func parseOpenRouterCredits(body []byte) ([]WindowStatus, error) {
	var payload struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	remaining := payload.Data.TotalCredits - payload.Data.TotalUsage
	if remaining < 0 {
		remaining = 0
	}
	return []WindowStatus{balanceWindow(remaining, "USD", "余额")}, nil
}
