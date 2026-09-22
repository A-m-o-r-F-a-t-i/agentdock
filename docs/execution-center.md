# AgentDock 1.1.2：任务与执行中心

## 状态与适用范围

本次修订在现有任务、分支和工作树上实施，保留 1.1.1 修复及已有任务管理、审批、回收站和 WPF 功能。正式发布条件记录在 `docs/releases/v1.1.2-acceptance.json`。候选构建、模拟客户端测试或保留配置文件，均不自动满足真实连接和权限回退验收。

## 核心对象

| 对象 | 职责与关系 |
| --- | --- |
| Conversation | 经可信接入元数据解析的一次宿主对话。可以不关联任务，也可以接续多个任务。 |
| Task | 长期目标、步骤、验收和生命周期。可以由多个 Conversation 接续。 |
| Call | 一次实际工具调用，具有服务端生成的独立 CallID。Task、TaskThread、Workspace 为可选关联。 |
| Command Session | 长命令对应的进程会话。保存启动 Call 的归属快照，后续查看和停止不会改变原归属。 |
| Activity Event | Call 开始、输出、审批和结束等追加事件。查询投影按 CallID 聚合，两个主视图读取同一记录。 |

任务分支字段 `thread_id` 保留原语义。Call 输出同时提供 `task_thread_id`，它与宿主 ConversationID 独立。内部子调用生成新 CallID，通过 ParentCallID 继承父调用的对话、任务、线程和工作区。

## 接入层自动归属

`internal/activity/conversation.go` 提供 ConversationResolver/ConversationRegistry。当前 Go MCP SDK 的实际请求类型是 `ServerRequest[*CallToolParamsRaw]`，工具请求元数据来自 `request.Params.Meta`，适配器读取其中的 `openai/session`。该相关性字段不授予任何权限。

映射键包含 provider、认证主体范围、接入命名空间、绑定来源和宿主会话标识。持久化映射键经过摘要处理，外部原始会话标识不作为数据库主键，也不显示在界面。首次并发访问通过进程锁和文件锁共享同一个本地 `conv_...` 标识。

解析规则：

1. 接入层具有可信宿主会话元数据时，创建或复用本地 Conversation，BindingQuality 为 `host_metadata`。
2. 仅在适配器明确保证连接与对话一一对应时，允许使用连接标识作为降级键，并标记 `connection_fallback`。
3. 其他情况保留空 ConversationID，标记 `unattributed`。界面中的“来源未识别”仅为导航分组，不创建虚假的全局 Conversation。

HTTP MCP 入口不把 `Mcp-Session-Id` 无条件当作对话。重连能否延续 Conversation 取决于可信宿主标识是否保持一致。普通工具禁止输入 `conversation_id`，输入此字段会生成可查看的参数失败 Call。`agentdock_context` 不要求模型复用其返回标识。接口查询和本地管理界面仍可以使用服务端生成的技术标识选择对象。

## 不可变执行上下文

ExecutionScope 在统一入口解析并以值形式写入 `context.Context`，包含 ConversationID、TaskID、ThreadID、WorkspaceID、CallID、ParentCallID、Source、BindingQuality 和 BindingRevision。工具、审批、Activity Recorder 和命令 Session 从上下文读取，不各自解析对话参数。

参数验证、权限拒绝和启动失败同样保留 Call。执行器完成准入后，运行和待审批请求保存固定参数、固定目标和固定归属。UI 切换、另一请求更新当前任务、审批人选择其他列表项均不重写这些快照。

## 服务端任务继承

ConversationState 持久化 `active_task_id`、`active_task_thread_id`、`workspace_id`、`binding_revision` 和 `updated_at`。绑定修订使用比较交换，旧的并行管理请求不会静默覆盖较新的绑定状态。

| 操作 | 对后续调用的影响 |
| --- | --- |
| `task_manage(create)` | 成功后绑定新任务。创建操作自身保留进入执行器时的归属。 |
| `task_manage(resume/set_current)` | 一次性选择当前任务。进行中任务可以被其他对话接续。 |
| `task_manage(thread_switch)` | 成功后更新当前任务分支。 |
| `task_manage(get/thread_get/list)` 或 UI 浏览 | 只改变查看对象，不改变绑定。 |
| `complete/cancel/unbind` | 清理对应当前任务与线程，后续临时调用无 TaskID。 |
| 明确工作区上下文 | 更新该对话的工作区默认值，不改变其他对话。 |

已有普通执行工具上的 task_id/thread_id/workspace_id 仅为可选高级覆盖。正常调用不需要填写；与非空当前绑定冲突时拒绝，提示使用一次性管理动作。未识别来源的调用不猜测可继承的状态，任务操作会明确说明该限制。

## 执行记录与界面

Runtime 业务工具默认进入统一观测入口，不使用容易遗漏新增工具的 observedTool 白名单。协议发现、健康检查、UI 自动刷新和连接保活按明确规则标为诊断或不进入业务列表。诊断 Call 可通过专门查询查看。

调用投影保留动作标题、参数摘要、开始/完成时间、状态、耗时、结果、错误、审批、工作区、父子关系及归属质量。参数摘要仅选择有意义的操作字段，文件正文和任意嵌套第三方数据不直接写入审计。输出在持久化前脱敏、截断，并保留缺口提示。

