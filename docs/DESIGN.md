# AIQuotaGlass 设计文档

## 1. 背景与目标

### 1.1 问题
使用多厂商 AI 套餐(Coding Plan / Token Plan)或直连 API 开发时,用量消耗是黑盒——额度耗尽前往往没有感知,导致工作中断。各厂商控制台互不相同,逐个登录查看成本高。

### 1.2 目标
- 常驻 Windows 桌面,一个悬浮小窗同时展示多个厂商的套餐/API 用量
- 定时自动刷新,阈值告警(应用内提示 + 系统通知)
- 界面简约、玻璃拟态、透明、无边框、可置顶、贴边吸附,不遮挡桌面工作区
- 可扩展:新增厂商只需实现一个查询接口

### 1.3 非目标(当前版本)
- 不做用量扣费/计费分析(仅展示与告警)
- 不做多用户/云同步
- 不做历史趋势图

## 2. 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 语言 | Go 1.25 | 单 exe、低内存(实测 ~30MB)、定时/HTTP/并发强项 |
| 桌面框架 | Wails v3 (alpha2.119, 锁定) | 后端 Go + 前端 WebView2;`Frameless`+`AlwaysOnTop`+`BackgroundTypeTransparent` 一等支持;体积小 |
| 前端 | 原生 TS + Vite + @wailsio/runtime | 无框架依赖,轻量;`--wails-draggable: drag` 走 Wails 自带拖拽运行时 |
| 渲染 | WebView2(系统自带) | 透明窗口 + backdrop-filter 玻璃效果 |
| 通知 | PowerShell WinRT toast | Wails v3 alpha 无内置通知 API |
| 持久化 | JSON + Windows DPAPI | cookie 加密存储,防明文泄露 |

备选被否:Electron(体积/内存过大)、PySide6(需带 Python 运行时)、JavaFX(JVM 过重)、Fyne(不支持真透明窗口)。

## 3. 总体架构

```
┌────────────────────────────────────────────────┐
│ frontend (WebView2, 透明玻璃窗口)              │
│   index.html / src/main.ts / public/style.css  │
│   ├─ 用量卡片: 5小时/本周/本月 进度条+倒计时    │
│   ├─ 设置面板: 厂商配置 + 阈值 + 全局选项       │
│   └─ 告警 toast                                 │
└──────────────┬─────────────────────────────────┘
               │ Wails bindings (JSON-RPC over IPC)
┌──────────────▼─────────────────────────────────┐
│ backend (Go)                                    │
│  main.go ── 窗口/应用装配                        │
│  app.go ── AppService (唯一绑定服务)             │
│    ├─ scheduler 定时 tick ──► refresh()         │
│    │    ├─ providers.New(cfg) ──► Query()       │
│    │    ├─ checkAlerts() ──► Event + notify     │
│    │    └─ Event.Emit("usage:update")           │
│    └─ edgeDockLoop 800ms ──► edge.SnapToEdge    │
└─────────────────────────────────────────────────┘
```

### 3.1 依赖方向
`main` → `app` → `internal/{config,providers,scheduler,notify,edge}`。上层依赖下层接口,下层不感知上层。

### 3.2 平台隔离
OS 专属代码全部 platform-tagged,业务层通过接口解耦:
- `windowControl` 接口(app.go):`SetAlwaysOnTop/SetPosition/Position/Quit/NativeHandle`,`wailsWindow`(window_adapter.go)实现
- `snap(hwnd, on)` / `mouseLeftDown()`:snap_*.go
- `Notifier` 接口:notify_windows.go(PowerShell toast)、non-windows noop
- `edge` 包:w32 实现,`WorkArea` 走原生 SPI_GETWORKAREA syscall
- 窗口透明度不再走原生层(与 DirectComposition 冲突),由前端 `document.body.style.opacity` 实现

## 4. 模块说明

### 4.1 internal/config
- `AppConfig`:refreshIntervalSec、nativeNotify、edgeDock、alwaysOnTop、opacity、providers[]
- `ProviderConfig`:id、name、type、enabled、workspace、cookie、alertThresholds、detail
- 存储路径:`AQUOTA_CONFIG_DIR` 环境变量 > `os.UserConfigDir()/AIQuotaGlass`。运行时数据(WebView2 userData)同目录 `webview/`
- 加载流程:读 JSON → 逐 provider `Decrypt(cookie)` 还原明文入内存;保存流程:`Save` 先 `Encrypt` 再落盘(0o600)
- 解密失败回退:手改 config.json 放明文 cookie 也能加载

