# Task / Thread / Activity / Workspace

AgentDock 1.1.0 的任务活动中心是独立 Windows 窗口，展示经过 AgentDock 的实际执行记录。Task 保存目标和验收，Thread 保存可恢复的执行分支，Activity 保存有界事件。Thread 不运行模型，也不创建后台 AI Worker 或 Git worktree。

## 入口与恢复

托盘菜单选择“任务活动中心”。也可直接启动 `agentdock-tray.exe --activity --runtime-root <安装根目录>`。独立入口不启动服务、不改设置、不占用托盘单例；关闭窗口释放 HTTP/SSE 订阅。切换查看的线程只改变视图，“设为继续线程”操作才改变 Task 的默认线程。

`agentdock_context(workdir)` 返回该目录对应的 Workspace 和最多 8 个活动/阻塞任务摘要；任务索引最多 16 KiB，损坏任务的读取警告不阻断其他能力。先复用 `task_id` 和 `active_thread.thread_id`，再以 `task_manage(get)` 或 `thread_get` 加载检查点。已有命令通过原 `session_id` 观察和停止，不因切换默认线程而转移。

## 线程与任务生命周期

`task_manage` 保留 create/list/get/checkpoint/block/resume/final_review/complete，并增加 cancel/archive/unarchive。取消要求原因，保存 `outcome=cancelled`；正常完成保存 `outcome=success`。归档从默认列表和启动索引排除任务，但 get 和显式 include_archived 仍可读取。

线程动作是 thread_create/thread_list/thread_get/thread_switch/thread_checkpoint/thread_block/thread_resume/thread_fork/thread_close。分叉复制检查点与步骤，保存父线程和 checkpoint_event_id，不复制活动日志。执行绑定要求 active Task 和 open Thread。关闭或取消不会自动终止已有进程，使用“停止命令”单独处理仍在运行的会话。

旧 Task 只读加载时产生虚拟 main 线程。首次修改或执行绑定才落盘，启动时不批量迁移。Task 和 Thread 使用有界写前事务共同恢复，不把成功的最终复核当作已完成所有待办。

## Workspace 路由

`workspace_manage` 提供 list/get/register/resolve。register 明确指定绝对 root；修改已有记录必须提供 workspace_id 和匹配的 expected_revision。相同 runtime/root 复用记录，不能通过重复注册覆盖自定义路径。项目名称存在多个匹配时要求显式 ID。

命令和文件编辑的可选 `workspace_id` 优先于线程和任务绑定。新任务通过 project 匹配或全局配置目录取得工作区；`agentdock_context(workdir)` 不修改命令全局默认目录。保留旧版无绑定工具调用，它们继续使用原显式路径或配置目录，不使用进程当前目录。

| target_kind | 位置 | 要求 |
| --- | --- | --- |
| source | workspace.root | 默认源码目标 |
| artifact | artifact_root/task_id | 必须有 Task |
| scratch | scratch_root/task_id | 必须有 Task |
| cache | cache_root | 可重新生成的缓存 |
| external | 单次 external_path | 必须明确绝对路径，不修改任何默认目录 |

Native 工作区校验实际路径、符号链接、父目录、越界和 Windows 数据流路径。源码目录的创建必须显式 register(create_root=true)。WSL 使用独立 runtime 与 Linux 路径，沿用现有 WSL 文件工具限制。路径路由不隔离 shell 命令内容，操作系统权限仍是执行边界。

带工作区绑定的多文件 patch 使用结构化 `*** Begin Patch` 格式，先校验全部目标再执行。未绑定的旧调用仍支持原统一 diff 格式。文件事件记录原逻辑路径和实际解析路径，dry_run 不产生 file.changed。单文件增删行数来自编辑器；无法得到逐文件统计的批量操作不展示虚构计数。

## 活动与本地访问

事件存储于 `<AGENTDOCK_HOME>/tasks/activity`，按全局 seq 分段，Task/Thread 字段用于筛选。默认最多保留 8 个约 8 MiB 分段；轮换、按时间清理或崩溃预留序号可以造成明确的历史缺口，不重用已经分配的序号。恢复缺口以 gap/reset/warning 通知客户端，不能把已清理数据作为完整历史。

终端 stdout/stderr 以 300 ms 窗口合并，每条预览各最多 8 KiB。活动读取与工具输出具有独立游标。分段写入前脱敏命令、授权头、密码参数、私钥参数及已知秘密环境值，超长不完整行和私钥块被省略并标记截断；不保存环境变量映射或完整终端输出。动态 MCP、浏览器和插件事件只保存外层调用事实，不将任意远端正文保存成可信执行指导。

本地 API 支持活动查询、SSE、线程查询、控制和当前 Diff：

```text
GET  /internal/runtime/activity/tasks
GET  /internal/runtime/activity?task_id=...&thread_id=...&after=...
GET  /internal/runtime/activity/stream
GET  /internal/runtime/tasks/{task_id}/threads
GET  /internal/runtime/tasks/{task_id}/threads/{thread_id}
GET  /internal/runtime/tasks/{task_id}/threads/{thread_id}/activity
GET  /internal/runtime/tasks/{task_id}/activity/stream
GET  /internal/runtime/activity/diff?task_id=...&thread_id=...&seq=...
POST /internal/runtime/activity/control
```

上述新增活动接口要求直接回环客户端，同时校验 Host、Origin 和代理头，并沿用已配置的 Bearer/OAuth 鉴权。Cloudflare/Tailscale 转发请求被拒绝。SSE 支持 Last-Event-ID、按 seq 重放、心跳、最多 32 条连接及慢消费者写入期限。Windows 客户端不使用代理或 HTTP 重定向，使用有界队列和虚拟化列表，最多显示最近 1000 行，输出折叠状态属于每行。

Diff 只接受已持久化 file.changed 引用，重新验证当前工作区路径，在有界、禁止外部 diff/textconv 的 Git 子进程中读取当前 tracked 工作树与 HEAD 的差异，最多 256 KiB 并脱敏。它不还原历史文件，未跟踪文件和已移出当前工作区的路径返回明确提示。

## 软 Harness

启动说明保留极短执行不变量，context 返回恢复索引，统一外层 `agentdock_guidance` 按本次实际结果提示复用会话、检查退出码、验证文件和记录检查点。原有工具结果字段不被替换；嵌套 MCP 正文不能覆盖可信 Guidance。本机制不实施模型循环、自动压缩或权限沙箱。

## 验证入口

```powershell
go test -p 2 ./... -count=1
go vet ./...
go test -race -p 1 ./internal/activity ./internal/taskstate ./internal/workspace ./internal/tool/command/... ./internal/app ./internal/httpx
.\scripts\test\test-windows-activity-center.ps1 -TestRoot E:\validation\activity-ui
.\scripts\test\test-windows-activity-runtime.ps1 -CoreBinary <新Core.exe> -TestRoot E:\validation\activity-core
```

每次测试使用全新目录。桌面回归覆盖中英文离屏缩放渲染、输出和列表上限、SSE 断线续传与释放；Native 回归通过真实 Core/MCP/HTTP 验证执行、文件路由、线程隔离、重启和旧任务懒迁移。安装回归另行验证 1.0.x 升级、同版修复、失败回滚、保留用户数据和无额外控制台窗口。