命令开始、输出和完成更新同一 Call。动态 MCP 的外层卡片以实际目标动作为标题，转发器和实际远端工具通过父子关系展开。真实重试创建新 Call 并链接 retry_of；相同命令文本不会被去重。

WPF 两个主视图读取同一 Call 投影：

- 对话视图显示全部实际调用。存在当前任务时展示名称、状态、完成步骤数、当前步骤、下一动作和完整任务入口；没有任务仅隐藏进度卡。
- 任务视图显示目标、步骤、验收、分支、跨对话调用和文件/测试输出。分支选择只影响查看。
- checkpoint 等任务进度事件进入独立里程碑区域，不与输出 delta 混排。

保留右键菜单、冻结选择的批量操作、标签/工作区分类、归档、回收站、待处理视图、权限界面、搜索和重连。列表分页、容器回收和有界输出避免一次加载全部历史。界面刷新和 SSE 重放不发起工具重试。

## 权限与管理边界

默认按规则审批，提供只读检查及经本地确认的全局/工作区完全权限。完全权限仍执行显式禁止规则，ConversationID 不是权限凭据。第三方工具的只读注释不作为可信判定。

审批绑定具体 CallID，固定原始业务参数、解析后的工作区目标和动态 MCP 配置摘要。批准前重新校验策略、工作区规则和 MCP 目标，变化时将原请求过期并返回 `dispatched=false`。实际动态 MCP 派发也在目标配置锁内重验。重复批准、刷新或断线不会重复执行。服务重启后无法恢复的固定请求过期，未知副作用不自动重放。

直接本机且已认证的控制接口提供显式“设为当前任务”动作。读取或选中列表不能模拟宿主身份。管理动作的目标 Conversation 与该管理请求自身的来源分开记录。

任务删除、归档、恢复和到期清理只操作管理数据。运行或待审批的关联 Call 必须先处理。源码、项目目录、产物和独立执行审计不随任务删除。

## 旧数据与回滚

旧任务和 Activity 文件原位读取，新增对话和权限信息独立存储。旧 Activity 没有宿主身份时保留原 TaskID/ThreadID，显示来源未知，不按时间或工作区猜测 Conversation。

旧命令仅在观察到明确 started、SessionID 一致、任务/线程/工作区一致且事件顺序可靠时聚合。SessionID 后续复用会生成独立旧 Call。缺少开始、交叠开始、归属冲突或顺序异常的事件保留为只读旧记录并标注不完整。任务里程碑独立保留。损坏或超长日志行按有界读取跳过并报告缺口，后续健康记录继续加载；读取迁移不改写原始日志。

安装事务失败回退与“已经使用 1.1.2 后降级到 1.1.1”分别验收。旧执行器不包含新审批引擎，保留 policy.json 仅保留用户意图。未验证旧执行器停止远程写入或满足用户重新确认之前，不能将这种降级计为安全权限回滚。正式发布门槛对此保持关闭。

## 验证与发布

`internal/httpx/execution_scope_integration_test.go` 通过真实 SDK/HTTP 序列化构造宿主元数据，覆盖两组并行对话各 20 次无业务 ID 调用、重连、缺少元数据和本地显式绑定。它不代表当前 ChatGPT 网页连接实际透传已验收。

后端大规模夹具与实际 WPF 窗口的大列表 HTTP 夹具分别记录条件和结果，不将两者相加称为真实数据库端到端首屏时间。安装验证必须使用隔离脚本，检查生产配置前后不变。

候选构建使用 `build-windows-release.ps1 -Candidate`，报告注明候选通道、源码提交与工作树是否有未提交修改。正式 1.1.2 构建和两条发布流程调用 `go run ./tools/release verify-acceptance docs/releases/v1.1.2-acceptance.json`。任何必要检查缺失、无证据或 release_ready=false 时拒绝发布。

## 1.1.4 展示与观测补充

1.1.4 不改变 Conversation、Task、Call 与 Command Session 的身份关系，而是在同一根调用投影上补充可空的阶段测量和文件编辑事实。

- 调用列表提供紧凑／详细两种显示密度，始终展示真实工具名、真实状态和来源标签；旧记录缺少耗时时显示“未记录”，不再伪造 `0.000 s`。
- 根 RPC 的接收、返回和总耗时与处理器执行、等待审批／准入及命令进程耗时分开记录。统计只纳入已完成且测量有效的根 RPC，子调用和并行跨度不重复计数。
- `file_edit` 的 replace、patch、add、move、delete 共用一份结构化详情，记录动作、路径、dry-run、是否执行、是否改变、受影响文件、增删行和有界 diff。参数拒绝保留调用，但明确标记为未执行。
- 对话侧栏的活跃标记仅由真实根工具请求的 `request_received_at` 驱动：`0 <= now - last_tool_call_at < 30s` 时显示，终止对话不显示。SSE 重放、列表刷新、健康检查和后台探测不能续期。
- 显示页可以持久化关闭 ChatGPT MCP Apps UI。该开关只影响 AgentDock 提供的 UI 资源和工具 UI 绑定，工具能力、结构化结果、文件参数元数据、认证及权限策略保持可用；保存后无需重启 Core。

新增字段均为可选字段。读取 1.1.3 及更早的记录时保持未知值，不回写原始日志，也不从 `UpdatedAt`、完成时间或进程状态猜测请求发生时间。
