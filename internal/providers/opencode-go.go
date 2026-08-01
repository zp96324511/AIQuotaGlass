package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aiquotaglass/internal/config"
)

// opencodeGoBase is the workspace console host.
const opencodeGoBase = "https://opencode.ai"

// Regexes that extract the SSR-hydrated usage data from the console pages.
// The Go subscription page embeds rollingUsage/weeklyUsage/monthlyUsage, and
// the usage page embeds a usage.list array of per-request records.
var (
	reWindows = regexp.MustCompile(
		`rollingUsage:\$R\[\d+\]=\{status:"([^"]+)",resetInSec:(\d+),usagePercent:(\d+)\},` +
			`weeklyUsage:\$R\[\d+\]=\{status:"([^"]+)",resetInSec:(\d+),usagePercent:(\d+)\},` +
			`monthlyUsage:\$R\[\d+\]=\{status:"([^"]+)",resetInSec:(\d+),usagePercent:(\d+)\}`)
	reRecord = regexp.MustCompile(
		`timeCreated:\$R\[\d+\]=new Date\("([^"]+)"\),timeUpdated:\$R\[\d+\]=new Date\("[^"]+"\),timeDeleted:[^,]+,` +
			`model:"([^"]+)",provider:"[^"]*",inputTokens:(\d+),outputTokens:(\d+),reasoningTokens:(\d+),` +
			`cacheReadTokens:(\d+),cacheWrite5mTokens:[^,]+,cacheWrite1hTokens:[^,]+,cost:(\d+),keyID:"[^"]*",sessionID:"([^"]+)"`)
	reAuthPage = regexp.MustCompile(`<title>OpenAuth</title>`)
)

type openCodeGo struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init registers the OpenCode Go adapter with the provider registry, declaring
// the parameter form the settings UI renders for this type.
func init() {
	Register(
		"opencode-go",
		"OpenCode Go",
		"OpenCode Go 套餐用量 (SSR 页面解析)",
		newOpenCodeGo,
		ProviderField{
			Key: "help", Kind: "help",
			Label: "如何获取配置信息:\n" +
				"1. 在浏览器登录 https://opencode.ai (需已订阅 OpenCode Go)\n" +
				"2. 按 F12 打开开发者工具 → Network 标签\n" +
				"3. 刷新页面, 点击任意一条请求\n" +
				"4. 在 Request Headers 找到 Cookie, 复制其中 auth= 后面的值填入上方\n" +
				"5. Workspace ID: 地址栏 https://opencode.ai/workspace/wrk_xxxx 中 wrk_ 开头的一段",
		},
		ProviderField{Key: "workspace", Label: "Workspace ID", Kind: "text", Required: true, Placeholder: "wrk_..."},
		ProviderField{Key: "cookie", Label: "Cookie (auth=...)", Kind: "password", Required: true, Placeholder: "Fe26.2**..."},
		ProviderField{Key: "detail.showUsageDetail", Label: "统计使用明细 (请求/费用/缓存命中)", Kind: "checkbox"},
	)
}

func newOpenCodeGo(cfg config.ProviderConfig) (Provider, error) {
	return &openCodeGo{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (p *openCodeGo) ID() string   { return p.cfg.ID }
func (p *openCodeGo) Name() string { return p.cfg.Name }

func (p *openCodeGo) fetch(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opencodeGoBase+path, nil)
	if err != nil {
		return nil, err
	}
	// Tolerate the auth= prefix users copy along from DevTools.
	req.Header.Set("Cookie", "auth="+strings.TrimPrefix(p.cfg.Cookie, "auth="))
	req.Header.Set("User-Agent", "AIQuotaGlass/0.1 (+https://github.com/zp96324511/AIQuotaGlass)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return body, nil
}

func (p *openCodeGo) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}

	path := "/workspace/" + p.cfg.Workspace + "/go"
	body, err := p.fetch(ctx, path)
	if err != nil {
		res.Error = fmt.Sprintf("查询失败: %v", err)
		return res, err
	}
	if reAuthPage.Match(body) {
		res.Error = "登录已过期, 请更新 Cookie"
		return res, fmt.Errorf("session expired")
	}

	m := reWindows.FindSubmatch(body)
	if m == nil {
		res.Error = "解析用量数据失败"
		return res, fmt.Errorf("parse go page: no usage data")
	}
	windows := []WindowStatus{
		{Key: "5h", Label: "5小时", Status: string(m[1]), ResetInSec: parseInt(m[2]), Percent: parseFloat(m[3])},
		{Key: "weekly", Label: "本周", Status: string(m[4]), ResetInSec: parseInt(m[5]), Percent: parseFloat(m[6])},
		{Key: "monthly", Label: "本月", Status: string(m[7]), ResetInSec: parseInt(m[8]), Percent: parseFloat(m[9])},
	}
	res.Windows = windows

	if p.cfg.Detail.ShowUsageDetail {
		if d, err := p.queryDetail(ctx); err == nil {
			res.Detail = d
		}
	}
	return res, nil
}

// queryDetail parses the usage history page for per-request statistics.
func (p *openCodeGo) queryDetail(ctx context.Context) (UsageDetail, error) {
	var d UsageDetail
	body, err := p.fetch(ctx, "/workspace/"+p.cfg.Workspace+"/usage")
	if err != nil {
		return d, err
	}
	matches := reRecord.FindAllSubmatch(body, -1)
	var in, cache int64
	for _, m := range matches {
		in += parseInt(m[3])
		_ = m[2] // model name
		_ = parseInt(m[4])
		_ = parseInt(m[5])
		cache += parseInt(m[6])
		d.Cost += float64(parseInt(m[7])) / 1e8
		d.Requests++
	}
	if in+cache > 0 {
		d.CacheHit = float64(cache) / float64(in+cache) * 100
	}
	return d, nil
}

func parseInt(b []byte) int64 {
	n, _ := strconv.ParseInt(string(b), 10, 64)
	return n
}

func parseFloat(b []byte) float64 {
	f, _ := strconv.ParseFloat(string(b), 64)
	return f
}
