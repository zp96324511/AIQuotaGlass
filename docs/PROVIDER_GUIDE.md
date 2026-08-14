# 厂商插件开发指南

本指南面向想为 AIQuotaGlass 增加新厂商的开发者。项目内置一套**编译期插件注册表**：新增厂商 = 在 `internal/providers/` 加一个 Go 文件 + 一个 `init()` 注册调用，主程序与前端**零改动**。

> 限制：Go 的 `plugin` 包不支持 Windows，因此没有运行时动态加载。第三方厂商需随主程序一起编译，或走外部进程模型（未实现）。

## 1. 核心概念

```
internal/providers/
├── provider.go        # 接口 + 注册表 + 类型定义（不要改）
├── quota_util.go      # 通用解析工具（windowFromLimitRemaining 等）
├── opencode-go.go     # 示例：SSR HTML 正则解析（cookie + workspace）
├── zhipu.go           # 示例：API Key 查询（最简单，推荐先读）
├── kimi.go / minimax.go
├── sensenova.go       # 示例：账号密码 OAuth 登录（PKCE + JWE，自动续期）
└── zhipu_test.go      # 测试约定（parse 函数单测）
```

- **`Provider` 接口**：一个厂商 = 一个实现该接口的类型，职责是「根据持久化配置，查询该账号的用量窗口」。
- **`Register()`**：各厂商文件 `init()` 中自注册，把 `typeKey` → 工厂函数 + UI schema 写入注册表。
- **`RegisterWindows()`**：声明该厂商实际发出的用量窗口键（`5h`/`weekly`/`monthly`/`total`/`balance`），设置面板据此只渲染**适用**的阈值输入。
- **`ProviderField`**：声明该厂商在设置面板的动态参数表单（API Key、workspace、教程等）。
- **前端零改动**：设置面板按注册的 `Fields` 动态渲染表单；主窗口按 `Result.Windows` 渲染进度条。

## 2. 最小示例

以下是一个完整的 API Key 查询型厂商（模式与 `zhipu.go` 相同）。假设厂商 "example" 提供 `GET https://api.example.com/quota` 返回 JSON：

```go
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

type example struct {
	cfg    config.ProviderConfig
	client *http.Client
}

// init 注册到注册表。typeKey 必须是稳定字符串（持久化到 config.json，
// 不能改），建议用厂商域名去掉点号。
func init() {
	Register(
		"example",                          // typeKey（稳定、唯一）
		"Example AI",                       // 显示名
		"Example AI 套餐用量 (API Key 查询)", // UI 里的一行说明
		newExample,                         // 工厂函数
		// —— 参数表单 schema（可选，可多个）——
		ProviderField{
			Key: "help", Kind: "help",      // 可折叠教程框
			Label: "如何获取配置信息:\n" +
				"1. 登录 https://example.com 订阅套餐\n" +
				"2. 打开 https://example.com/api-key 创建 API Key\n" +
				"3. 复制填入上方「API Key」",
		},
		ProviderField{Key: "cookie", Label: "API Key", Kind: "password",
			Required: true, Placeholder: "sk- 开头的 API Key"},
	)
}

func newExample(cfg config.ProviderConfig) (Provider, error) {
	return &example{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (p *example) ID() string   { return p.cfg.ID }   // 多账号实例 ID（告警去重键）
func (p *example) Name() string { return p.cfg.Name } // 用户自定义显示名

func (p *example) Query(ctx context.Context) (*Result, error) {
	res := &Result{
		ProviderID:   p.cfg.ID,
		ProviderName: p.cfg.Name,
		UpdatedAt:    time.Now().Format("15:04:05"),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.example.com/quota", nil)
	if err != nil {
		res.Error = fmt.Sprintf("请求构造失败: %v", err)
		return res, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.cfg.Cookie))
	req.Header.Set("User-Agent", "AIQuotaGlass/0.1")

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
	// 凭据错误尽早报错，让前端显示可行动的提示
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		res.Error = "API Key 无效或已过期"
		return res, fmt.Errorf("example auth failed: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("查询失败: HTTP %d", resp.StatusCode)
		return res, fmt.Errorf("example status %d", resp.StatusCode)
	}

	windows, err := parseExampleQuota(body, time.Now())
	if err != nil {
		res.Error = fmt.Sprintf("解析用量数据失败: %v", err)
		return res, err
	}
	res.Windows = windows
	return res, nil
}

// parseExampleQuota 解析厂商返回的用量窗口。独立成纯函数，便于单测。
// 推荐用内置工具 windowFromLimitRemaining(limit, remaining, reset, key, label, now)：
// 输入 (限额, 剩余, 重置时间) 输出已用百分比 + 剩余秒数，limit<=0 自动跳过。
func parseExampleQuota(body []byte, now time.Time) ([]WindowStatus, error) {
	var payload struct {
		Data []struct {
			Window   string `json:"window"`  // "5h" | "weekly" | "monthly"
			Limit    any    `json:"limit"`   // 数字或数字字符串均可
			Remaining any   `json:"remaining"`
			ResetUTC any    `json:"resetAt"` // 秒或毫秒时间戳 / ISO8601
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	labelByKey := map[string]string{"5h": "5小时", "weekly": "本周", "monthly": "本月"}
	var windows []WindowStatus
	for _, d := range payload.Data {
		label, ok := labelByKey[d.Window]
		if !ok {
			continue
		}
		if w, ok := windowFromLimitRemaining(d.Limit, d.Remaining, d.ResetUTC,
			d.Window, label, now); ok {
			windows = append(windows, w)
		}
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("no usable quota windows")
	}
	return windows, nil
}
```

