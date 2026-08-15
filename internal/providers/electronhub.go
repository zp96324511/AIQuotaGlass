package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"aiquotaglass/internal/config"
)

// ElectronHub DevPass usage via the master API key. The dashboard's DevPass
// panel talks to an authenticated WebSocket that only accepts a ~1h JWT
// minted from a rotating httpOnly refresh_token cookie — usable but fragile
// (single-use rotation fights with the browser session). Instead this
// provider uses the permanent master key (ek-...) against
// GET /v1/user/me, whose history[] array aggregates the last 7 days of
// per-day requests and input/output tokens (DevPass requests report 0
// credits, so the free/0.25-credit fields are irrelevant for subscribers).
//
// Windows carry the reference soft-headroom totals (20M daily / 100M weekly,
// Lite tier) as display-only scale: DevPass is unlimited tokens, so the bars
// show "how far into the slow lane", never a hard quota. history[] comes
// newest-day-first; entry[0] is "today" under the server's day boundary.
//
// Cloudflare blocks the default Go/curl User-Agent on electronhub.ai; a
// desktop Chrome UA passes (verified with curl + browser parity).
const (
	electronhubUserMeURL = "https://api.electronhub.ai/v1/user/me"
	// electronhubUA must look like a desktop browser to pass Cloudflare.
	electronhubUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
	// Reference soft headroom caps (Lite tier) used as display-only totals so
	// the bars carry a scale. DevPass is unlimited — going past these never
	// blocks, it only slows admission, so the percent is informational.
	electronhubDailySoftLimit = 20_000_000
	electronhubWeeklyCap     = 100_000_000
)

type electronhub struct {
	cfg    config.ProviderConfig
	client *http.Client
}

func init() {
	Register(
		"electronhub",
		"ElectronHub DevPass",
		"ElectronHub Coding Plan 用量 (主 API Key 查询近 7 天统计)",
		newElectronHub,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 浏览器登录 https://app.electronhub.ai 并订阅 Coding Plan (DevPass)\n" +
				"2. 左侧 Console → API keys, 复制 Master Key (ek- 开头,\n" +
				"   不是 ek-dev- 开头的 Dev key —— Dev key 无法查询用量)\n" +
				"3. 粘贴到上方「API Key」\n" +
				"4. 用量 = 今日/本周 tokens (输入+输出) 与请求次数统计;\n" +
				"   进度条按参考软上限 (今日 20M / 本周 100M) 换算,\n" +
				"   DevPass 无限 token, 超限仅降速不停用",
		},
		ProviderField{Key: "cookie", Label: "API Key (ek- 主密钥)", Kind: "password",
			Required: true, Placeholder: "ek- 开头的 Master Key"},
	)
	RegisterWindows("electronhub", "5h", "weekly")
}

func newElectronHub(cfg config.ProviderConfig) (Provider, error) {
	return &electronhub{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (p *electronhub) ID() string   { return p.cfg.ID }
func (p *electronhub) Name() string { return p.cfg.Name }

func (p *electronhub) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	key := strings.TrimSpace(p.cfg.Cookie)
	if key == "" {
		res.Error = "未配置 API Key"
		return res, fmt.Errorf("electronhub: missing api key")
	}

	body, status, err := p.fetchUserMe(ctx, key)
	if err != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			res.Error = "API Key 无效或已过期"
		} else if status != 0 {
			res.Error = fmt.Sprintf("查询失败: HTTP %d", status)
		} else {
			res.Error = fmt.Sprintf("查询失败: %v", err)
		}
		if status != 0 {
			res.ErrorInfo = httpErrorInfo(http.MethodGet, electronhubUserMeURL, status, body)
		}
		return res, err
	}

	windows, detail, perr := parseElectronhubUserMe(body, time.Now())
	if perr != nil {
		res.Error = fmt.Sprintf("解析用量数据失败: %v", perr)
		return res, perr
	}
	res.Windows = windows
	res.Detail = detail
	return res, nil
}

// fetchUserMe calls /v1/user/me with the master key and returns body, HTTP
// status, and any transport error. The body is always returned (possibly
// nil) so the caller can build an ErrorInfo on failure.
func (p *electronhub) fetchUserMe(ctx context.Context, key string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, electronhubUserMeURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", electronhubUA)
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

// electronhubHistoryEntry is one day of the history[] array.
type electronhubHistoryEntry struct {
	Date         string  `json:"date"` // "2026-08-14"
	Requests     int     `json:"requests"`
	Credits      float64 `json:"credits"`
	InputTokens  float64 `json:"input_tokens"`
	OutputTokens float64 `json:"output_tokens"`
}

// parseElectronhubUserMe maps /v1/user/me to quota windows and the usage
// detail. history[] order is not guaranteed, so entries are sorted by date
// first. "Today" is the entry matching the local date; when the server has
// not bucketed the local day yet (its day boundary is UTC, up to 8h behind
// UTC+8), the newest available entry is shown instead so the numbers never
// read zero while usage is ongoing. Weekly sums entries within 7 days of the
// newest bucket. Tokens are in+out sums; the 20M/100M totals are Lite-tier
// reference soft headroom (display-only — DevPass never hard-stops).
func parseElectronhubUserMe(body []byte, now time.Time) ([]WindowStatus, *UsageDetail, error) {
	var payload struct {
		Subscription string                    `json:"subscription"`
		History      []electronhubHistoryEntry `json:"history"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(payload.History) == 0 {
		return nil, nil, fmt.Errorf("history 为空 (账号无使用记录)")
	}

	entries := append([]electronhubHistoryEntry(nil), payload.History...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Date > entries[j].Date })

	today := entries[0]
	if localDate := now.Format("2006-01-02"); localDate != today.Date {
		for _, h := range entries {
			if h.Date == localDate {
				today = h
				break
			}
		}
		// No bucket for the local day yet: keep the newest entry as "today"
		// so early-morning usage shows the freshest available numbers.
	}

	newest, _ := time.ParseInLocation("2006-01-02", entries[0].Date, time.Local)
	weekStart := newest.AddDate(0, 0, -6)
	var weekTokens float64
	var weekRequests int
	for _, h := range entries {
		d, err := time.ParseInLocation("2006-01-02", h.Date, time.Local)
		if err != nil {
			continue
		}
		if d.Before(weekStart) {
			continue
		}
		weekTokens += h.InputTokens + h.OutputTokens
		weekRequests += h.Requests
	}

	todayTokens := today.InputTokens + today.OutputTokens
	used := func(v float64, limit float64) float64 {
		if v > limit {
			return 100
		}
		return v / limit * 100
	}
	windows := []WindowStatus{
		{Key: "5h", Label: "今日", Used: todayTokens, Total: electronhubDailySoftLimit,
			Percent: used(todayTokens, electronhubDailySoftLimit), ResetInSec: -1, Status: "ok"},
		{Key: "weekly", Label: "本周", Used: weekTokens, Total: electronhubWeeklyCap,
			Percent: used(weekTokens, electronhubWeeklyCap), ResetInSec: -1, Status: "ok"},
	}
	detail := &UsageDetail{
		Requests:       today.Requests,
		WeeklyRequests: weekRequests,
	}
	detail.MarkUsageMetricsAvailable()
	return windows, detail, nil
}