### 4.2 internal/providers
- `Provider` 接口:`ID()/Name()/Query(ctx) (*Result, error)`
- `Result`:providerId、providerName、windows[]、detail、updatedAt、error
- `WindowStatus`:key(5h/weekly/monthly)、label、percent、resetInSec、status
- `UsageDetail`:requests、cost(USD)、cacheHit(百分比)——由使用历史页聚合
- **插件式注册表**:`Register(type, name, desc, factory, fields...)` 由各厂商文件 `init()` 自注册。`fields` 声明该类型在设置面板的**动态参数表单**(`ProviderField`: key → ProviderConfig 槽位 workspace/cookie/detail.showUsageDetail、label、kind text/password/checkbox)。`New(cfg)` 按 `cfg.Type` 查表路由;`Types()` 返回类型+字段 schema 供 UI 渲染。加厂商 = 加一个 Go 文件 + `init()` 注册,主程序与前端零改动;前端表单自动按 schema 加载
- **多账号**:`AppConfig.Providers` 是列表,同 `type` 允许多实例(每个实例独立 id/workspace/cookie/阈值)。设置面板支持添加/删除账号,`id` 必须唯一(告警去重键 = `providerID/windowKey`)。每次 `refresh` 为每个启用实例 `New()` 一个 provider 串行查询
- **限制**:Go `plugin` 包不支持 Windows,无运行时动态加载;第三方插件需外部进程模型(未实现)

### 4.3 opencode-go 实现(首个厂商)
- 无独立 JSON API。数据经 SSR 内嵌在控制台页面 HTML,带 `$R[NN]=` 混淆:
  - `/workspace/{ws}/go` → `rollingUsage/weeklyUsage/monthlyUsage`(用量窗口)
  - `/workspace/{ws}/usage` → `usage.list`(逐请求记录:inputTokens/outputTokens/reasoningTokens/cacheReadTokens/cost)
- 请求头:`Cookie: auth=<cookie>`,`User-Agent` 自报。HTTP 20s 超时
- 正则:`reWindows`(三窗口)、`reRecord`(请求明细)、`reAuthPage`(登录态失效检测)
- 成本换算:`cost` 字段单位 1e-8 USD(`cost/1e8 = $`)
- 缓存命中率 = ΣcacheRead / (ΣcacheRead + Σinput)

### 4.4 internal/scheduler
- 简单 ticker,防重入:`Start/Stop/SetInterval/RunNow`;单次 tick 超时 60s
- tick 回调 = `refresh(ctx)`:遍历启用厂商并行(当前串行,未来可并行化),组装 `[]Result` emit

### 4.5 internal/notify
- `Notifier.Show(ctx, title, message)`;Windows 实现缓存 toast.ps1 到配置目录,`powershell -NoProfile -WindowStyle Hidden` 隐藏执行(CREATE_NO_WINDOW)
- 告警频率:边缘触发(percent 从 <阈值 到 ≥阈值 才发一次,回落再触发)

### 4.6 internal/edge
- `WorkArea()`:SPI_GETWORKAREA(排除任务栏)
- `SnapToEdge(hwnd, snap)`:窗口距边 ≤10px 时 SetWindowPos 贴平(左/右/上/下)
- 触发:AppService.edgeDockLoop 每 800ms 轮询 + 前端 mouseup 调 `SnapIfNearEdge()`;轮询期间检测左键按下(`mouseLeftDown`)则跳过,避免拖拽中途被吸边

### 4.7 设置弹窗(双窗口)
- 主窗口为悬浮小窗;`AppService.OpenSettings()` 懒创建第二个 WebviewWindow(`URL: /?settings=1`,460×640,Frameless+Solid 背景,禁缩放,置顶),`CloseSettings()` 关闭;`WindowClosing` 事件置空引用以便重开
- 同一套前端 `src/main.ts` 按 `location.search` 分支:`?settings=1` 进入设置模式(隐藏 `.widget`、显示 `.settings-win`),否则为悬浮小窗
- 事件全局广播,两窗口都能收到 `config:saved`;`usage:update`/`usage:alert` 仅主窗口渲染
- 设置弹窗头部 `--wails-draggable: drag`,可拖动;footer 为「+ 添加账号 / 测试通知 / 取消 / 保存」
- **厂商卡片**:每张卡片顶部有**厂商类型下拉**(只列已注册类型)+ 名称 badge;下方参数表单**按该类型注册的 `Fields` schema 动态渲染**(切换类型会重置该账号的类型专属凭据);阈值(5h/周/月)为通用字段始终渲染;删除按钮在卡片内

### 4.8 配置教程(help 字段)
- 无快捷登录(曾用 CDP 拉起浏览器抓 cookie,依赖 Edge/Chrome + 复制用户配置,太重,已废弃)
- 每张厂商卡片内置「获取配置信息教程」:ProviderField kind=help,Label 为多行步骤文案,前端渲染为可折叠 `<details>` 提示框(`white-space: pre-wrap`)
- 教程指导用户:DevTools → Network → 从请求 Cookie 头复制 `auth=` 值;从控制台 URL 提取 `wrk_` workspace
- 凭据由用户手动复制填写,应用不接触浏览器