## 3. 接口与数据结构（provider.go）

```go
type Provider interface {
	ID() string                                // 账号实例 ID（= config.ProviderConfig.ID）
	Name() string                              // 显示名（= config.ProviderConfig.Name）
	Query(ctx context.Context) (*Result, error) // 查询用量
}
```

### Result（前端渲染 + 告警的输入）

| 字段 | 说明 |
|---|---|
| `ProviderID` / `ProviderName` | 回填自 cfg，勿硬编码 |
| `Windows []WindowStatus` | 用量窗口列表（5h/周/月），**告警维度** |
| `Detail *UsageDetail` | 可选统计（requests / cost USD / cacheHit %），无则为 nil；成功解析活动指标时调用 `MarkUsageMetricsAvailable()` |
| `UpdatedAt` | `time.Now().Format("15:04:05")` |
| `Error string` | **非空 = 查询失败**，前端显示该字符串。失败时仍要返回 res（带上 Error）+ err |
| `ErrorInfo *ErrorInfo` | 失败请求的结构化信息（`Method/URL/StatusCode/Body`），非 2xx 响应用 `httpErrorInfo(method, url, status, body)` 填充；网络错误（无 HTTP 响应）留 nil。前端据此在卡片名称前状态圆点变红，点击圆点打开请求信息弹窗 |

### 动态排序的活动契约（重要）

主窗口按"真实使用"把当前正在消耗配额的账号排到前面（`Percent` 增长，或明细的 `Requests/Cost/TodayCost/PeriodCost` 增长）。**只有消耗方向的增长才计为活动**，被动变化一律不置顶：

- **窗口数值**：`Percent` 增大计为活动（`Used` 不参与判定——余额型账号的 `Used` 是剩余额，消耗时反而减小）。
- **明细活动指标**：成功解析且调用过 `MarkUsageMetricsAvailable()` 后，`Requests/Cost/TodayCost/PeriodCost` **增大**计为活动；`CacheHit` 单独变化和元数据（`GroupName/ExpiresAt/...`）变化不计。
- **窗口重置/倒计时**：重置（`Percent` 回落归零）、普通 `ResetInSec` 递减、倒计时重启都不计为活动。不要为了表达重置而自行增减该值。
- **错误状态**：`Error` 进入/恢复/持续都不计为活动（可用性变化 ≠ 使用）。
- **明细不可用**：可选明细查询失败时应返回错误或保持 `Detail` 为 nil——后端会沿用上一份有效明细基线，不会把空明细误判为变化。**不要**在解析失败时返回一个"全零但标记可用"的 `UsageDetail`。

### WindowStatus

| 字段 | 说明 |
|---|---|
| `Key` | 窗口标识：`"5h"` / `"weekly"` / `"monthly"`（与 `alertThresholds` 的 key 一致） |
| `Label` | 中文显示名（5小时/本周/本月） |
| `Percent` | 0..100 已用百分比（`windowFromLimitRemaining` 已钳制） |
| `ResetInSec` | 距窗口重置的秒数，≤0 则前端隐藏倒计时 |
| `Status` | `"ok"`；异常时给错误标记 |

### 错误约定（重要）

