# AgentDock Workbench：分层权限系统

## 1. 模型与执行顺序

本更新在原有固定请求、审批队列和执行中心上增加三层设置，不另建执行器。旧的 `readonly` / `rules` / `full` 模式与工具规则继续保留。

```text
显式 Deny 规则
  → Permission Profile：文件系统、网络、目标边界的准入上限
  → 现有模式与规则：Allow / Ask / Deny
  → Approval Policy：Ask 是否允许进入审批
  → Approval Reviewer：user / auto_review
  → 固定请求复验、修订校验、一次性 Claim、原工具执行
```

Profile 不会直接批准操作；它限定操作上限。`full`、显式 Allow、一次性审批、本地用户快捷执行和自动审查均不能覆盖 Profile 的拒绝。`never` 不是自动批准：原本允许的操作照常运行，原本需要审批的操作直接拒绝，不创建待审批项。

## 2. 数据与作用域

三层设置是一个完整对象，避免多层零散字段产生隐含授权：

```json
{
  "permission_profile": {
    "filesystem": "read",
    "network": "deny",
    "sandbox_boundary": "workspace"
  },
  "approval_policy": {
    "mode": "on-request"
  },
  "approval_reviewer": "user"
}
```

| 字段 | 可选值 | 语义 |
| --- | --- | --- |
| filesystem | deny / read / write | 禁止文件访问 / 允许读取 / 允许读写；仍受其他层约束 |
| network | allow / deny | 是否准入已知联网操作；受限配置拒绝无法约束的内部副作用 |
| sandbox_boundary | none / workspace | 无额外工作区目标限制 / 所有可检查目标必须位于固定源工作区 |
| approval_policy.mode | on-request / never / granular | 需要时审批 / 不受理审批 / 按类别受理审批 |
| approval_reviewer | user / auto_review | 本地用户 / 管理员配置的独立审查程序 |

全局设置可以被明确指定工作区的完整设置覆盖。未配置的工作区继承全局。对话 ID 不是授权主体，不允许设置 Profile 或 Reviewer；原有对话级限制保持兼容。

有效响应分别给出旧模式的 `scope` 和三层设置的 `settings_scope`、`settings_scope_id`。工作区修改只影响该工作区；`inherit_settings: true` 删除其三层覆盖，保留原有模式设置。

旧 schema 1 策略无需预先重写：默认解释为 `write / allow / none + on-request + user`，保持升级前行为。首次保存三层设置写入 schema 2。策略仍保存在 `execution/permissions/policy.json`，使用原有原子写入和 `expected_revision` 比较交换。配置无效、作用域错误或修订过期时不会部分保存。降级到不识别 schema 2 的旧程序前应由管理员恢复备份，而非手工删掉新字段绕过校验。

## 3. granular 的含义

```json
{
  "mode": "granular",
  "granular": {
    "file_writes": true,
    "commands": false,
    "network": false,
    "mcp": false,
    "management": false,
    "other": false
  }
}
```

这些布尔值表示“允许申请审批”，不是“直接授予权限”。一个操作同时匹配多个类别时，各类别都必须允许。例如命令存在联网能力时，`commands` 与 `network` 均需启用；第三方 MCP 同时涉及 `mcp` 与 `network`。未知的新工具落入 `other`。已由显式规则允许的请求不经过 Ask，因此不会再次触发类别门控；Profile 仍在此前检查。

这是 AgentDock 自身的分类，不承诺与其他产品的 granular 字段兼容。非 granular 模式不得携带 granular 对象；granular 模式必须明确提供对象，未选类别为 false。

## 4. 边界的真实能力

**本次实现是工具准入控制，不是操作系统级进程沙箱。** 权限接口明确返回：

```json
{
  "sandbox_enforcement": "tool_admission_only",
  "native_os_sandbox_available": false,
  "os_privileges_unchanged": true
}
```

本机原生 `read_file`、`list_dir`、`search_text` 与可解析目标的 `file_edit` 可以按文件权限和工作区目标检查。文件移动检查源和目的路径；结构化补丁检查全部目标；工作区限制不接受 `external_path` 作为豁免。已有符号链接解析和派发前复验继续生效；工作区根目录本身不能作为受限写入目标，受限写入也不能修改或搬移 AgentDockHome 及其祖先目录，从而避免修改自己的权限与审查配置。

任何受限 Profile 都不会把任意 shell 命令、第三方 MCP、WSL、浏览器或无法证明副作用边界的媒体工具当作已隔离操作。此类调用拒绝派发，而不是审批后在宿主全权限重试。上下文加载、插件加载与 MCP 动态发现可能启动外部进程或联网，现阶段同样保守拒绝；需要在应用受限配置前完成所需工作区与能力信息准备。工作区 get/list/resolve、会话观察、终止已有会话和可恢复任务元数据等受信控制面操作仍按原模式与规则处理，不等同任意文件访问。

受限搜索使用已有的有界 Go 引擎，不启动 PATH 中的 `rg`；受限列表/搜索不读取隐式 `.gitignore`、`.git/info/exclude` 或 worktree 的外部 Git 配置，仍应用内置隐藏目录规则与请求显式 glob。因此受限模式的默认搜索结果可能与 Git 忽略语义不同。受限补丁只走原生结构化补丁路径，不启动外部 Git。

`network=deny` 不是系统防火墙，不修改代理、路由或其他进程；明确 UNC 路径按网络访问处理，但操作系统已有挂载/映射盘及其他进程的网络活动不由本层隔离。文件检查也不是针对同一 OS 用户恶意并发改动、硬链接、挂载切换的内核安全保证。要执行任意命令且强制网络/文件隔离，仍需另行接入并验证真正的 OS/容器沙箱后端；本 PR 不伪报这种能力。

