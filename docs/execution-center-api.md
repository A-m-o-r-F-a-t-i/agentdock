# 1.1.2 执行中心接口

## 工具与接入元数据

普通工具的 JSON Schema 只声明业务参数。`conversation_id`、`call_id`、`parent_call_id`、`binding_quality` 和绑定修订均由服务端管理，不能放进工具 arguments。读取、搜索、列目录和未绑定任务的调用同样产生 Call。

MCP 适配器使用 SDK 请求的 `Params.Meta["openai/session"]`。认证主体来自实际接入认证过程，不读取模型填写的主体或账户字段。Meta 的会话相关性不等于权限。没有宿主元数据时不要求模型补 ID。

输出中的技术 ID 供界面查询、审核和故障定位使用。`binding_quality` 取 `host_metadata`、`connection_fallback`、`unattributed`。条件不满足时返回的 `binding_warning` 说明为什么没有建立后续默认绑定。

## 一次性任务选择

`task_manage(create/resume/set_current)` 成功后更新当前 ConversationState。`unbind` 解除当前任务；`thread_switch` 更新任务分支；完成或取消任务清理对应当前绑定。`get/list/thread_get` 不改变默认执行状态。

普通工具已有的 task_id/thread_id/workspace_id 是可选高级覆盖，与当前非空绑定不一致时返回 `EXECUTION_BINDING_CONFLICT`。无任务调用保留空 TaskID。

## 本地管理接口

执行中心 HTTP 路由要求已认证的直接本机访问，拒绝公网 Host、转发标头和重定向场景。下面的 ConversationID 只是查询或管理目标，不是该 HTTP 请求的来源身份。

| 方法及路径（前缀 `/internal/runtime`） | 用途 |
| --- | --- |
| `GET /execution/overview` | 调用统计和策略修订。 |
| `GET /conversations` | 对话分页，支持 search、workspace_id、tag、view、offset、limit。未知来源以无 ID 的导航项返回。 |
| `GET /conversations/{id}` | 对话元数据、当前 ConversationState 和有效权限。 |
| `POST /conversations/{id}/current-task` | 显式选择后续默认任务。body 为 task_id、可选 task_thread_id、必需 binding_revision。task_id 为空表示解绑。 |
| `POST /conversations/{id}/link-task` | 管理关系关联，不改写历史 Call 或当前执行绑定。 |
| `GET /execution/tasks` | 任务管理列表和分类分页。 |
| `GET /calls` | Call 投影分页和过滤。 |
| `GET /calls/{id}` | 单个 Call 的完整详情及有界输出。 |
| `GET /calls/stream` | 基于序列号和 Last-Event-ID 的实时更新。 |
| `GET /tasks/{id}/activity?milestones=true` | 仅查询任务进度里程碑。 |
| `GET /approvals`、`GET /approvals/{id}` | 审批列表和固定请求详情。 |
| `POST /approvals/{id}/approve`、`POST /approvals/{id}/reject` | 处理绑定到具体 CallID 的审批。 |
| `GET /permissions/effective`、`POST /permissions` | 查看及显式更新权限。更新要求 expected_revision；完全权限额外要求 confirm_full。 |

绑定修订冲突返回 HTTP 409。客户端应重新读取当前状态并显示冲突，不能用旧修订无限重试。更新成功只影响以后进入执行器的调用。

## Call 查询与增量

`/calls` 支持 conversation_id、unattributed、task_id、thread_id、workspace_id、parent_call_id、top_level、search、status、after、before、limit、include_output、include_diagnostic。未识别来源用 `unattributed=true`，不能构造 `conversation_id=unknown`。

分页记录包含 call_id、parent_call_id、conversation_id、task_id、thread_id/task_thread_id、workspace_id、tool_name、display_title、status、started_at、completed_at、elapsed_ms、parameter_summary、output_summary、error_summary、approval_id、visibility、binding_quality 和序列号。既有兼容字段仍保留。

普通列表默认排除 diagnostic；父子树通过 parent_call_id 展开。流式输出和审批变更更新同一 Call，不按事件条数新建卡片。`retry_of_call_id` 仅表示真正的新调用与原失败调用的关系，不改变 Conversation 归属。

SSE 客户端按序列号幂等合并。断线后查询补齐，遇到 gap/history_incomplete 时显示缺口，不推断未观察到的完成结果。查询刷新不构成重新执行指令。

## 固定审批与错误

审批服务保存准备时的固定业务参数和 ExecutionScope。客户端不能通过 approve body 替换命令、文件路径或第三方工具参数。重复审批只返回既有决定，不再次派发。

策略、工作区规则、解析路径或 MCP 目标配置变化会使原审批过期。正常的过期响应可以是 HTTP 200 且 `dispatched=false`，必须检查 approval.status 和实际派发字段，不能仅凭 HTTP 成功认定执行成功。

参数校验失败、权限禁止、待审批、工具启动失败和执行失败均可查询。输出以实际状态、退出码和可验证结果判定，不能把工具传输成功当作命令成功。

## 旧记录

旧记录的 read_only_legacy=true，ConversationID 为空。可靠的 SessionID/开始事件/顺序/任务归属允许只读聚合，其他旧事件独立呈现并标记不完整。旧记录不提供直接重放、停止或审批按钮。新投影不会复制或删除原始 Activity 日志。