1. **不要** panic；网络/解析错误一律走返回值。
2. **凭据错误**（401/403、业务响应提示 key 无效）→ `res.Error = "API Key 无效或已过期"`（前端按此文案给出可行动的提示）。同时用 `res.ErrorInfo = httpErrorInfo(method, url, resp.StatusCode, body)` 填充——响应体片段在请求信息弹窗中展示（点击卡片状态圆点），供用户排查。
3. 其他非 2xx 状态码：`res.Error = fmt.Sprintf("查询失败: HTTP %d", ...)` + `ErrorInfo`（同上）。`httpErrorInfo` 会自动剥离控制字符（含 ANSI 转义）、压缩空白并截断到 500 字符。
4. 凭据与敏感信息（cookie/API key）只放请求 **Header**；`ErrorInfo.URL` 会被展示到前端弹窗，绝不能带凭据。
5. 网络失败 → `res.Error = fmt.Sprintf("查询失败: %v", err)`（无 HTTP 响应，`ErrorInfo` 留 nil）。
6. 每次都返回非 nil 的 res（带 Error 字段），err 供日志。

## 4. 参数表单 schema（ProviderField）

字段声明设置面板的动态表单，`Key` 必须是 `config.ProviderConfig` 的已知槽位：

| Kind | Key | 渲染 | 说明 |
|---|---|---|---|
| `text` | `workspace` | 单行输入 | 如 OpenCode 的 workspace ID |
| `password` | `cookie` | 密码框 | **API Key / 会话 cookie 都放这里**（磁盘 DPAPI 加密、前端不回显明文） |
| `checkbox` | `detail.<flag>` | 开关 | 厂商专属开关，存到 `ProviderConfig.Detail`（如智谱的 `detail.international`） |
| `help` | `""` | 可折叠教程框 | `Label` 为多行步骤文案（`\n` 换行），教用户如何获取凭据 |

约定：表单字段顺序 = `Register` 传入顺序，help 放最前。

### 阈值窗口（`RegisterWindows()`）

设置面板的「阈值」输入并非通用——每个厂商实际发出的用量窗口不同（中转面板只发总额度、智谱没有月度窗口等）。在 `Register()` 之后调用 `RegisterWindows(typeKey, keys...)` 声明该厂商支持的窗口键，前端 `windowKeysFor()` 据此只渲染**适用**的阈值输入，告警循环也只对声明过的 key 去重。

```go
func init() {
    Register("example", "Example AI", "...", newExample,
        ProviderField{Key: "cookie", Label: "API Key", Kind: "password", Required: true},
    )
    // 与 Query() 实际 append 到 res.Windows 的 Key 完全一致
    RegisterWindows("example", "5h", "weekly", "monthly")
}
```

| 厂商 | 实际窗口 | 阈值输入数 |
|---|---|---|
| `opencode-go` | `5h` / `weekly` / `monthly` | 3 |
| `zhipu` / `kimi` / `minimax` | `5h` / `weekly` | 2 |
| `sensenova` | `5h` | 1 |
| `electronhub` | `5h`(今日) / `weekly` | 2 |
| `new-api` / `sub2api` | `total` | 1 |
| `deepseek` / `openrouter` | `balance` | 1 |

未声明 `RegisterWindows` 的厂商：设置面板不显示阈值输入，告警循环跳过全部窗口（用于无额度概念的厂商或尚未适配的实验类型）。

### 余额窗口（`balance`）

按量付费厂商（DeepSeek / OpenRouter 等）没有配额/重置窗口，只有账户余额。这类厂商：

- `Query()` 返回单个 `Key: "balance"` 的窗口，用 `balanceWindow(remaining, unit, label)` 构造（`internal/providers/quota_util.go`）——进度按 **100 单位参考线**换算：余额 ≥100 进度为 0（满血）、余额 0 进度为 100（耗尽）、中间线性。
- `Used` 存真实余额，`Total` 恒为 0，`Unit` 存币种（`CNY`/`USD`）；前端悬停进度条显示 `余额 ¥88.5`（CNY 用 ¥，其他用 $）。
- 阈值语义沿用统一 percent（80 默认 = 余额低于 20 元/美元时告警）。

### 账号密码 OAuth 自动登录（SenseNova 模式）

部分控制台只用短时效的 OAuth access_token（如商汤日日新约 3 小时）且**未启用 refresh_token grant**，用户无法直接维护 access_token。这类厂商改让用户填**账号密码**，由 provider 全自动完成登录续期：

