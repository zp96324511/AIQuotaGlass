# AIQuotaGlass

多厂商 AI 套餐/API 用量定时刷新与阈值告警的 Windows 桌面悬浮工具。

透明玻璃拟态、无边框、置顶、贴边吸附,不遮挡桌面工作区。首厂商支持 **OpenCode Go**。

## 功能

- 定时自动刷新(默认 5 分钟)多个厂商的套餐/API 用量
- 每个用量窗口(如 OpenCode Go 的 5小时/周/月限额)独立进度条 + 重置倒计时
- 阈值告警:应用内提示 + Windows 系统通知(边缘触发去重)
- 使用明细统计:请求数、费用、缓存命中率
- 悬浮窗:可拖拽、贴边吸附、置顶、透明度调节
- 配置(厂商/Cookie/阈值/间隔)通过设置面板维护,持久化到本地

## 快速开始

```powershell
# 环境变量(本机 C: 盘满,缓存与运行数据必须指向 D:)
$env:GOPATH='D:\gocache'; $env:GOMODCACHE='D:\gocache\pkg\mod'
$env:GOCACHE='D:\gocache\build'; $env:GOTMPDIR='D:\gocache\tmp'
$env:PATH="D:\gocache\bin;$env:PATH"
$env:AQUOTA_CONFIG_DIR='D:\aiquotaglass\data'

wails3 build
.\bin\aiquotaglass.exe
```

点击悬浮窗 ⚙ 配置厂商:
1. 登录 https://opencode.ai/auth
2. DevTools → Network → 页面请求 → Copy as cURL,取 `Cookie: auth=...`
3. 填入 Workspace ID(`wrk_...`)与 cookie,保存即自动刷新

## 技术栈

Go 1.25 + Wails v3 (alpha2.119) + WebView2 + 原生 TypeScript/Vite。

## 文档

- [设计文档](docs/DESIGN.md) — 架构、数据流、模块说明、扩展指南
- [AGENTS.md](AGENTS.md) — 开发环境、命令、约定、已知坑(给 AI 协作的上下文)
