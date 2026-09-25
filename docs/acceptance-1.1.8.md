# AgentDock Workbench 1.1.8：原计划验收矩阵

　　本文件继续核对《AgentDock_1.1.7_详细修改计划.md》的 R01–R09、F01–F11，以及后续加入的权限、插入回执、消息行与全平台发行。1.1.7 已发行是既有事实，不能替代原计划尚未执行的原生恢复、键盘和性能验收。本修订不移动或覆盖已发布的 1.1.7 标签与资产。

## 1. 状态口径

　　“已有回归”表示 1.1.7 构建已执行相关自动化测试，本修订全量测试仍须再次运行。“新增原生门禁”要求实际 Windows x64/ARM64 runner 执行通过，不接受本机编译或 fake 测试替代。“明确限制”表示功能有实际范围，不能改写成未实现的平台能力。

| 原编号 | 要求与对应实现 | 验收入口 | 当前范围 |
| --- | --- | --- | --- |
| R01 | 1,000–100,000 Unicode 标量输出设置、调用入口快照、普通正文共享预算和授权续读 | `internal/app/tool_output_test.go`、`internal/mcp/output_policy_test.go`、桌面 `OutputPolicyTests` | 已有回归；未知结构化 Schema 明确豁免，控制/规则/补充不截断 |
| R02 | 项目标题移除“对话”徽标及残余边框 | 实际 `ExecutionWindow.xaml` 离屏模板断言 | 已有回归 |
| R03 | “搜索对话”与无障碍名称，保留项目搜索和清空逻辑 | `SidebarInteractionTests`、实际 WPF 模板 | 已有回归 |
| R04 | 中文动作映射与去重、历史英文标签和未知工具回退 | `ExecutionTitleFormatter` 纯策略回归及真实 WPF 行 | 已有回归；不翻译/改写技术标识 |
| R05 | 删除子调用 Tab、专用加载与失效索引，保留内部审计关联 | 实际 WPF 标签与请求测试、后端根调用合同 | 已有回归 |
| R06 | 目标规则/绑定单次收敛，同目标并发收敛、不同目标冲突、完整性终态 | `context_binding_test.go`、`execution_scope_test.go`、完整根调用性能夹具 | 已有回归；外部 ChatGPT 自行重复调用不能由服务端承诺消失 |
| R07 | 明确字节位置、替换式分页末页不冒充全文、损失来源不冒充可读 | payload Go 回归、`ExecutionPayloadView` 纯策略及真实详情 | 已有回归 |
| R08 | 更多按钮真实事件、去重、失败回退、空页、删除、搜索/关闭竞争、键盘 | `SidebarInteractionTests` 的 100 轮、鼠标预览、UIA、原生 HWND 上 Enter/Space | 新增原生门禁；空项目保留不可见分组锚点，不保留可点页脚 |
| R09 | 修改增删统计在首个时间字段前，详细/简洁、明暗主题、两种宽度和四档渲染 DPI | WPF `ValidateActualRows`、`ValidateCallTable`、离屏图片 | 已有回归；不是物理显示器硬件测试 |
| F01 / Q01 | 原子 journal，内存不提前提交，临时文件/部分写/同步/关闭/替换故障 | `journal_atomic_test.go`、`atomicfile/write_fault_test.go` 九个原子写失败边界 | 新增故障回归；故障后旧/新完整 JSON 及引用的备份保留 |
| F02 / Q02 | 先校验和暂存、保留当前目录、重命名中断恢复与幂等 | `journal_restore_test.go`、原生占用与属性篡改回归 | 已有回归加新增原生门禁 |
| F03 / Q03 | 权限转换阶段清晰，恢复失败和未知进程不清理材料 | `PrivilegeTransitionTests`、`ControlPanel.NativeTests` | 新增原生任务 COM、真实 NTFS 和进程等待门禁；双向转换和原任务不存在均覆盖 |
| F04 / Q04 | 取消贯穿等待与提交，已提交结果不被取消掩盖 | permission/insertion/conversation/filelock 的 cancellation tests、race | 已有回归 |
| F05 / Q05 | 同版本不反复解析，暖读不按全 JSON 分配，外部变更可见，达到十倍目标 | `conversation_snapshot_test.go`、`conversation_exact_snapshot_test.go`、统一性能夹具 | 本修订精确逐字节快照比较，已有三轮同机结果超过十倍；最终版本数据另存证据 |
| F06 / Q06 | OAuth pending 独立配额与固定授权窗口、授权后持久化晋升、索引回收 | auth/httpx pending/capacity/PKCE/restart tests | 已有回归；一小时 pending 窗口与模拟 50 分钟授权流程记录分开 |
| F07 / Q07 | 文件模式、Windows owner/group/DACL 与可支持属性保真，不丢弃未知附加数据 | `journal_metadata_windows_test.go`、POSIX 0600/链接/预算测试 | 新增原生门禁；ADS、EA、reparse、压缩/稀疏/加密等不支持对象明确拒绝，不伪报完整备份 |
| F08 / Q08 | 活动事件数/字节预算，副作用前预留终态，取消释放、慢盘恢复 | `append_budget_test.go`、execution/command capacity tests | 已有回归；计时值可能低于时钟分辨率，单独批次数确认计量发生 |
| F09 / Q09 | generation 隔离，新调用不加入旧 flight，关闭和取消交错正确 | `internal/snapshot` 并发测试及上下文缓存调用方测试 | 已有回归 |
| F10 / Q10 | 路由、状态转移、准入纯计算、类型化参数、Windows 权限事务定点拆分 | 各原有合同测试和全库测试 | 已有回归；唯一准入、审计、恢复所有权保持不变 |
| F11 / Q11 | 解析/测试/构建/发布同 SHA，安装 not_run 不被资产成功覆盖 | `workflow_identity_test.go`、`test-workbench-release.py`、六目标发行工作流 | 新增发布门禁要求原生 Windows 恢复、ARM 安装与 Linux 包事务证据 |