- 字段：用户名复用 `workspace` 槽位、密码复用 `cookie` 槽位（本地 DPAPI 加密存储，与其它厂商 cookie 一致）。
- `Query()` 先取**包级缓存**的 access_token（`sensenovaCache`，按 provider ID 索引——`providers.New` 每轮重建实例，故缓存必须在包级，不能放 struct 字段）；token 剩余 >60s 直接复用，否则 `mintToken()` 重铸。
- `mintToken()` 重放完整 OAuth2 授权码流程：① 起一个无 session 的 PKCE authorize（S256 challenge，`sensenovaPKCE`）→ 从首个 302 捕获 `login_challenge`；② 取 IdP JWKS（`signin.sensecore.cn/.well-known/jwks.json`），挑 `kid:public:hydra.openid.id-token` 的 RSA 公钥；③ 用该公钥 **JWE 加密**密码（RSA-OAEP+A256GCM，Go 标准库实现，注意商汤用的是**非标准 5 段紧凑序列化** `protected.encrypted_key.iv.ciphertext.tag`——tag 单独成第 5 段，非 JWE 标准的内联）；④ POST `{username, password:<JWE>, challenge, is_encrypt:true}` 到 `iam/authn/v1/auth/nova/login` → 拿 `redirect` URL（含 login_verifier）；⑤ 带 cookie jar GET 该 URL 走 consent→code，`CheckRedirect` 在捕获到 `?code=` 或 `?error=` 时 `http.ErrUseLastResponse` 停下；⑥ `POST /oauth2/token`（authorization_code grant）换 access_token。
- 从 access_token 的 JWT 中段解码 `exp` 与 `ext.tenant_id`（account_id 自动解析，无需用户填）。
- API 返回 401/403 → 清缓存强制重铸并重试一次；仍失败报「登录后仍被拒绝, 请检查账号密码」。密码不改则长期免维护。
- 窗口语义：商汤各 Coding Plan 模型各有独立 5 小时窗口，`parseSenseNovaQuota` 取**消耗最高**（剩余%最低）的模型作为单个 `5h` 窗口，`Label` 为该模型名，便于贴边缩条与阈值告警（键 `5h` 稳定，标签动态）。

### 主 Key 查询近 7 天统计（ElectronHub 模式）

ElectronHub 的 DevPass 面板数据只走「cookie 换 JWT + 鉴权 WebSocket」，但 refresh_token 单次有效且与浏览器会话互踢，不适合长期挂机。折衷：用**永久主 key**（`ek-`，Dev key `ek-dev-` 无法查询）调 `GET /v1/user/me`，其 `history[]` 返回近 7 天按日聚合的 `requests/input_tokens/output_tokens`：

- 窗口：`history[0]`（最新一天，免时区假设）→ `5h` 键（Label「今日」）；全 7 条求和 → `weekly` 键。DevPass 无限 token，两窗口 `Percent: -1`（前端显示「无限」）、`Total: 0`、`ResetInSec: -1`，`Used` 携带真实 tokens 供 hover。
- 明细：`Detail.Requests` = 今日请求，`Detail.WeeklyRequests` = 本周请求（`UsageDetail` 专属可选字段，前端渲染「今日 N 次 · 本周 N 次」）；今日 requests 增长即计为活动（跨日重置 requests 回落不计，符合被动变化约定）。
- 该站 Cloudflare 拦默认 Go UA：请求须带桌面 Chrome UA。

## 5. 多账号

- `AppConfig.Providers` 是列表，同一 `type` 允许多实例；**每个实例 `id` 必须唯一**（告警去重键 = `providerID/windowKey`）。
- `ID()`/`Name()` 必须回传 `cfg.ID`/`cfg.Name`，不能返回 typeKey 或硬编码名字。

## 6. 测试约定

- **解析逻辑写成纯函数**（`parseXXX(body []byte, now time.Time)`），用固定时间戳的表驱动测试覆盖：正常多窗口、单窗口（旧套餐）、业务失败、非法 JSON、未知窗口类型。见 `zhipu_test.go`。
- 网络层不必 mock——解析函数独立后，Query 层只做 HTTP + 透传。
- 运行：`go test ./internal/providers/...`

## 7. 验证清单

1. `go build ./...`、`go vet ./...`、`go test ./...` 通过
2. `wails3 build`（重新生成 bindings + 前端）后启动 `bin/aiquotaglass.exe`
3. 设置面板：「+ 添加账号」→ 类型下拉出现新厂商 → 表单按 schema 渲染 → 填凭据保存
4. 主窗口出现新卡片且进度正常；阈值触发时告警按 `providerID/windowKey` 去重

## 8. 从 HTML 页面解析（OpenCode Go 模式）

厂商没有 JSON API、数据内嵌在页面 HTML 时，参考 `opencode-go.go`：

- `net/http` GET 控制台页面（20s 超时），请求头带 `Cookie: auth=<cookie>` 和自报 User-Agent
- 正则提取 SSR 数据（注意 `$R[NN]=` 混淆串，先 `strings.NewReplacer` 解混淆或写精确的正则）
- 登录态失效检测：响应里出现登录页特征（如 `<title>OpenAuth</title>`）→ 报「登录态已失效，请重新获取 cookie」
- 教程（help 字段）引导用户从 DevTools → Network → Copy as cURL 复制 cookie
