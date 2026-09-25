# AgentDock Workbench CLI

`agentdock` 现在既保留原有的本机安装、更新、服务和离线 Skill/Plugin 命令，也可以作为 AgentDock Core 的受控管理客户端。CLI 不维护第二套任务、会话、调用、审批、权限或插入状态机；在线命令统一调用 Core 已有的 `/internal/runtime/*` 接口。

完整实现状态见 [命令矩阵](command-matrix.md)，共享传输契约见 [`api/workbench/v1/contract.json`](../../api/workbench/v1/contract.json)。

## 快速开始

```bash
# Core 默认地址为 http://127.0.0.1:${AGENTDOCK_PORT:-8765}
export AGENTDOCK_TOKEN_FILE="$HOME/.agentdock/token"

agentdock status --json
agentdock workspace list --jsonl
agentdock task list --view active --limit 50 --json
agentdock activity watch --after 120 --jsonl
```

令牌只从 `--token-file`、`AGENTDOCK_TOKEN_FILE` 或兼容环境变量 `AGENTDOCK_AUTH_TOKEN` 读取。CLI **不提供明文 `--token` 参数**，不会在普通输出或 `--verbose` 元数据中回显令牌。

## 连接与配置优先级

在线命令使用以下优先级：

1. 当前命令的显式参数；
2. 环境变量；
3. 选中的本地 CLI Profile；
4. 内置默认值。

默认配置文件为 `$AGENTDOCK_HOME/cli.json`，未设置 `AGENTDOCK_HOME` 时为 `~/.agentdock/cli.json`。也可以用 `AGENTDOCK_CLI_CONFIG` 指定文件。

```json
{
  "default_profile": "phone",
  "profiles": {
    "phone": {
      "endpoint": "https://agentdock.example.invalid",
      "token_file": "~/.agentdock/token",
      "auth_mode": "oauth",
      "workspace_id": "wsp_example",
      "project": "agentdock",
      "output_format": "json",
      "timeout": "15s"
    }
  }
}
```

常用环境变量：

| 变量 | 用途 |
|---|---|
| `AGENTDOCK_ENDPOINT` | Core 的 HTTP(S) 根地址 |
| `AGENTDOCK_TOKEN_FILE` | Bearer/OAuth token 文件 |
| `AGENTDOCK_AUTH_MODE` | `auto`、`bearer`、`oauth` 或 `none` |
| `AGENTDOCK_PROFILE` | CLI Profile 名称 |
| `AGENTDOCK_WORKSPACE_ID` | 默认工作区 |
| `AGENTDOCK_TASK_ID` | 默认任务 |
| `AGENTDOCK_CONVERSATION_ID` | 默认会话 |
| `AGENTDOCK_CALL_ID` | 默认调用 |
| `AGENTDOCK_OUTPUT_FORMAT` | `human`、`json` 或 `jsonl` |
| `AGENTDOCK_CLI_TIMEOUT` | 普通 HTTP 请求超时 |
| `NO_COLOR` | 禁用颜色；当前稳定输出默认不依赖颜色 |

## 输出契约

- `--format human`：稳定、缩进后的结构化文本，适合人工检查。
- `--format json` 或 `--json`：一个 JSON 值。
- `--format jsonl` 或 `--jsonl`：列表中的每条记录或流中的每个事件占一行。
- `--quiet`：成功时不输出；错误仍保留退出码。
- `--verbose`：只向 `stderr` 输出脱敏后的请求方法、路径和重连信息。

机器格式错误使用同一包络：

```json
{"ok":false,"code":"UNAUTHORIZED","error":"token required","exit_code":4}
```

稳定退出码：

| 码 | 含义 |
|---:|---|
| 0 | 成功 |
| 1 | 操作执行失败或对象以失败终态结束 |
| 2 | 参数、输入或请求格式无效 |
| 3 | 资源不存在 |
| 4 | 未认证或权限拒绝 |
| 5 | 网络、Core 或本机服务故障 |
| 6 | 操作正在等待审批 |
| 7 | 并发冲突、前置条件不满足或不确定终态 |
| 8 | 当前后端没有该能力；没有执行伪操作 |
| 9 | 部分成功或部分失败 |
| 10 | 等待或请求超时 |
| 130 | 用户中断或父上下文取消 |

## 分页、游标与载荷

