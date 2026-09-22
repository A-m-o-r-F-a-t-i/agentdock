# GitHub Windows 构建与发布

　　Windows x64 安装包的标准发布入口是 `.github/workflows/windows-package.yml`。后续版本由 GitHub 托管的 Windows Runner 从干净提交重新编译，不再把本机已有安装包直接上传为常规发布资产。

## 1. 两种运行模式

### 手动候选构建

　　在 GitHub Actions 中运行 **Windows Package**，保持 `publish=false`，并将 `ref` 设为需要验证的分支、提交或标签。工作流执行完整 Go 回归、静态分析、Windows 安装器契约测试、Windows x64 ZIP 和离线 Setup 构建；产物通过版本与校验和验证后，还会在一次性 Windows Runner 上真实执行 Setup 安装、同版本修复、旧任务迁移、后台启动／停止和卸载，再上传一个保留 30 天的 Actions Artifact。该模式不修改 Git 标签或 Release。

### 标签自动发布

　　推送 `vX.Y.Z` 标签后，Windows Package 自动检出该标签指向的提交并执行同一构建链。版本号必须与 `internal/buildinfo` 中的版本一致，标签必须指向实际构建提交。构建和两次资产验证通过后，工作流创建或更新对应 GitHub Release。

　　标签自动发布默认标记为 Pre-release。需要正式版本时，可从 Actions 手动运行同一工作流，选择现有版本标签、启用 `publish` 并关闭 `prerelease`。工作流不会创建指向任意分支的新标签，手动发布使用的标签必须已经存在并指向本次检出的提交。

## 2. 发布前文件

　　发布前必须提供一份详细更新说明，路径采用以下任一形式：

```text
docs/releases/vX.Y.Z.md
docs/releases/vX.Y.Z-主题.md
```

　　精确文件优先。不存在精确文件时，主题文件必须且只能匹配一份，否则发布阶段失败并保留未公开的 Draft Release。更新说明应覆盖主要功能、行为变化、兼容性、安装升级、验证结果和已知限制，避免只使用自动生成的提交列表代替版本说明。

## 3. 云端构建内容

　　GitHub Runner 使用 `go.mod` 指定的 Go 版本和 .NET 8 SDK，调用仓库现有的 `packaging/windows/build-windows-release.ps1`，只构建 `windows/amd64`：

```text
AgentDockSetup-amd64.exe
AgentDockSetup-amd64.exe.sha256
agentdock_windows_amd64.zip
agentdock_windows_amd64.zip.sha256
install.ps1
install.ps1.sha256
```

　　构建脚本下载官方 cloudflared 兼容载荷并验证其 Authenticode 签名。当前仓库没有配置 Windows 代码签名证书，因此 AgentDock EXE、ZIP 和 Setup 按未签名资产发布；工作流会明确验证并记录该状态，不会把未签名包描述为已签名包。

## 4. 发布门禁

　　`verify-windows-release-assets.ps1` 在构建后和发布前各执行一次，检查以下条件：

- Release 目录只能包含预期的六个文件。
- 三个主要载荷的 SHA-256 与对应校验文件一致。
- `build-report.json` 的版本、完整提交号、通道、平台和源码洁净状态正确。
- ZIP 内 `agentdock.exe` 的版本、12 位提交号和 `windows/amd64` 平台正确。
- Setup 的 ProductVersion 与标签版本一致。
- cloudflared 的 Authenticode 验证结果为有效。
- 离线 Setup 能从完整旧布局迁移，创建由托盘 WinExe 托管的管理员 Core，保持健康检查通过且不弹出控制台窗口；重复修复、旧计划任务迁移、停止／重启和静默卸载均通过。
- GitHub 上传后的资产名称、大小和服务器端 SHA-256 digest 与本地产物一致。

　　发布阶段先创建 Draft Release，再上传和远端复核全部资产，最后才解除 Draft。中途失败时不会公开缺少文件的 Release。重复运行同一标签会使用 `--clobber` 更新同名 Windows 资产，并重新执行完整远端校验。

## 5. 旧工作流边界

　　`.github/workflows/release.yml` 保留为手动的跨平台签名发布流程。它依赖 Windows 签名证书、容器注册表凭据和多平台产物，不再响应普通 `v*` 标签。这样可以避免一个标签同时启动 Windows 常规发布和缺少凭据的跨平台发布，造成无意义的失败记录。
