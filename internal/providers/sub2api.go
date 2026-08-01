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

// Sub2API (https://github.com/Wei-Shaw/sub2api) gateway usage endpoints.
// GET /v1/usage reports the authenticated API Key's quota without requiring a
// panel login; it is the same endpoint the public "Key Usage" page uses.
// GET /v1/sub2api/billing reports the key's billing multiplier (incl. the
// peak-rate window when active) and only authenticates without charging.
const (
	sub2apiPath        = "/v1/usage"
	sub2apiBillingPath = "/v1/sub2api/billing"
)

type sub2API struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init registers the Sub2API adapter with the provider registry.
func init() {
	Register(
		"sub2api",
		"Sub2API",
		"Sub2API 中转面板用量 (API Key 查询)",
		newSub2API,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 部署 sub2api (https://github.com/Wei-Shaw/sub2api)\n" +
				"2. 登录面板 → 创建 API Key, 复制填入上方「API Key」\n" +
				"3. 服务地址填面板地址 (如 https://sub2api.xxx.com)\n" +
				"4. 配额模式显示 总配额 + 5小时/1天/7天速率限制;\n" +
				"   订阅模式显示 日/周/月限额; 钱包余额模式显示 无限额度\n" +
				"5. 明细行显示 今日费用 + 近30天费用 + 请求数 + 缓存命中率;\n" +
				"   分组/倍率来自 /v1/sub2api/billing (峰值时段自动计算), 有效期来自 /v1/usage",
		},
		ProviderField{Key: "workspace", Label: "服务地址 (Base URL)", Kind: "text", Required: true, Placeholder: "https://your-sub2api.example.com"},
		ProviderField{Key: "cookie", Label: "API Key", Kind: "password", Required: true, Placeholder: "sk- 开头的 API Key"},
	)
}

func newSub2API(cfg config.ProviderConfig) (Provider, error) {
	return &sub2API{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (p *sub2API) ID() string   { return p.cfg.ID }
func (p *sub2API) Name() string { return p.cfg.Name }

func (p *sub2API) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	base := normalizeBaseURL(p.cfg.Workspace)
	if base == "" {
		res.Error = "请填写服务地址"
		return res, fmt.Errorf("sub2api: empty base url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+sub2apiPath, nil)
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
		return res, fmt.Errorf("sub2api auth failed: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("查询失败: HTTP %d", resp.StatusCode)
		return res, fmt.Errorf("sub2api status %d", resp.StatusCode)
	}

	windows, err := parseSub2APIUsage(body, time.Now())
	if err != nil {
		res.Error = fmt.Sprintf("解析用量数据失败: %v", err)
		return res, err
	}
	res.Windows = windows
	res.Detail = p.queryDetail(ctx, base, body)
	return res, nil
}

// queryDetail assembles the detail row: cost figures from the /v1/usage body
// plus, best-effort, the billing multiplier from /v1/sub2api/billing.
func (p *sub2API) queryDetail(ctx context.Context, base string, usageBody []byte) UsageDetail {
	now := time.Now()
	var d UsageDetail
	if parsed, err := parseSub2APIUsageDetail(usageBody, now); err == nil {
		d = parsed
	}
	if b, err := p.get(ctx, base+sub2apiBillingPath); err == nil {
		if rate, peak, err := parseSub2APIBilling(b); err == nil {
			d.RateMultiplier = rate
			d.PeakActive = peak
		}
	}
	return d
}

// get performs a GET with the key's bearer auth and returns the body on 200.
func (p *sub2API) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.cfg.Cookie))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "AIQuotaGlass/0.1 (+https://github.com/zp96324511/AIQuotaGlass)")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return body, nil
}

