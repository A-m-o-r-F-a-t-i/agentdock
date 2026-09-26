# AgentDock Workbench CLI 命令矩阵

本表是 WB01 的命令面冻结清单。状态含义：

- **已实现**：调用真实 Core 或本机后端，包含参数校验和稳定退出码。
- **派生实现**：由多个真实读取/写入接口组合，不创建第二套状态机。
- **兼容保留**：WB01 未改写其原语义，仅确保不被新分发抢占。
- **部分实现**：真实后端只覆盖目标能力的一部分，限制已明确写出。
- **未提供**：当前 Core/本机后端没有该能力，或属于其他线路；CLI 不返回伪成功。

## 根命令与通用选项

| 命令/选项 | 状态 | 说明 |
|---|---|---|
| `agentdock --help`、`help [command]` | 已实现 | 根级帮助和文档入口 |
| `completion bash|zsh|fish` | 已实现 | 静态根命令补全 |
| `status` | 已实现 | `GET /internal/runtime/status` |
| `--endpoint`、`--token-file`、`--auth-mode`、`--profile` | 已实现 | token 不接受命令行明文值 |
| `--workspace`、`--project`、`--task`、`--thread`、`--conversation`、`--call` | 已实现 | 默认选择器，可被具体命令位置参数覆盖 |
| `--format human|json|jsonl`、`--json`、`--jsonl` | 已实现 | JSON/JSONL 错误同样结构化 |
| `--timeout`、`--quiet`、`--verbose`、`--no-color` | 已实现 | verbose 只输出脱敏元数据 |
| `--cursor`、`--all`、`--since`、`--until` | 未提供 | 后端当前使用 `offset`/`after`/`before`；不伪造通用游标 |
| 通用 `--dry-run`、`--yes`、`--no-interactive` | 未提供 | 后端没有统一预演协议；危险操作改用专用确认参数 |

## Workspace

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `workspace list` | 已实现 | 工作区注册表列表 |
| `workspace show <id>` | 已实现 | 工作区详情 |
| `workspace status <id>` | 已实现 | 同一详情契约，不伪造额外健康状态 |
| `workspace create` / `workspace register` | 已实现 | `workspace_manage register`；创建要求显式 `--root` |
| `workspace update <id>` | 已实现 | 同一 register 后端；强制 `--expected-revision`，只发送显式字段 |
| `workspace validate <id>` | 派生实现 | 使用真实 `workspace_manage resolve` 验证路径与边界 |
| `workspace resolve <id>` | 已实现 | `source`、`artifact`、`scratch`、`cache`、`external` |
| `workspace use` / `set-default` | 未提供 | 注册表没有默认工作区写入动作；可通过 Profile/环境变量选择 |
| `workspace unregister` / `delete` | 未提供 | 注册表没有安全注销动作 |
| `workspace export` | 未提供 | 可使用 `workspace show --json`，没有专用导出协议 |

## Task

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `task list` | 已实现 | 受管任务分页、视图、搜索、标签、选择集 |
| `task show <id>` | 已实现 | 任务详情 |
| `task create` | 已实现 | 复用 `task_manage create` |
| `task steps <id>` | 派生实现 | 从真实任务详情提取步骤 |
| `task calls <id>` | 已实现 | 任务调用分页 |
| `task follow <id>` | 已实现 | 调用 SSE，以 task_id 过滤 |
| `task checkpoint <id>` | 已实现 | 单步或批量原子检查点 |
| `task block`、`resume`、`cancel` | 已实现 | 复用任务生命周期动作，要求摘要 |
| `task final-review`、`complete` | 已实现 | 保留 pass/failed、verified/risks 语义 |
| `task pin`、`unpin`、`tags` | 已实现 | 复用批量管理 API；`tags` 当前替换完整非空标签集合 |
| `task archive`、`unarchive`、`trash`、`restore` | 已实现 | 只管理任务记录，不删除项目文件 |
| `task rename` | 已实现 | 批量管理 API 的单对象调用 |
| `task delete` / `purge` | 已实现 | 永久删除管理记录需 `--confirm-permanent`；项目文件不受影响 |
| `task export` | 已实现 | 任务详情写 stdout 或 0600 文件 |
| `task update` | 未提供 | Core 没有通用可变字段更新协议 |
| `task tag` / `untag` | 部分实现 | 可用 `task tags --set-tag ...` 替换集合；无原子增删单标签后端 |
| `task batch --filter ... --dry-run` | 未提供 | 当前后端支持批量 ID/选择集，但没有统一服务端预演协议 |

