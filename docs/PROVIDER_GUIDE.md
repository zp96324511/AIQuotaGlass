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
└── zhipu_test.go      # 测试约定（parse 函数单测）
```

- **`Provider` 接口**：一个厂商 = 一个实现该接口的类型，职责是「根据持久化配置，查询该账号的用量窗口」。
- **`Register()`**：各厂商文件 `init()` 中自注册，把 `typeKey` → 工厂函数 + UI schema 写入注册表。
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
| `Detail UsageDetail` | 可选统计（requests / cost USD / cacheHit %），无则留零值 |
| `UpdatedAt` | `time.Now().Format("15:04:05")` |
| `Error string` | **非空 = 查询失败**，前端显示该字符串。失败时仍要返回 res（带上 Error）+ err |

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
2. **凭据错误**（401/403、业务响应提示 key 无效）→ `res.Error = "API Key 无效或已过期"`（前端按此文案给出可行动的提示）。
3. 网络失败 → `res.Error = fmt.Sprintf("查询失败: %v", err)`。
4. 每次都返回非 nil 的 res（带 Error 字段），err 供日志。

## 4. 参数表单 schema（ProviderField）

字段声明设置面板的动态表单，`Key` 必须是 `config.ProviderConfig` 的已知槽位：

| Kind | Key | 渲染 | 说明 |
|---|---|---|---|
| `text` | `workspace` | 单行输入 | 如 OpenCode 的 workspace ID |
| `password` | `cookie` | 密码框 | **API Key / 会话 cookie 都放这里**（磁盘 DPAPI 加密、前端不回显明文） |
| `checkbox` | `detail.<flag>` | 开关 | 厂商专属开关，存到 `ProviderConfig.Detail`（如智谱的 `detail.international`） |
| `help` | `""` | 可折叠教程框 | `Label` 为多行步骤文案（`\n` 换行），教用户如何获取凭据 |

约定：表单字段顺序 = `Register` 传入顺序，help 放最前。阈值（5h/周/月）是通用字段，前端始终渲染，无需声明。

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
