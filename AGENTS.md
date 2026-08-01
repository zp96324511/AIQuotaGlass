# AIQuotaGlass — Agent 项目上下文

Windows 桌面悬浮工具:多厂商 AI 套餐/API 用量定时自动刷新与阈值告警。透明玻璃拟态、无边框、置顶、贴边吸附,不遮挡工作区。首厂商为 **OpenCode Go**。

## 技术栈
- Go 1.25 (go.mod `go 1.25.0`,工具链自动切到 go1.25.12) + Wails v3 `v3.0.0-alpha2.119`(锁定版本)
- 前端:原生 TypeScript + Vite + `@wailsio/runtime`,无框架依赖
- 桌面:WebView2(Windows),无边框 + `BackgroundTypeTransparent` 透明窗口

## 常用命令

```powershell
# 环境变量必须先设(C: 盘满,全部指向 D:)
$env:GOPATH='D:\gocache'; $env:GOMODCACHE='D:\gocache\pkg\mod'
$env:GOCACHE='D:\gocache\build'; $env:GOTMPDIR='D:\gocache\tmp'
$env:PATH="D:\gocache\bin;$env:PATH"     # wails3 CLI
$env:AQUOTA_CONFIG_DIR='D:\aiquotaglass\data'   # 运行时:配置+WebView2数据

# 构建(生产,输出 bin\aiquotaglass.exe)
wails3 build
# 开发热重载
wails3 dev
# 后端编译/静态检查(不打包前端)
go build -o bin\debug.exe .
go vet ./...
go test ./...
```

注意:User 级环境变量已持久化;每次新 shell 用 `go build`/`wails3` 前仍需设置(除非重开终端)。

## 架构地图

```
main.go                     入口;窗口配置(Frameless+AlwaysOnTop+透明+禁缩放);托盘(关闭隐藏,菜单: 显示/刷新/退出)
app.go                      AppService —— 前端绑定的唯一服务(setup 内部连接);
                            OpenSettings/CloseSettings 管理设置弹窗窗口
internal/config/            配置:JSON 持久化,DPAPI 加密 cookie,AQUOTA_CONFIG_DIR
internal/providers/         Provider 接口 + opencode-go(SSR HTML 正则解析) + zhipu(API Key 查询)
internal/scheduler/         定时刷新(防重入,可停/重启/立即执行)
internal/notify/            Windows toast(PowerShell WinRT 桥,CREATE_NO_WINDOW)
internal/edge/              贴边吸附(按窗口所在显示器 workarea + SetWindowPos, 10px 阈值)
snap_*.go / window_adapter.go  OS 专属胶水(platform-tagged)
frontend/                   玻璃拟态 UI;src/main.ts 双窗口模式,public/style.css
```

## 关键约定(改代码前必读)

