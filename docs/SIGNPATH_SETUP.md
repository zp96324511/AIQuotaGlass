# SignPath 代码签名配置指南

本指南说明如何为 AIQuotaGlass 配置免费的 SignPath 代码签名，以消除杀毒软件误报。

## 什么是 SignPath

[SignPath Foundation](https://signpath.org) 为开源项目提供**免费的 Authenticode 代码签名**服务。签名后的 exe 拥有受信任的数字证书，不会被 360、SmartScreen 等拦截。

## 配置步骤

### 1. 申请 SignPath 账户

1. 访问 [https://signpath.org/apply](https://signpath.org/apply)
2. 填写申请表，选择 **Open Source Project**
3. 提供项目信息：
   - **项目名称**: AIQuotaGlass
   - **仓库地址**: https://github.com/zp96324511/AIQuotaGlass
   - **License**: MIT
   - **简要描述**: Windows 桌面悬浮 AI 用量监控工具
4. 等待审核（通常 1-3 个工作日）

### 2. 安装 SignPath GitHub App

1. 审核通过后，安装 [SignPath GitHub App](https://github.com/apps/signpath)
2. 授予对 `zp96324511/AIQuotaGlass` 仓库的访问权限

### 3. 配置 SignPath 项目

登录 [app.signpath.io](https://app.signpath.io)：

1. **创建项目**
   - Project slug: `aiquotaglass`
   - Trusted Build System: GitHub.com

2. **创建证书**
   - 类型: Code Signing (Authenticode)

3. **配置 Signing Policy**
   - Signing Policy slug: `release-signing`
   - 关联证书

4. **配置 Artifact Configuration**（Artifact Configuration 标签页）
   - 由于 `upload-artifact` 会把 exe 打包成 ZIP，根元素使用 `<zip-file>`：

```xml
<artifact-configuration xmlns="http://signpath.io/artifact-configuration/v1">
  <zip-file>
    <pe-file path="aiquotaglass.exe">
      <authenticode-sign/>
    </pe-file>
  </zip-file>
</artifact-configuration>
```

   - 也可以在 SignPath Web UI 中用 **Simple Authenticode** 模板自动生成

### 4. 添加 GitHub Secrets

在仓库 Settings → Secrets and variables → Actions 中添加以下 secrets：

| Secret 名称 | 值 | 获取位置 |
|---|---|---|
| `SIGNPATH_API_TOKEN` | API Token | SignPath → User → API Token |
| `SIGNPATH_ORGANIZATION_ID` | Organization ID | SignPath → Organization → 基本信息 |
| `SIGNPATH_PROJECT_SLUG` | `aiquotaglass` | 创建项目时设置的 slug |

### 5. 发布

```bash
git tag v0.2.0
git push origin v0.2.0
```

GitHub Actions 会自动：
1. 构建未签名 exe
2. 上传 artifact 到 GitHub
3. 提交签名请求到 SignPath（SignPath 验证来源后签名）
4. 下载已签名 exe
5. 打包 ZIP + SHA-256
6. 发布 GitHub Release

## 故障排除

### 签名请求被拒绝

- 确认 SignPath GitHub App 已安装且有仓库权限
- 确认 Artifact Configuration 的 `<pe-file path="aiquotaglass.exe">` 与实际文件名一致
- 确认 Signing Policy slug 是 `release-signing`

### Artifact Configuration 不匹配

`upload-artifact@v4` 默认把文件打包成 ZIP 上传。SignPath 收到的是一个 ZIP，里面有一个 `aiquotaglass.exe`。因此 Artifact Configuration 的根元素必须是 `<zip-file>`。

### 首次签名后仍有 SmartScreen 警告

EV 证书会立即获得 SmartScreen 信任。SignPath 提供的证书需要**积累声誉**——随着下载量增加，警告会逐步消失。