## Conversation

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `conversation list`、`show` | 已实现 | 分页、视图、搜索、标签、选择集 |
| `conversation tasks <id>` | 派生实现 | 从真实详情提取 `task_ids` |
| `conversation calls <id>` | 已实现 | 会话调用分页 |
| `conversation follow <id>` | 已实现 | 会话 SSE，支持 Last-Event-ID 恢复 |
| `conversation attach`、`detach` | 派生实现 | 先读取 `binding_revision`，再执行乐观并发写入 |
| `conversation link` | 已实现 | 关联历史任务，不改变当前任务绑定 |
| `conversation stop` / `terminate`、`resume` | 已实现 | 复用现有生命周期路由 |
| `conversation pin`、`unpin`、`tags` | 已实现 | 批量管理 API 的单对象调用 |
| `conversation archive`、`unarchive`、`trash`、`restore` | 已实现 | 管理会话记录，不删除项目文件 |
| `conversation rename` | 已实现 | 真实批量管理动作 |
| `conversation delete` / `purge` | 已实现 | 需要 `--confirm-permanent` |
| `conversation export` | 已实现 | 可选同时导出受限页大小的调用 |
| `conversation create`、`update` | 未提供 | Core 没有对应公共管理契约 |
| `conversation retry`、`reopen` | 未提供 | 不把 resume 或新会话伪装成重试 |
| 过滤式批量修改 | 未提供 | 当前 CLI 仅暴露单 ID；无统一 dry-run 协议 |

## Call

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `call list`、`show` | 已实现 | 支持 task/thread/conversation/status/view/search 等真实筛选 |
| `call events <id>` | 已实现 | 有界事件页 |
| `call logs <id>` | 已实现 | request/response/source；字节游标＋Unicode 标量预算 |
| `call wait <id>` | 已实现 | 成功返回 0；partial 返回 9；failed/cancelled/unknown 返回 1 |
| `call follow` / `watch` | 已实现 | 调用 SSE 和断线恢复 |
| `call cancel` / `stop` | 已实现 | 复用真实停止路由 |
| `call export` | 已实现 | 强制有界页，写 stdout 或 0600 文件 |
| `call archive`、`unarchive`、`isolate`、`unisolate` | 已实现 | 真实调用管理动作 |
| `call trash`、`restore`、`delete` / `purge` | 已实现 | 永久删除管理记录需确认 |
| `call retry` / `rerun` | 未提供 | Core 没有安全重放请求的公共契约 |

## Activity

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `activity list` | 已实现 | task/thread/conversation/workspace/project 和序号筛选 |
| `activity watch` | 已实现 | SSE、Last-Event-ID、退避和有限冷启动重试 |
| `activity export` | 已实现 | 复用有界调用导出；单页上限 200 |
| `activity show <event-id>` | 未提供 | 当前后端没有单事件详情路由 |
| 通用 kind/status/tool/device/since/until 筛选 | 部分实现 | 只透传当前后端真实支持的筛选，不在客户端伪过滤无限流 |

## Insertion

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `insertion list <conversation>` | 已实现 | 会话插入记录 |
| `insertion show <conversation> <id>` | 派生实现 | 从真实列表按 ID 定位 |
| `insertion send` / `enqueue` | 已实现 | `--text` 或有界 `--file`/stdin；幂等 submission_id |
| `insertion wait` | 已实现 | acknowledged 成功；不确定/过期/取消/目标变化返回非零 |
| `insertion cancel`、`retry` | 已实现 | 复用现有路由，不改写状态机 |
| `insertion follow` | 未提供 | 当前插入域没有专用事件流；可观察 activity/call 流 |

