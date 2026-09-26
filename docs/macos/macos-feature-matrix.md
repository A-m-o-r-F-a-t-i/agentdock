# macOS 原生 Workbench 功能矩阵

Windows 比较基线为 `b367eaab95202873fb213b8713440bf7822878c4`。Windows 路径以下均相对 `desktop/windows/control-panel/`，macOS 路径相对 `desktop/macos/AgentDockApp/Sources/`。本线保留 AppKit，不修改 WPF 和共享 Core 状态机。

“已实现”描述源码覆盖，不自动表示整个候选通过。最终门禁为本线精确 SHA 的 `.github/workflows/parallel-macos.yml`：原生 arm64、原生 x86_64、自测、实际 AppKit 截图、同 SHA 通用包与签名检查必须同时通过。每次 run 的工具链、测试 XML、日志、截图及候选报告随 Actions artifact 保存。交接文件记录最终 SHA/run/attempt，不使用旧 run 替代后续提交。

## 1. 导航与原生窗口

| feature_id | 用户场景 | Windows 基线入口 | Core 接口 | macOS 实现 | 状态与证据 |
|---|---|---|---|---|---|
| MAC-001 | 菜单栏打开单一 Workbench，重复打开前置原窗口 | App.Activity.cs / ShowActivityCenter | 无 | AppDelegate、WorkbenchWindowController | 已实现；NativeUITests 窗口身份与关闭重开断言 |
| MAC-002 | 关闭窗口继续运行节点 | App.Activity.cs、App.xaml.cs | 节点生命周期独立 | WorkbenchWindowController.windowWillClose | 已实现；RealCoreTests 关闭窗口后 Core 仍响应 |
| MAC-003 | 可调整三栏、保存窗口和分栏位置 | ExecutionWindow.xaml | 无 | NSSplitViewController、frame autosave | 已实现；原生深浅色及窄窗口截图 |
| MAC-004 | 工作区分组与“搜索对话” | ExecutionWindow.Sidebar.cs / LoadSidebarCoreAsync | POST execution/sidebar | WorkbenchSidebarViewController | 已实现；实际 Core 窗口、身份测试、本地化测试 |
| MAC-005 | 最近三天默认五条、展开十五条、完整历史 | ExecutionWindow.Sidebar.cs / LoadMoreProjectConversationsAsync | 旧 Core 接受 5 或 20 的倍数 | WorkbenchSidebarPolicy、ManagementWindow | 展示采用 5/15；传输按旧 Core 的 20 项分段兼容；完整历史走分页管理器 |
| MAC-006 | 选中、运行中、置顶记录可见；折叠全部 | ExecutionWindow.Sidebar*.cs | 服务端摘要和 history cursor | SidebarPolicy、折叠偏好 | 已实现；1000 对话、旧置顶/选中等单测。超出已加载摘要的置顶集合仍依赖 Core 投影 |
| MAC-007 | 添加工作区及查询 | ExecutionWindow.xaml.cs / LoadWorkspacesAsync | GET/POST workspaces | ManagementWindow 工作区页 | 已实现客户端；WB01 未合并时显示不可用，待集成 |
| MAC-008 | 合法未归属调用与异常列表恢复 | ExecutionWindow.Sidebar*.cs | is_unattributed、空 conversation_id | Validation、navigationID 与资源 ID 分离 | 已实现；重复/缺失/保留 ID 测试和真实 Core 历史查询 |

## 2. 对话、任务与调用管理

