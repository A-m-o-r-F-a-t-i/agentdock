# WB06 macOS 原生 Workbench 验收记录

基线提交：`b367eaab95202873fb213b8713440bf7822878c4`
工作线：`parallel/20260925/macos`
构建方式：仅 GitHub Actions；本工作线不安装生产环境、不发布、不合并。

## 自动化验收

专属工作流 `.github/workflows/parallel-macos.yml` 在每个本线提交上执行以下检查：

1. **原生 Apple Silicon runner**：`macos-15` / `arm64`。
2. **原生 Intel runner**：`macos-15-intel` / `x86_64`。
3. 两种 runner 均执行 Workbench 模型、120/180/300/30 秒时间边界、未知字段、SSE 分片/游标/上限、全量 Swift 类型检查和既有 macOS 回归测试。
4. 两种 runner 均从同一个不可变提交生成 AppKit 浅色/深色截图并记录 `uname`、`sw_vers`、Xcode 与二进制架构。
5. 两种 runner 分别构建原生 Darwin Core 载荷，随后在 Apple Silicon runner 上构建 `arm64 + x86_64` 通用 App、DMG 和更新 ZIP。
6. 候选包执行 Bundle ID、产品名、版本、源码提交、内嵌 Core、DMG、ZIP、校验和、通用架构与 `codesign --verify --deep --strict` 检查。
7. 候选资产名称包含 `WB06`、短 SHA 与 Actions Run ID；工作流权限只有 `contents: read`，不存在发布步骤。

## 自动化证据

Actions 成功后应产生：

- `wb06-native-arm64-<sha>-<run>`：Apple Silicon 原生测试、截图与环境证据。
- `wb06-native-x86_64-<sha>-<run>`：Intel 原生测试、截图与环境证据。
- `wb06-payload-darwin-arm64-<sha>-<run>` 与 `...-amd64-...`：同 SHA 原生 Core 载荷。
- `wb06-macos-candidate-<sha>-<run>`：通用 DMG、ZIP、SHA-256、浅色/深色截图、架构与签名报告。

候选包默认使用 **ad-hoc 签名**；没有 Developer ID 密钥时，公证状态必须记录为 `not_run_no_developer_identity`，不得描述为已公证。

## 需要实体 Mac 的最终检查

以下项目不能由本线 CI 或手机节点冒充完成，合并/发布前需在隔离测试 Mac 上执行：

- 从旧版安装包升级后，现有 Core 配置、任务、对话与权限不被覆盖。
- 双击 DMG 安装、首次启动、登录项、系统重启后服务恢复。
- Finder、辅助功能、自动化与通知权限的真实 TCC 交互。
- 长时间 SSE 断线恢复、睡眠/唤醒、网络切换和大规模历史滚动的体验检查。
- 使用正式 Developer ID 的签名、公证与 Gatekeeper 验证。
- VoiceOver 全流程、键盘焦点顺序、缩放、多屏与高对比度检查。
- 安装、升级与回退候选只在隔离机执行；不得由 WB06 分支修改生产设备。

## 判定规则

- 任一原生架构测试失败、候选包源 SHA 不一致、架构不完整、签名报告缺失或截图未生成，均不得判为完成。
- 共享 Core 暂未提供的权限、插入回执或活动字段必须显示“不可用/待确认”，不得由 macOS 本地状态机伪造成功。
- Actions 的失败尝试、修复提交与最终成功 run 均写入交接文件，不能只保留最后一次结果。