## Approval

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `approval list`、`history`、`show` | 已实现 | 真实审批页和详情 |
| `approval wait` | 已实现 | 轮询 pending，超时返回 10 |
| `approval approve` | 已实现 | 默认单次批准 |
| `approval approve --allow-workspace` | 已实现 | 后端支持时写入工作区允许 |
| `approval reject` | 已实现 | 真实拒绝路由 |
| `approval allow-rule` | 未提供 | 没有独立规则编辑后端；不能伪装成批准 |

## Permission

权限策略和审批体系由其领域实现负责；WB01 只提供已有契约的 CLI 薄封装。

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `permission show` / `effective` | 已实现 | 查询有效策略 |
| `permission set` | 已实现 | scope/mode；强制 `--expected-revision` |
| `permission set --mode full` | 已实现 | 额外强制 `--confirm-full` |
| `permission list`、`validate` | 未提供 | 当前后端没有独立列表/模拟验证协议 |
| `permission profile create|update|delete|set-default` | 未提供 | 属于权限线路的后续公共契约，不在 WB01 内复制状态机 |

## Skill 与 Plugin

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `skill list`、`show`、`status` | 已实现 | 在线读取；status 是可达/存在性检查 |
| `skill bootstrap --bundle ...` | 兼容保留 | 原离线命令原样保留 |
| `skill enable`、`disable`、`refresh` | 未提供 | Core 没有公共写入契约 |
| `plugin list`、`show`、`status` | 已实现 | 在线读取；`plugin list --home` 仍走旧离线命令 |
| `plugin validate`、`migrate --home`、`list --home` | 兼容保留 | 原离线命令原样保留 |
| `plugin test` | 未提供 | 显式返回退出码 8；不会用读取详情冒充测试通过 |
| `plugin reload` | 未提供 | Core 没有公共重载契约 |

## Config、Service、Doctor 与 Logs

| 命令 | 状态 | 真实后端/限制 |
|---|---|---|
| `config runtime-get`、`runtime-update`、`update` | 兼容保留 | 现有本机配置后端；WB01 未改变语义 |
| `config get|set|validate|export|import` | 未提供 | 目标通用 Profile 管理尚无统一后端；CLI Profile 当前通过受限 JSON 文件读取 |
| `service status|start|stop|restart` | 兼容保留 | 现有跨平台服务控制 |
| `service logs` | 已实现 | systemd/OpenRC/macOS/Windows 受控日志源 |
| `service autostart`、`task-start` | 兼容保留 | 原平台专用入口 |
| `service enable|disable|foreground` | 未提供 | enable/disable 可由 autostart 表达；foreground 没有统一入口 |
| `doctor` / `doctor report` | 已实现 | status、workspace、effective permission 的有界诊断汇总 |
| `logs tail`、`follow`、`export` | 已实现 | 最多 8 MiB、1..10000 行；导出文件权限 0600 |
| 完整诊断 bundle（配置＋活动＋日志＋系统信息） | 部分实现 | 当前提供 report 和日志导出；不把不完整集合标为完整 bundle |

## 兼容根命令

`version`、`--version`、`update`、`install`、`uninstall`、`nexus`、`tunnel` 以及无子命令服务器启动均为**兼容保留**。WB01 不改其发布、安装或生产运行语义。

## 明确不做的伪能力

以下行为不会通过“空对象＋退出码 0”实现：

- 插件主动测试/重载；
- 调用重试/重放；
- Workspace 默认值写入或注销；
- 权限 Profile CRUD；
- 过滤式批量操作的客户端假预演；
- 插入 `delivery_unknown` 被当作 acknowledged；
- 失败或 partial 的调用等待被当作成功。