| feature_id | 用户场景 | Windows 基线入口 | Core 接口 | macOS 实现 | 状态与证据 |
|---|---|---|---|---|---|
| MAC-010 | 真实标题、详情、无任务调用 | ExecutionWindow.xaml.cs | conversations/{id}、calls | Models、ViewModel、Timeline | 已实现；真实 MCP 两个模拟宿主生成数据，原生客户端读取 |
| MAC-011 | 重命名、标签、置顶、归档、回收站、恢复、永久删除 | ExecutionWindow.Actions.cs / BatchAsync | conversations/batch、tasks/batch | ManagementWindow、对话操作菜单 | 已实现；明确资源集合、危险操作确认、部分失败保留；真实 Core 重命名 |
| MAC-012 | 任务独立检索、详情、步骤、检查点、条件和复核 | ActivityWindow.xaml.cs、ExecutionWindow.Actions.cs / LoadManagedAsync | execution/tasks、tasks/{id} | ManagementWindow、TaskSummary | 已实现；100 项分页、原始检查点/线程字段保留；真实任务读取 |
| MAC-013 | 对话顶部任务进度 | ExecutionWindow.xaml | 服务端 steps/step_count | TaskSummary.progressText、Timeline | 已实现；仅计算返回步骤的展示比例，不改任务状态 |
| MAC-014 | 关联任务、设为当前任务 | ExecutionWindow.Actions.cs / LinkTaskAsync | link-task、current-task | WindowController、APIClient | 已实现；绑定修订号来自 Core；不伪造宿主身份 |
| MAC-015 | 终止/恢复对话、停止具体调用 | ExecutionWindow.Actions.cs / ChangeLifecycleAsync | terminate/resume、calls/{id}/stop | 明确范围的操作菜单和调用按钮 | 已实现；结果由 Core 决定，不表示停止外部网页或模型推理 |
| MAC-016 | 单 Call 聚合、父子调用、根调用过滤 | ExecutionWindow.xaml.cs、ExecutionWindow.Actions.cs | calls?top_level=true、parent_call_id | ViewModel.mergeCalls、调用管理页 | 已实现；10000 调用去重/旧序列保护；子调用独立有界分页 |
| MAC-017 | 调用归档、隔离、回收站及批量处理 | ExecutionWindow.Actions.cs / CallMenu、LoadManagedAsync | calls/batch | 调用管理页 | 已实现；明确 ID 集合、Core 返回每项状态 |
| MAC-018 | 命令、目录、状态、错误、退出码、耗时、文件变化与 Diff | ExecutionWindow 详情/技术区 | calls/{id} | DetailViewController、Call DTO | 已实现；未知值保持未知；原始记录包含完整可用字段 |
| MAC-019 | 隐藏输出不读载荷；展开、下一段、复制与导出 | ExecutionWindow.Payload.cs / LoadPayloadPageAsync | payload/request、payload/response；limit_chars；字节 offset | PayloadSlice、按需按钮 | 已实现；最多10000 Unicode 标量/段，字节续传；中文/emoji/组合字符、真实 Core 载荷测试 |
| MAC-020 | 执行中与近期交互分别展示 | ConversationActivityPolicy、Sidebar | last_interaction_at、expires_at、in_flight | Models、服务端时间锚点 | 已实现兼容；120/180 边界单测；WB04 新增语义真实跨线集成待执行 |
| MAC-021 | 持续输出、重连、缺口、旧结果不串线 | ExecutionWindow.SidebarStream.cs、History.cs | calls/stream、Last-Event-ID、gap/reset | Transport、SSE、ViewModel | 已实现；256 事件缓冲、1000 调用窗口、100ms 合并刷新、15s 摘要轮询；断线/取消/切换单测 |
| MAC-022 | 完成通知并打开对应任务 | Services/CompletionNotificationService.cs | execution/notifications | App-owned CompletionNotifications、原生 panel | 已实现；Core 认领身份，最多3面板、15秒关闭、128条去重缓存；原生面板测试，无额外系统通知授权要求 |

## 3. 插入、审批与权限

| feature_id | 用户场景 | Windows 基线入口 | Core 接口 | macOS 实现 | 状态与证据 |
|---|---|---|---|---|---|
| MAC-030 | 180秒插入资格、稳定提交 ID、300秒原始期限 | ExecutionWindow.Insertion.cs | conversations/{id}/insertions | Composer、ViewModel | 已实现；未知资格禁用；超时先回读，同一文本保留 submission_id；真实 Core 去重/取消 |
| MAC-031 | 排队、附加未确认、不同层级回执、有限重投 | Models/InsertionPresentation、InsertionTimeline | receipt_type、manual_retry_available、remaining budgets | Insertion DTO、时间线/详情 | 已实现客户端；接收方回执、宿主转发、上下文写入明确区分；WB03 跨线与真实外层宿主验证待执行 |
| MAC-032 | 附加后30秒回执等待与到期由 Core 管理 | ExecutionWindow.Insertion*.cs | next_retry_at、expires_at、delivery_reason | 原样展示服务器状态 | 客户端轮询上限不充当新的状态机，不把等待结束显示为送达 |
| MAC-033 | 待审批、原参数、批准/拒绝、历史及冲突 | ExecutionWindow.Actions.cs、审批服务 | approvals、approvals/{id}/approve/reject | 审批页和调用详情 | 已实现；选择具体审批，先读取原请求；真实 Core 批准后实际固定调用完成；403/409 不自动重试 |
| MAC-034 | 全局/工作区权限编辑与有效来源 | PermissionSettingsEditor.cs | permissions/effective、permissions | WorkbenchPermissionEditor | 已实现；revision/CAS、执行模式与三层规则分开；真实过期修订409测试 |
| MAC-035 | “启用自定义权限设置”、关闭后保留历史配置 | PermissionSettingsEditor.cs；WB02 新契约 | custom_permissions_enabled、configured_settings、settings_source | Forms、PermissionState | 已实现兼容；旧 Core 明确禁用此开关；fixture 开/关原生截图；WB02 实际整合待执行 |
| MAC-036 | Profile / Policy / Reviewer、granular 与继承来源 | PermissionSettingsEditor.cs | settings 与 scope | 原生选择器、粒度分类 | 已实现；never 不解释为授权全部；自动审查不在 App 中调用模型 |
| MAC-037 | 首次新装完全权限且已有配置保留 | WB02/WB05 共用职责 | Core/installer 初始化 | macOS 不重写共享默认值 | pending_integration；本线没有另建默认权限逻辑 |

