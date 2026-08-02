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

// DeepSeek is pay-as-you-go (prepaid balance): the official API key exposes no
// quota/reset windows, only the account balance via the undocumented
// GET /user/balance endpoint (same one used by farion1231/cc-switch). The
// balance is rendered as a 100-unit quota window in the UI.
const deepseekBalanceURL = "https://api.deepseek.com/user/balance"

type deepseek struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init registers the DeepSeek adapter with the provider registry.
func init() {
	Register(
		"deepseek",
		"DeepSeek",
		"DeepSeek 官方 API 余额 (API Key 查询)",
		newDeepSeek,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://platform.deepseek.com 充值余额\n" +
				"2. 打开 https://platform.deepseek.com/api_keys\n" +
				"3. 创建 API Key, 复制填入上方「API Key」\n" +
				"4. 余额按 100 元参考线换算进度 (≥100 元满血, 0 元耗尽);\n" +
				"   鼠标悬停进度条查看真实余额",
		},
		ProviderField{Key: "cookie", Label: "API Key", Kind: "password", Required: true, Placeholder: "sk- 开头的 API Key"},
	)
	RegisterWindows("deepseek", "balance")
}

func newDeepSeek(cfg config.ProviderConfig) (Provider, error) {
	return &deepseek{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *deepseek) ID() string   { return p.cfg.ID }
func (p *deepseek) Name() string { return p.cfg.Name }

func (p *deepseek) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, deepseekBalanceURL, nil)
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
		return res, fmt.Errorf("deepseek auth failed: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("查询失败: HTTP %d", resp.StatusCode)
		return res, fmt.Errorf("deepseek status %d", resp.StatusCode)
	}

	windows, err := parseDeepSeekBalance(body)
	if err != nil {
		res.Error = fmt.Sprintf("解析余额数据失败: %v", err)
		return res, err
	}
	res.Windows = windows
	return res, nil
}

// parseDeepSeekBalance extracts the account balance from /user/balance. The
// primary currency entry (balance_infos[0]) drives the window; additional
// currencies are ignored. Balance values may be numbers or numeric strings.
// An is_available=false flag with zero balance reads as a depleted account
// (100% used) rather than an error.
func parseDeepSeekBalance(body []byte) ([]WindowStatus, error) {
	var payload struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency     string `json:"currency"`
			TotalBalance any    `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(payload.BalanceInfos) == 0 {
		return nil, fmt.Errorf("no balance entries")
	}
	info := payload.BalanceInfos[0]
	balance, ok := valueAsFloat(info.TotalBalance)
	if !ok {
		return nil, fmt.Errorf("invalid total_balance value")
	}
	currency := info.Currency
	if currency == "" {
		currency = "CNY"
	}
	return []WindowStatus{balanceWindow(balance, currency, "余额")}, nil
}