## 5. 数据流

### 5.1 用量刷新
```
scheduler tick
  → 遍历 enabled providers
  → providers.New(cfg).Query(ctx)
      → GET console 页面 → 正则解析 → Result
  → checkAlerts(res)
      → 与阈值比较 → 边缘触发 → Event("usage:alert") + notify.Show
  → Event("usage:update", results)  ──► 前端 render()
```

### 5.2 配置保存
```
前端设置表单 → AppService.SaveConfig(cfg)
  → config.Save(encrypt cookie) → 更新内存 → applyWindowState → 重启调度间隔
  → Event("config:saved") → 前端刷新 currentConfig
  → 立即 refresh()
```

## 6. 配置模型(JSON)
```json
{
  "refreshIntervalSec": 300,
  "nativeNotify": true,
  "edgeDock": true,
  "alwaysOnTop": true,
  "opacity": 1,
  "providers": [{
    "id": "opencode-go",
    "name": "OpenCode Go",
    "type": "opencode-go",
    "enabled": true,
    "workspace": "wrk_...",
    "cookie": "<DPAPI base64>",
    "alertThresholds": {"5h": 80, "weekly": 80, "monthly": 80},
    "detail": {"showUsageDetail": true}
  }]
}
```

## 7. 前端协议

### 7.1 绑定方法(AppService,生成于 frontend/bindings)
- `GetConfig(): AppConfig`
- `GetProviderTypes(): ProviderType[]`(注册表枚举,供「添加账号」选类型)
- `SaveConfig(cfg): void`
- `RefreshAll(): ProviderResult[]`
- `SetAlwaysOnTop(on)`
- `OpenSettings() / CloseSettings()`
- `SnapIfNearEdge()`
- `TestNotify()`
- `Quit()`

### 7.2 事件(后端 → 前端)
| 事件 | 载荷 | 触发 |
|---|---|---|
| `usage:update` | `ProviderResult[]` | 每次刷新完成 |
| `usage:alert` | `{provider, window, percent, threshold}` | 越过阈值(边缘) |
| `config:saved` | `AppConfig` | 保存配置 |

## 8. 安全
- cookie(会话凭据)磁盘 DPAPI 加密(`CryptProtectData`,绑定当前用户);内存明文,前端密码框回显
- 非 Windows 平台降级为 base64 `plain:` 前缀(明文,文档声明)
- 明文 cookie 文件权限 0600
- 不记录/上传任何使用数据;查询直连厂商域名

## 9. 跨平台策略
- Go 后端、providers、config、scheduler 三平台同代码
- 窗口透明/贴边/置顶按 OS 拆平台文件;透明度走前端 CSS(跨平台天然一致);Linux 透明受 WebKitGTK/Wayland 限制(可能降级为实心卡片)
- 通知:Windows = PowerShell toast;其他平台接入系统通知(预留 Notifier 接口)
- 新增平台 = 补 window/notify 平台文件,业务零改动

## 10. 构建与运行

```powershell
# 依赖环境(本机 C: 满,必须指向 D:)
$env:GOPATH='D:\gocache'; $env:GOMODCACHE='D:\gocache\pkg\mod'
$env:GOCACHE='D:\gocache\build'; $env:GOTMPDIR='D:\gocache\tmp'
$env:PATH="D:\gocache\bin;$env:PATH"
$env:AQUOTA_CONFIG_DIR='D:\aiquotaglass\data'

wails3 build          # 生产: bin\aiquotaglass.exe
wails3 dev            # 开发热重载
go vet ./... && go test ./...
```

`wails3 build` 流程:go mod tidy → generate bindings → vite build → generate icons/syso → `go build -tags production`。默认 CGO_ENABLED=0。

## 11. 已知限制
- C: 盘满导致默认路径(AppData)不可写——依赖 `AQUOTA_CONFIG_DIR` 迁移(见 AGENTS.md 坑 1)
- opencode cookie 为会话凭据,过期需重抓(控制台 DevTools → Network → Copy as cURL)
- `usage.list` 仅解析首页 ~50 条,非全量历史
- 告警仅支持窗口维度(5h/周/月),无成本维度
- Wails v3 alpha:API 可能随版本变动;已锁定版本号

## 12. 路线图
1. 第二个厂商(官方 API 余额/用量,如 Claude/Gemini/DeepSeek)——已具备注册表机制,加一个 Go 文件即可
2. ~~设置面板动态厂商表单~~(已实现:卡片内类型下拉 + 按 `Fields` schema 动态渲染)
3. 托盘图标 + 开机自启 + 单实例
4. 用量历史落盘 + 简单趋势
5. NSIS 安装包 / 发布签名
6. macOS 窗口实现
7. 外部进程插件模型(第三方厂商独立交付,基于注册表 Factory 包装)