## 4. Skill、插件、系统与候选包

| feature_id | 用户场景 | Windows 基线 | macOS 实现 | 状态与证据 |
|---|---|---|---|---|
| MAC-040 | Skill 列表、详情、启停、同名不同来源 | 控制面板 Skill 管理 | 管理器调用 skills?summary=true 与 skill_ref | 已实现现有 Runtime 范围；独立 Skill 安装/更新未由旧 Runtime 公开，不能伪造成功 |
| MAC-041 | 插件校验、本地安装/更新、启停、成员和按需加载 | 控制面板 Plugin 管理 | plugins GET/POST、原生路径选择器与确认 | 已实现旧 Runtime 动作；更换既有更新源所需 confirmed_source_change 未被旧 HTTP DTO 公开，待 WB01 契约增量 |
| MAC-042 | MCP 列表、详情、添加 HTTP 服务、刷新、启停与移除 | 控制面板 MCP 管理 | mcp GET/POST | 已实现；HTTPS URL 不允许嵌入凭据；本端不改 Core 鉴权 |
| MAC-043 | Core 服务、Tunnel/连接、更新、配置和日志 | 托盘与设置 | 保留 ServiceController、SetupWindow、AdvancedSettings、原更新事务 | 既有实现不重做；原 macOS 回归、真实包结构和签名测试 |
| MAC-044 | 浅色、深色、系统主题及中英文 | WPF 主题与文案 | 动态 AppKit 颜色、既有 L10n 中英资源 | 已实现；同一真实原生窗口深浅色、窄窗口、管理页与权限页截图，翻译/格式门禁 |
| MAC-045 | 原生快捷键与可访问性 | WPF 命令/焦点 | AppKit 标准控件、辅助标签、Cmd-Return 插入 | 原生断言；完整 VoiceOver、物理键盘、多屏与系统外观变化仍待人工 |
| MAC-046 | 可选能力系统权限、授权进程归属、失败时手动路径 | macOS 特有 | DesktopPermissionChecker、DesktopPermissionsWindow | 已实现说明；普通节点不依赖可选桌面权限；实际 TCC 弹窗/第三方 Skill 授权链待实体环境 |
| MAC-047 | 原生 Intel 与 Apple Silicon 构建和运行 | 现有发行构建基线 | macos-15-intel / macos-15，uname 严格检查 | 双原生 CI 分开记录；不把交叉切片构建称为原生运行 |
| MAC-048 | Universal App、Core、helper、DMG、更新ZIP及摘要 | packaging/macos/build-app.sh | 复用原打包器，专属候选工作流 | 最终同 SHA candidate job 是交付门禁；失败/取消/跳过均不计通过 |
| MAC-049 | 签名、公证、安装、升级和回退 | 平台发行职责 | ad-hoc 候选，保持既有恢复事务 | Developer ID 公证未执行；真实安装/升级/回退未执行；不关闭 Gatekeeper |

## 5. 大数据与证据口径

测试输入包含1000条合成对话、10000条合成调用，验证近期投影和1000条有界调用窗口。它们不是用户历史数据。XCTest报告给出实际用例耗时，原生界面截图证明 AppKit 被实例化和绘制。截图和编译不替代完整人工可访问性、长期睡眠/唤醒、真实桌面权限弹窗或安装验收。

`fixture-core` 在 GitHub-hosted runner 的新临时目录启动同 SHA 的真实 Go Core，通过真实 SDK/HTTP 生成两个模拟宿主的任务、调用、审批与未归属记录。Swift 客户端实际执行查询、载荷读取、修改、审批、插入去重及取消，并在关闭原生窗口后继续查询 Core。该证据不宣称已连接真实 ChatGPT 外层宿主，也不宣称未合并的 WB01–WB04 已整合。
