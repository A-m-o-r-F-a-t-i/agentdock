# Windows-to-Android Workbench feature matrix

“Mapped” means the page and contract exist in WB07. It does not claim every shared Core mutation route is already available.

| Capability | Android destination | Source | WB07 state |
|---|---|---|---|
| Overview/node status | 首页 | Core health + desired state | Mapped |
| Workspace grouping | 工作区 | execution sidebar | Mapped |
| Conversations | 对话 | Core sidebar/detail | Mapped |
| Independent task center | 任务中心 | task/activity API | Mapped |
| Active/event stream | 活动流 | Core SSE/activity | Mapped |
| Call detail/output/timing | 调用详情 | call API/SSE | Mapped, bounded raw record |
| Insertion/receipt | 插入与停止 | insertion API | Mapped |
| Conversation/call stop | 插入与停止 / 调用详情 | control routes | Mapped |
| Approvals | 审批 | approval API | Mapped |
| Permission editor | 权限 | effective policy + revision write | Mapped, closed by default |
| Skill inventory/management | Skill | runtime Skill API | Inventory mapped; mutation depends on shared API |
| Plugin inventory/management | 插件与 MCP | runtime plugin API | Inventory mapped; mutation depends on shared API |
| MCP inventory/management | 插件与 MCP | runtime MCP API | Inventory mapped; mutation depends on shared API |
| Core endpoint/credentials | Core 与连接 | endpoint policy + Keystore | Mapped |
| PRoot install/update/rollback | 安装与更新 | fixed Termux bridge | Mapped; install waits for signed manifest |
| Existing node adoption | 安装与更新 | explicit Termux adoption | Contract mapped; final UI payload integration pending |
| Project/artifact directories | 项目与文件 | Android SAF | Mapped |
| Logs/diagnostics | 日志与诊断 | Core summary + Termux export | Mapped |
| Theme/language/density | 设置 | DataStore | Mapped |
| Guardian/tile/boot check | 设置 | Android lifecycle | Mapped |
| Public access | 设置 | explicit state | UI mapped; provider mutation depends on shared API |
| Detailed calls/output limit | 设置 | Android display + Core policy | Mapped; Core remains authority |

Compact screens change navigation presentation only. Every destination remains in the drawer; bottom navigation is only a shortcut.
