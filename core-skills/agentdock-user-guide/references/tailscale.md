# Windows Tailscale Funnel

适用于提供原生 Tailscale provider 的 Windows Desktop。AgentDock 不捆绑或静默安装 Tailscale，不代替官方客户端登录，不保存 Auth Key、节点私钥或账户 Cookie。原生管理要求当前客户端支持 `status --json`、`funnel status --json`、`--bg` 和精确 `--set-path`；未知状态结构会拒绝写入。

## 配置

在官方 Tailscale 客户端完成登录和设备授权，再打开 AgentDock“公网访问”页，选择“Tailscale Funnel”并点击“检测 Tailscale”。确认设备名、只读域名、密钥到期时间和本机目标，点击“启用 Funnel”。需要授权时通过按钮打开官方管理页处理，不索取或保存 Auth Key。

CLI 使用真实 `runtime.json` 所在目录：

```powershell
agentdock tunnel status --runtime-root <运行目录> --provider tailscale
agentdock tunnel configure --runtime-root <运行目录> --provider tailscale --mode funnel
agentdock tunnel status --runtime-root <运行目录>
agentdock tunnel stop --runtime-root <运行目录>
agentdock tunnel start --runtime-root <运行目录>
```

仅当客户端在非标准位置时提供绝对 `--tailscale-binary` 路径。Tailscale 配置不接受 `--server-url` 或 `--token-file`。客户端入口使用检测得到的 `https://<device>.<tailnet>.ts.net/mcp`，认证继续使用 AgentDock 的 Bearer Token 或 OAuth。

## Origin 与所有权

根路径转发至 AgentDock 的完整本机 HTTP Origin，使 `/mcp`、`/context`、OAuth 元数据与授权路径、服务器描述和签名 Artifact 地址可用。业务接口仍受现有认证保护，公开 Artifact 仍检查签名和过期时间。

启用前核对当前设备 ID、DNS、在线状态、Funnel/HTTPS443 权限和现有映射。已有相同根目标可在显式配置时接管。临时 `/mcp` 映射只有精确指向当前 AgentDock `/mcp` 时才迁移。其他根目标、覆盖协议的路径和会被顺带公开的私有 Serve 路径均拒绝覆盖。

停止、模式切换和卸载只删除所有权记录仍能匹配的根映射及待迁移 `/mcp` 映射，始终使用显式路径。禁止 `tailscale funnel reset`、`tailscale serve reset`、`tailscale down`、`tailscale logout`，不停止 Tailscale 服务，不删除设备或卸载客户端。其他路径和端口保持不变。

## 验证和恢复

配置依次读取实际状态、写入待验证所有权、停止 AgentDock Cloudflare 入口、配置映射、更新 Origin、重启 Core，并检查健康、401 challenge、OAuth Metadata、MCP 描述和认证 initialize。成功后才提交 provider 和就绪状态。状态查询只读，`ready` 结合已完成验证记录、当前映射和本机健康状态；公网网络发生变化时还需实际访问检查。

命令失败或超时后先读取状态，禁止盲目重复写入。失败恢复自身文件、旧映射及原运行状态。其他操作修改了同一路径时保留该配置并报告冲突。无法确认清理结果时保留 `pending` 所有权，修复前不要删除该文件。设备或 DNS 改变时停止自动接管，核对官方客户端及旧映射后重新配置。

Tailscale Windows 服务与 `--bg` 配置负责开机恢复，不创建 AgentDock Tailscale 自启动项。设备密钥到期前会提示，AgentDock 不自动关闭到期策略。安装、更新和 repair 保留 provider、Origin、映射所有权和监听端口，改端口应通过控制面板，停用的 Funnel 不因普通设置保存而自动启用。

旧 1.0.1 Core 能读取新增字段对应的 schema 1 兼容投影，并从保留的 `server-url.txt` 使用外部 Origin，但旧控制面板不能管理 Funnel。旧安装器不理解新增字段，降级安装前应在新版控制面板停用或切换 Funnel，保留配置备份。
