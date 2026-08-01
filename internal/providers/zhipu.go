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

// Zhipu GLM Coding Plan quota endpoint. Not part of the public API docs — the
// endpoint was reverse-engineered (see farion1231/cc-switch), and the CN and
// international hosts share the same backend and response shape.
const (
	zhipuCnBase = "https://open.bigmodel.cn"
	zhipuEnBase = "https://api.z.ai"
)

type zhipu struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init registers the Zhipu GLM adapter with the provider registry.
func init() {
	Register(
		"zhipu",
		"智谱 GLM",
		"智谱 GLM Coding Plan 用量 (API Key 查询)",
		newZhipu,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://bigmodel.cn 订阅 GLM Coding Plan (国内站)\n" +
				"   或 https://z.ai 订阅 (国际站, 需勾选下方「国际站 z.ai」)\n" +
				"2. 打开 https://bigmodel.cn/api-key (国内) 或 https://z.ai/manage-apikey (国际)\n" +
				"3. 创建 API Key, 复制填入上方「API Key」\n" +
				"4. 用量为 5小时滚动窗口 + 每周窗口两条进度",
		},
		ProviderField{Key: "cookie", Label: "API Key", Kind: "password", Required: true, Placeholder: "uuid 格式的 API Key"},
		ProviderField{Key: "detail.international", Label: "国际站 z.ai", Kind: "checkbox"},
	)
}

func newZhipu(cfg config.ProviderConfig) (Provider, error) {
	return &zhipu{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *zhipu) ID() string   { return p.cfg.ID }
func (p *zhipu) Name() string { return p.cfg.Name }

func (p *zhipu) base() string {
	if p.cfg.Detail.International {
		return zhipuEnBase
	}
	return zhipuCnBase
}

func (p *zhipu) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.base()+"/api/monitor/usage/quota/limit", nil)
	if err != nil {
		res.Error = fmt.Sprintf("请求构造失败: %v", err)
		return res, err
	}
	// Zhipu authenticates with the bare API key — no Bearer prefix.
	req.Header.Set("Authorization", strings.TrimSpace(p.cfg.Cookie))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "en-US,en")
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
		return res, fmt.Errorf("zhipu auth failed: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("查询失败: HTTP %d", resp.StatusCode)
		return res, fmt.Errorf("zhipu status %d", resp.StatusCode)
	}

	windows, err := parseZhipuQuota(body, time.Now())
	if err != nil {
		// The endpoint reports bad credentials as HTTP 200 with success=false
		// and a msg mentioning the token; surface it as a key problem.
		if strings.Contains(err.Error(), "令牌") || strings.Contains(err.Error(), "验证") {
			res.Error = "API Key 无效或已过期"
			return res, err
		}
		res.Error = fmt.Sprintf("解析用量数据失败: %v", err)
		return res, err
	}
	res.Windows = windows
	return res, nil
}

// parseZhipuQuota extracts quota windows from the monitor endpoint body.
// The limits array holds TOKENS_LIMIT entries whose unit marks the window:
// unit 3 = 5-hour rolling window, unit 6 = weekly window. Legacy plans return
// a single entry (5h only). Percentage is the already-used percent.
func parseZhipuQuota(body []byte, now time.Time) ([]WindowStatus, error) {
	var payload struct {
		Success bool `json:"success"`
		Msg     string `json:"msg"`
		Data    struct {
			Level  string `json:"level"`
			Limits []struct {
				Type          string `json:"type"`
				Percentage    float64 `json:"percentage"`
				NextResetTime int64  `json:"nextResetTime"`
				Unit          int64  `json:"unit"`
			} `json:"limits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if !payload.Success {
		return nil, fmt.Errorf("%s", payload.Msg)
	}

	var windows []WindowStatus
	for _, l := range payload.Data.Limits {
		if !strings.EqualFold(l.Type, "TOKENS_LIMIT") {
			continue
		}
		var key, label string
		switch l.Unit {
		case 3:
			key, label = "5h", "5小时"
		case 6:
			key, label = "weekly", "本周"
		default:
			continue
		}
		percent := l.Percentage
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		w := WindowStatus{
			Key:        key,
			Label:      label,
			Percent:    percent,
			Status:     "ok",
			ResetInSec: -1,
		}
		if l.NextResetTime > 0 {
			sec := (l.NextResetTime - now.UnixMilli()) / 1000
			if sec < 0 {
				sec = 0
			}
			w.ResetInSec = sec
		}
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("no TOKENS_LIMIT entries")
	}
	return windows, nil
}