- **Wails 绑定**:只有 `AppService` 的**导出**方法进入 `frontend/bindings`。接口类型参数无法 JSON 序列化——`setup` 必须小写。新增绑定方法 = 导出方法,参数/返回值用可 JSON 类型。
- **前端调用**:`import { AppService } from "../bindings/aiquotaglass"`(绑定是模块,不是类);事件用 `@wailsio/runtime` 的 `Events.On`。
- **事件协议**(后端→前端):`usage:update`(ProviderResult[])、`usage:alert`({provider,window,percent,threshold})、`config:saved`(AppConfig)。
- **双窗口**:主窗口为悬浮小窗;`AppService.OpenSettings()` 懒创建设置弹窗(`URL: /?settings=1`),`CloseSettings()` 关闭。前端用 `location.search` 判断 `?settings=1` 进入设置模式(隐藏 widget、显示 `.settings-win`)。事件全局广播,两窗口都能收到。
- **托盘/关闭行为**:主窗 ✕ 按钮 = `HideToTray()`(隐藏,进程驻留继续刷新);`WindowClosing` 钩子(RegisterHook)同样转隐藏并 `e.Cancel()`;真正的退出只有托盘菜单「退出」(或 `AppService.Quit()`)。必须保持 `Windows.DisableQuitOnLastWindowClosed: true` + `HiddenOnTaskbar`,否则隐藏/关窗会退出进程。托盘用 `app.SystemTray.New()` + `SetMenu`(显示窗口/刷新数据/退出),左键点击显示窗口。
- **拖拽**:Wails v3 的 JS 拖拽机制,元素 CSS 设 `--wails-draggable: drag`,按钮等禁拖区设 `no-drag`。**不要用** `-webkit-app-region`(WebView2 不支持)。见 `drag.js`:`mousedown` 后 `mousemove` 触发 `invoke("wails:drag")` → 后端 `PostMessage(WM_NCLBUTTONDOWN, HTCAPTION)`。
- **透明度(前端 CSS 实现)**:Wails v3 透明窗口走 DirectComposition(WS_EX_NOREDIRECTIONBITMAP),原生 SetLayeredWindowAttributes 会破坏渲染。透明度在前端 `document.body.style.opacity` 应用(仅主窗口,设置弹窗恒不透明)。不要再走原生 opacity 路径。
- **多账号**:`providers` 列表天然支持同 type 多实例,`id` 必须唯一(alertArmed 键 = providerID/windowKey)。设置面板 "+ 添加账号"(可先选厂商类型,仅列已注册类型)/"删除此账号" 按钮管理,`syncDraftFromDom()` 先回写 DOM 再重渲染。
- **新增厂商(插件式注册表)**:`internal/providers` 实现 `Provider` 接口(`Query(ctx) (*Result, error)`),在文件 `init()` 里 `Register(type, name, desc, factory, fields...)` 自注册。`fields`(ProviderField: key/label/kind text|password|checkbox)声明设置面板的动态参数表单,key 映射 `ProviderConfig` 槽位(workspace/cookie/detail.showUsageDetail)。`New()` 按 `Type` 查注册表;`Types()` 供前端枚举(设置面板每张卡片顶部选类型,表单按 schema 渲染)。**Go 的 `plugin` 包不支持 Windows**,运行时动态 .so 插件不可行,需要真·插件走外部进程模型(未实现)。
- **配置教程(help 字段)**:设置面板每张厂商卡片内置「获取配置信息教程」(ProviderField kind=help,Label 为多行步骤文案),教用户从浏览器 DevTools Network 复制 cookie、从控制台 URL 复制 workspace,手动填写。**不再有快捷登录**——CDP 拉起浏览器抓 cookie 的方案已废弃(依赖 Edge/Chrome + 配置复制,太重)。
- **cookie**:磁盘存 DPAPI 加密(非 Windows 平台 base64 `plain:` 前缀),内存明文;`config.json` 手改放明文也能加载(解密失败回退原文)。
- **前端与后端共享结构**:Go `config.AppConfig` / `providers.Result` 自动生成 TS 接口于 `frontend/bindings/`,别手写。
- **生成物勿手改**:`frontend/bindings/`、`frontend/dist/`(已 gitignore)。

## 已知坑(踩过)

1. **C: 盘 0 字节空闲**。所有写盘路径必须 D:。无 `AQUOTA_CONFIG_DIR` 时应用会在 AppData 写配置失败。
2. **OpenCode 用量无独立 JSON API**——数据 SSR 内嵌在页面 HTML(`rollingUsage:`/`usage.list`),用正则解析。字段含 `$R[NN]=` 混淆,详见 `internal/providers/opencode.go` 的 `reWindows`/`reRecord`。
3. 验证 opencode 接口用 `curl.exe`,**不要用** `Invoke-WebRequest`(会 302 跳 OpenAuth 登录页)。
4. Wails v3 alpha **没有内置通知 API**——toast 走 PowerShell WinRT(`internal/notify/notify_windows.go`),每次告警起一个隐藏 powershell 进程。
5. 透明窗口:`BackgroundTypeTransparent` 创建时生效;运行时透明度**必须走前端 CSS**(`document.body.style.opacity`)。原生 SetLayeredWindowAttributes(WS_EX_LAYERED)与 DirectComposition 冲突会破坏渲染——不要再走原生路径。
6. 贴边=缩条:每 800ms 轮询 `edge.SnapToEdge`(拖拽中左键按下时跳过,见 `snap_windows.go` 的 `mouseLeftDown`)+ 前端 `mouseup` 调 `SnapIfNearEdge()`。贴边后窗口自动缩成进度条(竖条 44x200 贴左右、横条 200x44 贴上下,见 `app.go` 的 `setSnapState`),显示 `snapProviderID`(设置面板「贴边展示账号」,空=第一个启用账号)的 5h/周/月 三条进度;点条 `ExpandWidget()` 恢复 340x300 并偏移 40px 离开吸附区。前端 `widget:snap` 事件({dir,providerID})切换形态。`SnapToEdge` 返回方向字符串,**窗口已贴边时也返回方向**(勿改回空串,否则缩条不触发)。拖拽用 `--wails-draggable: drag`。
7. WebView2 远程调试(仅调试):`Windows.AdditionalBrowserArgs: ["--remote-debugging-port=9223"]`,然后 CDP `Runtime.evaluate` 读 DOM。发布版必须移除。
8. `wails3 build` 默认 `CGO_ENABLED=0`,纯 Go,无需 gcc。`-tags production`。
9. 绑定接口参数警告:任何导出方法带接口/函数类型都会生成 `any` 并运行时报错——规避。

## 目录规模
- 后端 ~700 行,前端 ~450 行。保持精简;新增功能优先复用 `internal/providers`/`internal/config` 抽象。