// parseSub2APIUsage extracts quota windows from the /v1/usage body. The
// response has three shapes selected by "mode":
//
//   - quota_limited: the key's own wallet quota (USD, used percent) plus
//     per-window rate limits (5h/1d/7d token windows).
//   - unrestricted + subscription: daily/weekly/monthly USD limits of the
//     subscription plan (weekly window resets 7d after weekly_window_start).
//   - unrestricted + wallet: no fixed quota — rendered as an unlimited window.
//
// Keys outside the alert set (1d/7d) are display-only: no threshold input
// exists for them, so alerts never fire for those windows.
func parseSub2APIUsage(body []byte, now time.Time) ([]WindowStatus, error) {
	var payload struct {
		Mode    string `json:"mode"`
		IsValid bool   `json:"isValid"`
		Status  string `json:"status"`
		Quota   *struct {
			Limit     float64 `json:"limit"`
			Used      float64 `json:"used"`
			Remaining float64 `json:"remaining"`
			Unit      string  `json:"unit"`
		} `json:"quota"`
		RateLimits []struct {
			Window    string `json:"window"`
			Limit     any    `json:"limit"`
			Used      any    `json:"used"`
			Remaining any    `json:"remaining"`
			ResetAt   any    `json:"reset_at"`
		} `json:"rate_limits"`
		Subscription *struct {
			DailyUsageUSD    float64 `json:"daily_usage_usd"`
			WeeklyUsageUSD   float64 `json:"weekly_usage_usd"`
			MonthlyUsageUSD  float64 `json:"monthly_usage_usd"`
			DailyLimitUSD    float64 `json:"daily_limit_usd"`
			WeeklyLimitUSD   float64 `json:"weekly_limit_usd"`
			MonthlyLimitUSD  float64 `json:"monthly_limit_usd"`
			WeeklyWindowStart any    `json:"weekly_window_start"`
		} `json:"subscription"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if !payload.IsValid {
		return nil, fmt.Errorf("API Key 不可用 (status %s)", payload.Status)
	}

	var windows []WindowStatus

	if payload.Quota != nil && payload.Quota.Limit > 0 {
		if w, ok := windowFromLimitRemaining(payload.Quota.Limit, payload.Quota.Remaining,
			nil, "total", "配额", now); ok {
			windows = append(windows, w)
		}
	}

	rateLabels := map[string]string{"5h": "5小时", "1d": "今日", "7d": "7天"}
	for _, rl := range payload.RateLimits {
		label, ok := rateLabels[rl.Window]
		if !ok {
			continue
		}
		if w, ok := windowFromLimitRemaining(rl.Limit, rl.Remaining, rl.ResetAt,
			rl.Window, label, now); ok {
			windows = append(windows, w)
		}
	}

	if payload.Subscription != nil {
		sub := payload.Subscription
		if sub.DailyLimitUSD > 0 {
			if w, ok := windowFromLimitRemaining(sub.DailyLimitUSD, sub.DailyLimitUSD-sub.DailyUsageUSD,
				nil, "1d", "今日", now); ok {
				windows = append(windows, w)
			}
		}
		if sub.WeeklyLimitUSD > 0 {
			var reset any
			if ts, ok := sub.WeeklyWindowStart.(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					reset = t.Add(7 * 24 * time.Hour).Format(time.RFC3339)
				}
			}
			if w, ok := windowFromLimitRemaining(sub.WeeklyLimitUSD, sub.WeeklyLimitUSD-sub.WeeklyUsageUSD,
				reset, "weekly", "本周", now); ok {
				windows = append(windows, w)
			}
		}
		if sub.MonthlyLimitUSD > 0 {
			if w, ok := windowFromLimitRemaining(sub.MonthlyLimitUSD, sub.MonthlyLimitUSD-sub.MonthlyUsageUSD,
				nil, "monthly", "本月", now); ok {
				windows = append(windows, w)
			}
		}
	}

	if len(windows) == 0 {
		if payload.Mode == "unrestricted" {
			return []WindowStatus{{Key: "total", Label: "余额", Percent: -1, Status: "ok", ResetInSec: -1}}, nil
		}
		return nil, fmt.Errorf("no quota data")
	}
	return windows, nil
}

// parseSub2APIUsageDetail extracts spend/group/expiry figures from the same
// /v1/usage body: today's cost (usage.today) and the rolling period cost (sum
// of the daily breakdown entries, ~30 days by default). actual_cost is
// preferred over the standard cost when present, matching the panel's own Key
// Usage page. The plan name (subscription mode only) becomes the group name;
// the key expiry comes from the top-level expires_at (quota mode) or
// subscription.expires_at (subscription mode); wallet mode has neither.
func parseSub2APIUsageDetail(body []byte, now time.Time) (UsageDetail, error) {
	var payload struct {
		PlanName    string  `json:"planName"`
		ExpiresAt   *string `json:"expires_at"`
		Subscription *struct {
			ExpiresAt *string `json:"expires_at"`
		} `json:"subscription"`
		Usage *struct {
			Today *struct {
				Requests        int64   `json:"requests"`
				InputTokens     int64   `json:"input_tokens"`
				OutputTokens    int64   `json:"output_tokens"`
				CacheReadTokens int64   `json:"cache_read_tokens"`
				Cost            float64 `json:"cost"`
				ActualCost      float64 `json:"actual_cost"`
			} `json:"today"`
		} `json:"usage"`
		DailyUsage []struct {
			Cost       float64 `json:"cost"`
			ActualCost float64 `json:"actual_cost"`
		} `json:"daily_usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return UsageDetail{}, fmt.Errorf("invalid JSON: %w", err)
	}

	var d UsageDetail
	if t := payload.Usage; t != nil && t.Today != nil {
		today := t.Today
		d.Requests = int(today.Requests)
		d.TodayCost = today.ActualCost
		if d.TodayCost <= 0 {
			d.TodayCost = today.Cost
		}
		if tokens := today.CacheReadTokens + today.InputTokens + today.OutputTokens; tokens > 0 {
			d.CacheHit = float64(today.CacheReadTokens) / float64(tokens) * 100
		}
	}
	for _, day := range payload.DailyUsage {
		cost := day.ActualCost
		if cost <= 0 {
			cost = day.Cost
		}
		d.PeriodCost += cost
	}

	// Wallet mode reports planName "钱包余额" — not a real group, skip it.
	if payload.PlanName != "" && payload.PlanName != "钱包余额" {
		d.GroupName = payload.PlanName
	}
	exp := payload.ExpiresAt
	if exp == nil && payload.Subscription != nil {
		exp = payload.Subscription.ExpiresAt
	}
	if exp != nil {
		if t, err := time.Parse(time.RFC3339, *exp); err == nil {
			d.ExpiresAt = t.Format("2006-01-02")
			if rem := int64(t.Sub(now).Seconds()); rem > 0 {
				d.ExpiresInSec = rem
			}
		}
	}

	if d.TodayCost <= 0 && d.PeriodCost <= 0 && d.Requests == 0 && d.GroupName == "" && d.ExpiresAt == "" {
		return UsageDetail{}, fmt.Errorf("no usage data")
	}
	return d, nil
}

// parseSub2APIBilling extracts the effective rate multiplier from the
// /v1/sub2api/billing body. effective_rate_multiplier already includes the
// peak-rate window computed by the panel for the current time; peakActive
// marks that a peak window is in effect right now.
func parseSub2APIBilling(body []byte) (rate float64, peakActive bool, err error) {
	var payload struct {
		EffectiveRateMultiplier float64  `json:"effective_rate_multiplier"`
		PeakRateEnabled         bool     `json:"peak_rate_enabled"`
		AppliedPeakMultiplier   *float64 `json:"applied_peak_multiplier"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, false, fmt.Errorf("invalid JSON: %w", err)
	}
	if payload.EffectiveRateMultiplier <= 0 {
		return 0, false, fmt.Errorf("no rate multiplier")
	}
	peakActive = payload.PeakRateEnabled && payload.AppliedPeakMultiplier != nil && *payload.AppliedPeakMultiplier > 1
	return payload.EffectiveRateMultiplier, peakActive, nil
}
