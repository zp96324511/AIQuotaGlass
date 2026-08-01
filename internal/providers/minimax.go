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

// MiniMax Coding Plan quota endpoints. Not part of the public API docs — the
// endpoint was reverse-engineered (see farion1231/cc-switch). The CN and
// international hosts share the same response shape.
const (
	minimaxCnBase = "https://api.minimaxi.com"
	minimaxEnBase = "https://api.minimax.io"
)

type minimax struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init registers the MiniMax adapter with the provider registry.
func init() {
	Register(
		"minimax",
		"MiniMax",
		"MiniMax Coding Plan 用量 (API Key 查询)",
		newMiniMax,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://platform.minimaxi.com 订阅 Coding Plan (国内站)\n" +
				"   或 https://platform.minimax.io 订阅 (国际站, 需勾选下方「国际站」)\n" +
				"2. 打开 https://platform.minimaxi.com/user-center/basic-information/interface-key\n" +
				"   (国际站: https://platform.minimax.io/user-center/basic-information/interface-key)\n" +
				"3. 创建 API Key, 复制填入上方「API Key」\n" +
				"4. 用量为 5小时滚动窗口 + 每周窗口两条进度",
		},
		ProviderField{Key: "cookie", Label: "API Key", Kind: "password", Required: true, Placeholder: "minimax- 开头的 API Key"},
		ProviderField{Key: "detail.international", Label: "国际站 (api.minimax.io)", Kind: "checkbox"},
	)
}

func newMiniMax(cfg config.ProviderConfig) (Provider, error) {
	return &minimax{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *minimax) ID() string   { return p.cfg.ID }
func (p *minimax) Name() string { return p.cfg.Name }

func (p *minimax) base() string {
	if p.cfg.Detail.International {
		return minimaxEnBase
	}
	return minimaxCnBase
}

func (p *minimax) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.base()+"/v1/api/openplatform/coding_plan/remains", nil)
	if err != nil {
		res.Error = fmt.Sprintf("请求构造失败: %v", err)
		return res, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.cfg.Cookie))
	req.Header.Set("Content-Type", "application/json")
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
		return res, fmt.Errorf("minimax auth failed: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("查询失败: HTTP %d", resp.StatusCode)
		return res, fmt.Errorf("minimax status %d", resp.StatusCode)
	}

	windows, err := parseMiniMaxQuota(body, time.Now())
	if err != nil {
		res.Error = fmt.Sprintf("解析用量数据失败: %v", err)
		return res, err
	}
	res.Windows = windows
	return res, nil
}

// parseMiniMaxQuota extracts quota windows from the coding plan remains body.
// Only the "general" entry of model_remains (the coding plan) is used, the
// "video" etc. models are skipped. Fields hold the REMAINING percent, so the
// used percent is 100 - remaining. The weekly bucket only exists when
// current_weekly_status == 1 (status 3 means the plan has no weekly limit).
// Error responses carry base_resp.status_code != 0.
func parseMiniMaxQuota(body []byte, now time.Time) ([]WindowStatus, error) {
	var payload struct {
		BaseResp struct {
			StatusCode any    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
		ModelRemains []struct {
			ModelName                   string `json:"model_name"`
			CurrentIntervalRemainingPct any    `json:"current_interval_remaining_percent"`
			CurrentWeeklyStatus         any    `json:"current_weekly_status"`
			CurrentWeeklyRemainingPct   any    `json:"current_weekly_remaining_percent"`
			EndTime                     any    `json:"end_time"`
			WeeklyEndTime               any    `json:"weekly_end_time"`
		} `json:"model_remains"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if code, ok := valueAsFloat(payload.BaseResp.StatusCode); ok && code != 0 {
		msg := payload.BaseResp.StatusMsg
		if msg == "" {
			msg = fmt.Sprintf("status_code %v", payload.BaseResp.StatusCode)
		}
		return nil, fmt.Errorf("%s", msg)
	}

	var general *struct {
		ModelName                   string `json:"model_name"`
		CurrentIntervalRemainingPct any    `json:"current_interval_remaining_percent"`
		CurrentWeeklyStatus         any    `json:"current_weekly_status"`
		CurrentWeeklyRemainingPct   any    `json:"current_weekly_remaining_percent"`
		EndTime                     any    `json:"end_time"`
		WeeklyEndTime               any    `json:"weekly_end_time"`
	}
	for i := range payload.ModelRemains {
		if payload.ModelRemains[i].ModelName == "general" {
			general = &payload.ModelRemains[i]
			break
		}
	}
	if general == nil {
		return nil, fmt.Errorf("no general model entry")
	}

	var windows []WindowStatus
	remainToUsed := func(pct float64) float64 {
		used := 100 - pct
		if used < 0 {
			used = 0
		}
		if used > 100 {
			used = 100
		}
		return used
	}

	if pct, ok := valueAsFloat(general.CurrentIntervalRemainingPct); ok {
		w := WindowStatus{Key: "5h", Label: "5小时", Percent: remainToUsed(pct), Status: "ok"}
		if sec, ok := resetInSec(general.EndTime, now); ok {
			w.ResetInSec = sec
		}
		windows = append(windows, w)
	}
	if status, ok := valueAsFloat(general.CurrentWeeklyStatus); ok && status == 1 {
		if pct, ok := valueAsFloat(general.CurrentWeeklyRemainingPct); ok {
			w := WindowStatus{Key: "weekly", Label: "本周", Percent: remainToUsed(pct), Status: "ok"}
			if sec, ok := resetInSec(general.WeeklyEndTime, now); ok {
				w.ResetInSec = sec
			}
			windows = append(windows, w)
		}
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("no usage entries")
	}
	return windows, nil
}