策略变更使未派发的旧请求/审批失效，不会自动终止此前已经启动的进程；终止操作仍由原有停止流程负责。审批不能增加操作系统权限。

## 5. 自动审查：配置与协议

`auto_review` 不是内置模型，也不会自动调用当前 Agent、ACP、Codex 或自动点击人工审批按钮。它调用管理员单独配置的可信审查适配程序。该程序可以自行接入模型或规则引擎；它属于受信控制面，必须由管理员保护可执行文件、配置和凭据。**本 PR 不安装任何真实审查程序、不写生产配置、不发送真实请求到模型供应商。**

配置文件位置：

```text
<AgentDockHome>/execution/permissions/auto-review.json
```

示例（路径必须替换为管理员实际部署的程序，示例不能直接运行）：

```json
{
  "command": "/opt/agentdock-reviewer/bin/reviewer",
  "args": ["--policy", "/etc/agentdock-reviewer/policy.json"],
  "timeout_ms": 10000,
  "env_from_env": {
    "REVIEWER_API_KEY": "AGENTDOCK_REVIEWER_API_KEY"
  }
}
```

Windows 使用绝对 EXE 路径。不会隐式启动 shell，也不会继承整个宿主环境；仅基本运行环境和 `env_from_env` 明确映射的值进入子进程。适配程序必须把收到的工具文本当作待审数据，不作为指令执行，不能运行原操作，也不能通过自称安全、提示注入或缺少凭据值的情况推断批准。需要更多信息而当前脱敏请求不足时，应拒绝。

stdin 为一个 JSON 对象：

```json
{
  "schema_version": 1,
  "approval": {
    "approval_id": "apr_example",
    "call_id": "call_example",
    "approval_reviewer": "auto_review",
    "policy_revision": 3,
    "tool": "file_edit"
  },
  "fixed_request": "{\"action\":\"add\",\"path\":\"example.txt\",\"content\":\"example\"}",
  "redacted": true
}
```

实际 approval 对象还携带原有绑定、固定请求摘要、权限快照等字段。ID 以服务器给出的实际值为准；示例仅说明结构。

stdout 必须是一个完整 JSON 对象，退出码为 0：

```json
{
  "approval_id": "apr_example",
  "call_id": "call_example",
  "decision": "approve",
  "reason": "基于完整固定请求给出的独立判断依据"
}
```

`decision` 仅支持 `approve` 或 `reject`，两个 ID 必须精确匹配。未知字段、重复键、尾随额外 JSON、空理由、错绑或超限输出都拒绝。标准错误不进入审批正文。限额：配置 16 KiB；最多 32 个参数/合计 8 KiB；完整固定请求最多 32 KiB；stdout 最多 8 KiB；理由最多 2048 UTF-8 字节；超时 100–30000 ms；每个 Store 实例最多两个并行审查进程。完整请求无法提供时直接拒绝，不使用截断请求推断批准。

缺少配置、无法启动、超时、取消、无效协议、审查拒绝均不执行原操作。审查只提供建议，不能自行派发：结果先脱敏并写入原审批记录，再核对策略修订、固定目标、审批指定主体，最后对同一原始 CallID 做一次性 Claim。自动审查不得生成永久工作区 Allow 规则；人工批准接口不能冒充自动审查。用户仍可以拒绝未完成的自动审查请求。

审批记录新增 `approval_reviewer`、`permission_settings`、`review_decision`、`review_reason`、`reviewed_at`，`decided_by` 保留实际决定者，异步完成不会覆盖身份。正向自动审查返回实际原工具结果，而非伪造“执行成功”；重放已处理审批不会再次执行。

## 6. 管理 API 与界面

复用现有本地管理权限接口及其认证/来源检查，不向 MCP 工具 Schema 开放改变 Profile 或指定 Reviewer 的参数。保存请求示意：

```json
{
  "scope": "workspace",
  "scope_id": "wsp_actual_id",
  "expected_revision": 3,
  "settings": {
    "permission_profile": {"filesystem": "read", "network": "deny", "sandbox_boundary": "workspace"},
    "approval_policy": {"mode": "never"},
    "approval_reviewer": "user"
  }
}
```

继承请求用 `inherit_settings: true`，不得同时提供 settings。保留原有 mode/rules 保存契约和 full 的本地明确确认。

Windows“执行权限”窗口增加三层控件、granular 类别和工作区继承选择。必须勾选修改三层设置才随保存请求提交，单纯打开窗口不迁移或扩大权限。审批详情显示指定主体与独立审查理由；自动审批不显示可用的“允许一次/永久授权”人工替代入口。

## 7. 验证范围与交付边界

新增回归覆盖 Profile 矩阵、显式 Deny 优先、未知副作用拒绝、旧策略升级、继承隔离与重置、granular 多类别、never 不入队、文件与符号链接越界、受限搜索不使用外部帮助程序、审批修订失效、自动审查成功/拒绝/缺配置/超时/无效返回/重复 JSON 键、身份绑定、敏感环境不隐式继承、并发一次性派发及结果审计。

Windows 控件有离屏初始化、序列化、granular 显隐、继承、旧数据和对话禁用回归。离屏运行仅在 GitHub Actions 执行；本机只构建界面并运行不启动应用的纯逻辑测试。

独立 `permission-profiles.yml` 在权限分支/相关 PR 上做 Linux、Windows 验证，权限仅 `contents: read`。它不打安装包、不部署、不发布、不合并 PR。实际验证结果以本次 PR 记录和 CI 为准；本文件不把尚未完成的检查写成通过。
