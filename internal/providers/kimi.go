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

// Kimi For Coding quota endpoint. Not part of the public API docs — the
// endpoint was reverse-engineered (see farion1231/cc-switch).
const kimiBase = "https://api.kimi.com"

type kimi struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init registers the Kimi For Coding adapter with the provider registry.
func init() {
	Register(
		"kimi",
		"Kimi For Coding",
		"Kimi For Coding 用量 (API Key 查询)",
		newKimi,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://platform.moonshot.cn 订阅 Kimi For Coding\n" +
				"2. 打开 https://platform.moonshot.cn/console/api-keys\n" +
				"3. 创建 API Key, 复制填入上方「API Key」\n" +
				"4. 用量为 5小时滚动窗口 + 每周窗口两条进度",
		},
		ProviderField{Key: "cookie", Label: "API Key", Kind: "password", Required: true, Placeholder: "sk- 开头的 API Key"},
	)
	RegisterWindows("kimi", "5h", "weekly")
}

func newKimi(cfg config.ProviderConfig) (Provider, error) {
	return &kimi{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *kimi) ID() string   { return p.cfg.ID }
func (p *kimi) Name() string { return p.cfg.Name }

func (p *kimi) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		kimiBase+"/coding/v1/usages", nil)
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
		return res, fmt.Errorf("kimi auth failed: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("查询失败: HTTP %d", resp.StatusCode)
		return res, fmt.Errorf("kimi status %d", resp.StatusCode)
	}

	windows, err := parseKimiQuota(body, time.Now())
	if err != nil {
		res.Error = fmt.Sprintf("解析用量数据失败: %v", err)
		return res, err
	}
	res.Windows = windows
	return res, nil
}

// parseKimiQuota extracts quota windows from the usages endpoint body.
// limits[] holds the 5-hour rolling window (each entry has a detail object
// with limit/remaining/resetTime); the usage object holds the weekly window.
// Values may be numbers or numeric strings. Plans without a 5h limit simply
// lack limits entries.
func parseKimiQuota(body []byte, now time.Time) ([]WindowStatus, error) {
	var payload struct {
		Limits []struct {
			Detail struct {
				Limit     any `json:"limit"`
				Remaining any `json:"remaining"`
				ResetTime any `json:"resetTime"`
			} `json:"detail"`
		} `json:"limits"`
		Usage struct {
			Limit     any `json:"limit"`
			Remaining any `json:"remaining"`
			ResetTime any `json:"resetTime"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	var windows []WindowStatus
	for _, l := range payload.Limits {
		w, ok := windowFromLimitRemaining(l.Detail.Limit, l.Detail.Remaining,
			l.Detail.ResetTime, "5h", "5小时", now)
		if ok {
			windows = append(windows, w)
		}
	}
	if w, ok := windowFromLimitRemaining(payload.Usage.Limit, payload.Usage.Remaining,
		payload.Usage.ResetTime, "weekly", "本周", now); ok {
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("no usage entries")
	}
	return windows, nil
}