列表命令使用后端原生的 `limit`、`offset`、`after`、`before` 和 `next_seq`；CLI 不偷偷抓取所有页面。调用载荷命令示例：

```bash
agentdock call logs call_123 \
  --kind response \
  --offset 0 \
  --limit-chars 100000 \
  --json
```

`offset` 是 UTF-8 **字节偏移**，`limit-chars` 是 Unicode 标量预算。响应返回下一字节游标，避免多字节字符被截断或重复读取。

## 流式订阅

`activity watch`、`call follow` 和 `conversation follow` 使用 SSE：

```bash
agentdock activity watch --after 42 --jsonl
agentdock call follow --task tsk_example --after 42 --jsonl
agentdock conversation follow conv_example --after 42 --jsonl
```

CLI 保留事件 `id`、`event` 和 `data`，重连时同时发送 `Last-Event-ID` 并保留 `after`。网络错误、HTTP 429 和 5xx 使用 250 ms 到 5 s 的退避；连续八次无法建立可用事件流后以服务故障退出。认证失败、无效内容类型和输出失败不会无限重试。Ctrl+C 返回 130，显式等待超时返回 10。

## 安全写入

- Core 的管理写入仍要求认证，并继续执行其“直连回环”检查；代理头不能伪造本机来源。
- `workspace update` 必须提供 `--expected-revision`，且只发送显式修改的字段。
- `conversation attach/detach` 先读取当前 `binding_revision`，再执行乐观并发更新。
- `permission set` 必须提供 `--expected-revision`；设置 `full` 还必须提供 `--confirm-full`。
- 永久删除任务、会话或调用管理记录必须提供 `--confirm-permanent`。这些命令不删除项目文件。
- 插入发送自动生成幂等 `submission_id`，也可以显式传入。`delivery_unknown`、`expired`、`cancelled` 和 `target_changed` 会原样输出并返回非零，不会伪装成已送达。
- 批量结果为 `partial` 时会先完整输出逐项结果，再返回退出码 9。

## 常用示例

```bash
# 创建任务
agentdock task create \
  --title "补全 Linux CLI" \
  --goal "通过候选 Actions" \
  --condition "测试通过" \
  --task-step S1=实现 \
  --task-step S2=验证 \
  --workspace wsp_example \
  --json

# 原子检查点
agentdock task checkpoint tsk_example \
  --completed-step S1 \
  --current-step S2 \
  --summary "实现完成，进入 Actions" \
  --json

# 安全更新工作区
agentdock workspace update wsp_example \
  --expected-revision 7 \
  --name "AgentDock Workbench" \
  --json

# 绑定会话；CLI 自动读取 binding_revision
agentdock conversation attach conv_example \
  --task tsk_example \
  --task-thread main \
  --json

# 发送并等待插入回执
agentdock insertion send conv_example --text "请先执行 Actions" --json
agentdock insertion wait conv_example ins_example --wait-timeout 5m --json

# 完全权限需要双重显式确认
agentdock permission set \
  --scope workspace \
  --scope-id wsp_example \
  --mode full \
  --expected-revision 4 \
  --confirm-full \
  --json
```

## 服务日志与补全

```bash
agentdock service logs --runtime-root /opt/agentdock/current --lines 200
agentdock logs follow --runtime-root /opt/agentdock/current
agentdock logs export --runtime-root /opt/agentdock/current --output agentdock.log

agentdock completion bash
agentdock completion zsh
agentdock completion fish
```

Linux systemd 使用 `journalctl`；OpenRC、macOS 和 Windows 只读取安装清单推导出的受控日志路径。单次尾读最多 8 MiB、1 到 10000 行，并支持日志轮转和截断后的继续跟随。

## 兼容性

以下旧入口保持原语义，不会被在线控制命令抢占：

- `agentdock --version`、`agentdock version [--json]`；
- `agentdock update ...`；
- `agentdock skill bootstrap --bundle ...`；
- `agentdock plugin validate ...`、`plugin migrate --home ...`、`plugin list --home ...`；
- `agentdock install`、`uninstall`、`nexus`、`tunnel` 和无子命令的服务器启动参数。

没有真实后端的目标命令不会返回空成功对象。它们在命令矩阵中标为“未提供”，已经存在但无法执行的显式入口（例如 `plugin test`）返回退出码 8。
