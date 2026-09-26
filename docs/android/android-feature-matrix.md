# Android Workbench 功能对照矩阵

本表区分客户端实现与完整验收。Windows 入口相对于 desktop/windows/control-panel；Android 入口相对于 mobile/android/app/src/main/java/dev/agentdock/workbench。接口前缀为 /internal/runtime。截图名为流水线必须采集的目标，不表示仅凭截图即可判定功能完成。

| feature_id | 场景及 Windows 入口 | Core/平台契约 | Android 入口与当前实现 | 测试/截图目标及剩余项 |
| --- | --- | --- | --- | --- |
| WB-A01 | 节点总览；ExecutionWindow.xaml.cs | healthz、各域快照 | ui/Pages.kt：首页显示快照和逐域错误 | WorkbenchNavigationTest / home.png；真实节点身份联调待验收 |
| WB-A02 | 工作区分组；ExecutionWindow.Sidebar.cs | POST execution/sidebar | Pages.kt：分组列表、选择后的资源筛选 | workspaces.png；添加、修改、折叠全部未完成 |
| WB-A03 | 对话筛选；ExecutionWindow.Actions.cs: SetConversationViewAsync | GET conversations，offset/limit | ManagementPage.kt、ManagementContract.kt：关键词/工作区/标签/归档/回收站分页 | ManagementContractTest / conversations.png；完整侧栏历史策略待补 |
| WB-A04 | 独立任务管理；ExecutionWindow.Actions.cs: LoadManagedAsync | GET execution/tasks、GET tasks/{id} | ManagementPage.kt：状态筛选、步骤详情、进度字段 | tasks.png；任务线程切换、取消入口未完成 |
| WB-A05 | 标签/置顶/重命名/回收站；BatchAsync | POST tasks/batch、conversations/batch | 冻结 1–200 个 ID，删除确认、逐项结果，单页有界 | ManagementContractTest、CoreClientHttpTest；真实 Core 副作用待验收 |
| WB-A06 | 关联与当前任务；LinkTaskAsync、TaskMenu_Click | conversations/{id}/link-task、current-task | 未实现对应交互 | 不列为客户端完成 |
| WB-A07 | 活动与发起即显示；ExecutionWindow.SidebarStream.cs | activity、activity/stream | WorkbenchRepository.kt、WorkbenchViewModel.kt：事件订阅、去重、重连和快照刷新 | activity.png；10,000 调用负载和完整历史游标未验收 |
| WB-A08 | 调用详情与输出；ExecutionWindow.Payload.cs | calls/{id}、payload/{request,response,source} | Pages.kt：详情按需读，列表 include_output=false，输出按游标和上限读取 | calls.png；父子树可视展开和完整文件变化未完成 |
| WB-A09 | 调用归档/隔离/导出；ShowCallMenu | calls/batch、导出读取 | 未实现完整调用批量 UI 和导出流程 | 不列为客户端完成 |
| WB-A10 | 插入草稿/回执；ExecutionWindow.Insertion.cs | conversations/{id}/insertions、retry、cancel | Pages.kt、ViewModel：稳定 submission_id，草稿恢复、排队与原始状态展示 | insert.png、draftSurvivesNavigationWithoutSending；WB03 新回执分类待集成 |
| WB-A11 | 对话/调用停止；ChangeLifecycleAsync、StopCall_Click | terminate、calls/{id}/stop | 实际请求接线；不提供不存在的调用 retry API | fixtureNeverDispatchesRealTermuxOrCoreWrites；危险范围确认/恢复对话交互仍需补齐 |
| WB-A12 | 审批；Approve_Click、Reject_Click | approvals 列表、详情、approve/reject | Pages.kt：选择、批准/拒绝及工作区选项 | approvals.png；历史、冲突/过期真实联调待验收 |
| WB-A13 | 权限配置；PermissionSettingsEditor.cs | permissions/effective、revisioned POST permissions | ViewModel：识别 custom_permissions_enabled；缺少字段报 pending_integration | permissions.png；工作区作用域、配置/有效值完整回读未完成 |
| WB-A14 | Skill 管理；RuntimeService.Capabilities.cs | runtime skills | Pages.kt：库存和启停请求 | skills.png；发现/安装/更新/配置与服务能力检测不完整 |
| WB-A15 | 插件/MCP；RuntimeService.Capabilities.cs | plugins、mcp | Pages.kt：库存和启停请求；不加载 MCP UI | plugins.png；完整连接、配置、更新和错误恢复未完成 |
| WB-A16 | Core 连接；RuntimeService.cs | Origin/Bearer、Termux 生命周期 | Pages.kt：端点校验、显式凭据保存/删除 | connections.png、EndpointPolicyTest；自动配对未完成 |
| WB-A17 | 安装更新；平台专属 | RUN_COMMAND、受信清单、PRoot | 固定桥、有界输入、签名校验及安全解包 | install.png、test-termux-bridge.sh；完整 journal/schema 回退/保留策略未完成 |
| WB-A18 | 工程与文件；平台专属 | SAF URI 与 Termux 路径分别授权 | Pages.kt：选择并持久化 Project/Artifacts URI | projects.png；双端探针、创建工程和实际导入导出未完成 |
| WB-A19 | 日志诊断；ExecutionWindow.Actions.cs: SaveExportAsync | Core 摘要、Termux 诊断包 | Pages.kt：诊断入口、最近调用/操作 | diagnostics.png；分页搜索、清理策略和导出预览未完成 |
| WB-A20 | 外观与输出；Models/ToolOutputSettings.cs | APK 显示偏好，Core 输出限额 | Theme.kt、Pages.kt：深浅色、密度、详细输出、1,000–100,000 上限 | settings.png；字号/横屏/平板实际截图待补 |
| WB-A21 | Android 守护；无直接 WPF 对等入口 | WorkManager/FGS/Tile、desired state | lifecycle/：明确启动、暂停、停止与开机检查 | Termux 停止意图/未知 PID 回归；网络/充电约束、熔断和长时真机未完成 |
| WB-A22 | 返回与恢复；移动端专属 | SavedStateHandle、Activity BackHandler | 保存筛选、页面/选中 ID、草稿；连接变更取消旧订阅 | restoresNavigationAndCommittedFiltersAfterRecreation；真实进程杀死/系统手势仍未验收 |
| WB-A23 | 错误与空态 | 稳定错误/鉴权/未知状态 | tasks/conversations 严格响应数组检查；页面错误与空态分开 | tasks-empty.png、tasks-error.png；其他域严格响应验证仍需补齐 |
| WB-A24 | 回执安全 | operation/request/nonce、终态不可回退 | termux/：凭据字段拒绝、时效、UTF-8 上限、错误脱敏 | TermuxResultValidatorTest、PendingOperationStoreTest；丢回执后远端 operation 查询未完成 |

## 验收解释

ManagementContractTest 和 CoreClientHttpTest 验证请求、响应与传输边界，不等于运行真实共享 Core 的业务副作用测试。WorkbenchNavigationTest 验证全部 16 个页面可达、18 张截图、草稿与 Activity 重建、Fixture 写隔离。跨工作线新增端点/语义未在本线合并，相关端到端结果必须保留为待集成。

全功能未达到完成条件。所有明确未实现项保持待办，不能用“移动端不适用”或仅可读界面取消原任务需求。
