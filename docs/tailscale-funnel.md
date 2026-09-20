# Tailscale Funnel / Windows 原生接入

AgentDock 1.1.0 在本 fork 的 Windows Desktop 增加原生 Tailscale Funnel。域名来自已登录设备的 `.ts.net` DNS 名，无需自有域名或 Cloudflare Tunnel Token。Tailscale 客户端需独立安装和登录，AgentDock 不收集 Auth Key，不改变账户、设备或密钥到期策略。

## 使用

1. 安装并登录官方 Tailscale 客户端，确保当前设备在线且具有 HTTPS443 Funnel 权限。
2. 打开 AgentDock 控制面板“公网访问”，选择“Tailscale Funnel”，点击“检测 Tailscale”。
3. 检查设备名、只读域名、到期时间和本机目标，点击“启用 Funnel”。有其他映射冲突时先核对目标服务，不直接清空 Tailscale 配置。
4. 将显示的公网 MCP 地址填入客户端，继续使用 AgentDock Bearer Token 或 OAuth 认证。

CLI、冲突判定与恢复规则见随安装包提供的 [Tailscale 用户指南](../core-skills/agentdock-user-guide/references/tailscale.md)。未安装、未登录、缺少能力、密钥到期、未知 JSON 或超时均保留明确诊断。

```powershell
agentdock tunnel status --runtime-root <运行目录> --provider tailscale
agentdock tunnel configure --runtime-root <运行目录> --provider tailscale --mode funnel
agentdock tunnel status --runtime-root <运行目录>
```

Tailscale 模式不接受 `--server-url` 或 `--token-file`。`--tailscale-binary` 只用于非标准位置的客户端绝对路径。旧 `--mode none|quick|named` 调用保持兼容。

## 协议与安全

完整 Origin 转发保留 `/mcp`、`/context`、OAuth 元数据和授权路径、MCP 描述以及签名 Artifact 访问。未认证管理接口继续返回401，不改变 Bearer/OAuth 的校验方式。公开 Artifact 仍校验签名和过期时间。

Funnel 的公开开关按 HTTPS 端口生效，因此443上存在其他私有 Serve 路径时拒绝启用。已有不同根目标、前台 Serve/Funnel、TCP 映射或覆盖 AgentDock 协议的路径同样拒绝覆盖。停止始终显式指定 `/`，保留其他路径和端口，禁止任何全局 reset/down/logout 操作。

所有权文件只记录当前运行目录拥有的映射及恢复状态。命令退出后重新读取状态，失败恢复旧文件和映射；当前路径已被其他操作更改时保留它并报告冲突。GUI 仅在完成健康、OAuth 和认证 MCP 验证后显示就绪。

## 安装和版本兼容

升级和 repair 保留 `public_access_provider/mode/url`、客户端路径、`server-url.txt` 和映射所有权。安装期间保留已配置端口，端口修改放在控制面板执行。卸载仅清理仍匹配的 AgentDock 映射，不卸载 Tailscale。无法确认映射状态时保留恢复信息并中止对应清理。

manifest 继续使用 schema1，Tailscale 的旧字段投影为 `tunnel_mode=none`、空 `public_url`。旧 Core 可读取此投影；旧安装器和控制面板无法管理或保留全部 Tailscale 数据，降级安装前应先通过新版控制面板切换到仅本机或 Cloudflare。

## 测试范围

Go 测试覆盖兼容模型、严格 JSON 解析、权限/所有权、同端口私有路径冲突、幂等启停、临时 `/mcp` 迁移、部分成功/超时、并发变化保留、文件和服务回滚、安装保留，以及 Windows 原生命令的输出和执行边界。

`TestTailscaleRealCoreAndFakeCLILifecycle` 使用真实独立 Core、真实 DPAPI 和独立 Fake Tailscale 进程，模拟 Funnel TLS 终止后验证实际 HTTP/OAuth/MCP、受保护上下文与管理接口、签名下载和模式切换，不接触运行中的用户 Funnel。当前实机客户端1.102.4另行验证了指定根路径删除保留同端口兄弟路径。

## English summary

Windows Desktop supports native Tailscale Funnel alongside local-only and Cloudflare Quick/Named modes. Install and sign in to the official Tailscale client first. The device DNS name is read-only; no Tunnel Token or Tailscale auth key is accepted or stored. The full AgentDock origin is forwarded, while existing Bearer/OAuth and signed-artifact checks remain enabled.

Only matching, owned paths are removed. Private sibling Serve paths, conflicting roots, changed device identities, and unknown schemas fail closed. Updates preserve provider state and the existing port; the control panel performs port changes. The previous schema1/none projection remains readable by old Core versions, but old installers cannot manage the new fields. This fork publishes Windows assets only.

Official CLI semantics: [Tailscale Funnel reference](https://tailscale.com/docs/reference/tailscale-cli/funnel).