## 2. 新增要求与组合场景

　　权限 PR #13 的三层规则、插入队列 v2 的未确认重投/接收回执、独立消息行和普通插件 Heavy 文案继续保留。`permission_insertion_integration_test.go` 验证禁止普通文件和命令的 Profile 仍允许合法补充回执，且回执不会扩大业务权限。SDK/Invoke/投影后附加测试验证消息不重新执行原工具、去重、真实证据状态和第三方伪造隔离。

| 组合 | 证据入口与判定 |
| --- | --- |
| 最小输出 + 首次规则/绑定 | 输出预算与上下文一致性相邻回归，规则不被截断 |
| 大正文 + 用户补充 + 两种 MCP 适配器 | 同一预算、两种正文副本一致，用户补充完整，回执后停止重投 |
| 编辑统计 + 输出分页 + 两种布局 | 后端实际统计与 WPF 模板共用，不从显示正文重算 |
| 分页 + 推送/搜索/选中切换 | 导航版本与取消拒绝陈旧响应，100 次交互不重复 |
| 移除子调用 + 其他详情页 | 实际模板命名 Tab 与技术/耗时/文件详情继续使用原数据 |
| 缓存 + 外部写入/权限/删除/终止 | 相同时间/大小的原地改写仍可见；保留每次内容与生命周期核验 |
| 慢盘 + 满队列 + 取消 | 取消释放票据，无配额时未执行命令，已有结果使用预留落盘 |
| 真实权限事务 + 恢复失败/未知进程 | 禁止与仍活跃的原生进程并发恢复，保留 task.xml/state.json/transition.json |
| 旧记录 + 新客户端 | 历史标题映射，未知统计和缺失正文保留未知，不回写历史日志 |

## 3. 性能测量

　　10,000 条记录暖读目标采用原审计提交 `b66e5f94c0feca92e4d15f0776bf922117549a85` 与本修订相同夹具、同 Windows amd64 机器、Go 1.26.5 测量。100、1,000、10,000、20,000 条分别有 11 个冷读和 51 个暖读样本，三轮交替执行 before/after。初步 10,000 条暖读中位数改善分别为 12.18、13.67、14.25 倍，原始 JSON 保留；不是把旧工具链或另一机器结果混入对比。

　　优化保留一份最多 16 MiB 的不可变编码快照，暖读仍持有跨进程锁并比较全部实际字节，变化时重新校验和解析。不会仅相信时间戳或文件大小。新增内存占用与减少重复哈希/分配是明确取舍，不能称为“无额外内存”。

　　完整 `Runtime.Call(agentdock_context)` 使用三种合成插件规模，各 20 次冷请求、100 次暖请求、100 次并发冷请求、100 次并发暖请求，共 960 个请求。记录 call_id、总时延、规则/Skill 阶段、目录构建数、注册表读取/字节/解析次数。样本不包含 HTTP 传输及 ChatGPT 页面渲染，不能当作其端到端耗时。新夹具在正常可信对话身份下调用，必需上下文与绑定一致性一并检查。

## 4. 平台覆盖与停止条件

　　任务调度器在重新注册旧描述符时可能把显式 Allow ACE 稳定移到继承 ACE 前，并标记 `D:AI`。本版区分“原始描述符完全一致”和“任务调度器的纯 Allow 规范化”。后者仅接受每个 ACE 的二进制内容、重复数量、各组相对顺序全部不变，以及只新增自动继承标志的情况；SID、权限位、保护状态、ACE 继承标记改变，Deny/对象/回调 ACE 的重排都拒绝并保留恢复材料。原始备份不被改写，原生报告分别记录两种结果。普通文件备份不使用这个比较器，仍核对 owner/group/DACL/属性原样一致。

　　这一任务级比较边界对应 Windows 的显式 ACE 优先顺序与继承模型转换，见 Microsoft 的 [Automatic Propagation of Inheritable ACEs](https://learn.microsoft.com/en-us/windows/win32/secauthz/automatic-propagation-of-inheritable-aces) 和 [Order of ACEs in a DACL](https://learn.microsoft.com/en-us/windows/win32/secauthz/order-of-aces-in-a-dacl)。相邻测试逐位改变权限、变更 SID、保护/继承标志、增删 ACE 以及重排拒绝项，均必须失败。任务恢复注册还关闭 registration trigger 的自动触发，避免单纯恢复定义重放任务动作。

　　新发行需在 Windows x64/ARM64 上通过真实任务调度器和备份元数据验证，再执行 Windows 两架构包验证与隔离安装、修复、卸载。Linux DEB 在匹配架构的 Ubuntu runner 原生安装/验证/移除；RPM 使用匹配 CPU 的真实 RPM 事务引擎与独立根数据库，不宣称完成所有 Fedora/RHEL 发行版兼容测试。macOS 保留 Intel/Apple Silicon 原生后端与通用 App 校验。

　　这些测试没有操作用户正在运行的生产任务或 Core。发布者签名、Apple Developer ID 公证、所有物理显示器与外部 ChatGPT 宿主集成不属于已自动完成能力。未知/不支持的元数据必须拒绝并保留原对象，不能用“忽略比较”使测试变绿。新增恢复/输入门禁任何失败时不发布新版本。最终运行编号、源码 SHA、测试数字和包校验记录在交付证据中逐项列出。
