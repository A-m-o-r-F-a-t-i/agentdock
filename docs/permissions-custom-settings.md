# 自定义权限设置与首次安装默认值

本页记录 AgentDock Workbench 权限策略 schema 3 的稳定语义。权限策略只决定 AgentDock 是否派发一个已经固定的请求；它不会提升操作系统权限，也不是操作系统沙箱。

## 两层控制关系

执行模式仍是基础控制层：`readonly`、`rules`、`full`。`custom_permissions_enabled` 是独立的显式开关。

- `false`：`Permission Profile`、`Approval Policy` 与 `Approval Reviewer` 的已保存值继续保留在 `configured_settings`，但不参与判定。实际 `settings` 使用执行模式对应的基线值，`settings_source` 为 `execution_mode`。
- `true`：当前作用域继承后得到的三层配置参与判定，实际 `settings` 等于 `configured_settings`，`settings_source` 为 `custom_permissions`。
- 显式 `deny` 规则始终先于执行模式和三层配置。
- Profile 是硬上限；Reviewer 与审批结果不能扩大 Profile。
- Approval Policy 的 `never` 表示不受理需要审批的操作，不表示自动批准。

服务端返回的有效对象同时包含：

- `custom_permissions_enabled`
- `custom_permissions_scope` / `custom_permissions_scope_id`
- `settings`：本次判定实际使用的值
- `configured_settings`：保存的历史值
- `settings_source`
- `settings_scope` / `settings_scope_id`
- `mode`、`scope`、`scope_id`、`revision`

客户端不得自行重算有效权限，应以服务端有效对象为准。

## 作用域与继承

执行模式仍支持全局、工作区、对话三级继承。自定义权限开关和三层配置只允许全局或工作区配置；对话不能启用或扩大自定义权限。

工作区记录可以只覆盖开关、只覆盖三层配置，或同时覆盖。`inherit_settings=true` 会删除该工作区的本地开关和本地三层配置，但保留同一作用域已有的执行模式。关闭开关不会删除历史三层值。

## schema 1/2 兼容迁移

读取旧策略时只做内存归一化，不因读取而改写 `policy.json`：

| 旧记录 | schema 3 解释 |
| --- | --- |
| 全局存在显式 `settings` | 全局 `custom_permissions_enabled=true` |
| 全局没有显式 `settings` | 全局 `custom_permissions_enabled=false` |
| 工作区存在显式 `settings` | 该工作区 `custom_permissions_enabled=true` |
| 工作区没有显式 `settings` | 继承全局开关 |

下一次通过 revision 校验的成功写入会原子持久化 schema 3。旧客户端若提交显式 `settings`、但没有提交新开关，视为启用该显式设置，避免旧配置突然失效。

## 首次干净安装

`permission.FreshInstallPolicy()` 是干净安装默认值的单一来源：

- `global_mode=full`
- `custom_permissions_enabled=false`
- revision 1、schema 3
- 保留默认三层历史值
- 空规则与空作用域

`permission.InitializeFreshInstall(ctx, root)` 只在 `policy.json` 不存在时原子创建该策略；已有策略保持原字节和原语义。**安装器必须先独立证明这是全新安装**，不能仅凭 `policy.json` 缺失推断为新装。升级、修复、回滚、已有 AgentDock Home 或无法确认来源时不得调用此入口。

真实 Setup/Installer 接线属于安装工作线；本权限线不修改安装、回滚或生产环境代码。

## HTTP 保存与权威读回

现有路径保持兼容：

- `GET /internal/runtime/permissions/effective`
- `POST /internal/runtime/permissions`

写入继续要求认证与 `expected_revision`。成功提交后，POST 同次返回保存后的 `policy` 和目标作用域的权威 `effective` 读回，并设置 `effective_readback_available=true`。若提交已成功但读回异常，返回已提交的策略并明确标记读回不可用，客户端不得盲目重试同一 revision。
