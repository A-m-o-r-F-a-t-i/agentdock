# Windows Desktop 配置与生效

适用于官方 Windows Desktop / Setup 安装。不要把 macOS `agentdock.env`、Linux EnvironmentFile 或 Docker Compose 当成 Windows Desktop 的配置事实源。

## 运行目录与配置事实源

官方安装器默认把二进制放在：

```text
%LOCALAPPDATA%\AgentDock\bin
```

因此默认 runtime root 是：

```text
%LOCALAPPDATA%\AgentDock
```

安装器允许改变安装目录，所以操作前应优先读取当前 `runtime.json`、控制面板显示的配置目录，或实际启动参数中的 `--runtime-root`，不要只依赖默认值。

Windows Desktop 的运行配置由多部分组成：

- `control-panel-settings.json`：端口、日志、MCP Apps UI、浏览器、ACP 以及 `runtime_options` 运行参数；
- `runtime.json`：安装位置、Core、Tray、Tunnel 与启动方式等运行清单；
- `auth-token.dpapi`、`oauth-password.dpapi`、`oauth-token-secret.dpapi`、`cloudflared-token.dpapi`：受当前 Windows 用户保护的秘密；
- `server-url.txt`、Tunnel 状态文件等：公网/OAuth/Tunnel 运行状态。

Core 启动时会读取这些状态并生成实际进程环境。

## 推荐修改方式

优先使用 AgentDock Windows 控制面板。保存设置时，控制面板会调用当前 Core 的结构化配置入口；该入口会校验新设置、保存配置、重启 Core/Tunnel，并在失败时恢复原文件后尝试恢复旧运行状态。

如果必须通过 CLI 修改控制面板覆盖的普通设置，应使用当前安装目录里的 `agentdock.exe config update --runtime-root <实际目录> ...`，不要手工拼写另一份 JSON 并假定所有字段都会被读取。

## 手工修改边界

- 不要把 Bearer Token、OAuth 密码、OAuth 签名密钥或 Tunnel Token 写进 `control-panel-settings.json`。
- 不要手工解密、复制或跨用户迁移 DPAPI 文件；它们绑定 Windows 用户保护上下文。
- 不要只修改 `runtime.json` 来改变普通设置；它主要描述运行时布局与启动状态。
- 如果手工改了 `control-panel-settings.json`，旧 Core 不会自动重新读取，仍需重启实际 Core。
- 提权模式可能由 Scheduled Task 启动 Core；标准模式可能由普通后台进程启动。不要用“杀掉一个同名进程”代替官方 service/control-panel 操作。

## 生效与验证

优先通过控制面板执行保存/重启。需要 CLI 时，使用当前实际 runtime root，例如默认安装可表示为：

```powershell
agentdock service restart --runtime-root "$env:LOCALAPPDATA\AgentDock"
agentdock service status --runtime-root "$env:LOCALAPPDATA\AgentDock"
```

如果安装目录不是默认值，把路径替换成 `runtime.json` 所在目录。

验证至少包括：

1. `service status` 或控制面板显示 Core running/healthy；
2. 当前端口的 `/healthz` 成功；
3. 本次设置对应的功能真的变化；
4. 如果配置更新返回回滚错误，检查当前文件和 Core 状态是否已恢复，不要继续覆盖 DPAPI 或 runtime 文件。

## 运行配置页面

“运行配置”页集中管理以下非敏感选项，数据目录、设置文件和运行清单位置只读展示：

| 选项 | 规则 |
| --- | --- |
| 默认全局工作区 | 绝对目录路径；不能指向文件。保存后同步运行清单。 |
| AGENTS.md 自动加载 | 默认开启；控制全局与工作区规则发现。 |
| 额外指令文件 | 可留空。显式文件须存在、非空、UTF-8，大小不超过 64 KiB。 |
| 浏览器程序 | 留空自动发现；显式路径须为现有普通文件。 |
| 可信代理 | 每行一个 CIDR，只填写实际控制的反向代理网段。 |
| 命令环境变量引用 | JSON 的键和值均为变量名；不能填写凭据值。 |
| ACP 并发提示数 | 1—8。 |
| ACP 交互超时 | 1000—3600000 毫秒。 |

`config runtime-get --runtime-root <实际目录>` 返回已保存值和配置位置，Core 停止时也可使用。`config runtime-update --runtime-root <实际目录> --options-json <JSON>` 保存这些选项，并保留其他页面的设置。GUI 保存前校验，CLI 再执行同一组校验，失败不会先停止当前实例。

安装或重新安装时，Skill 自举只校验存储目录，不读取浏览器、ACP、监听端口或额外指令文件等残余运行参数。Windows Desktop 启动时由明确的运行清单和设置重新生成受管环境；不把旧环境中的失效额外指令路径当作新安装的必需文件。已经显式保存在 `runtime_options` 中的文件路径仍会严格检查，失效后可通过本页清空或修正。
