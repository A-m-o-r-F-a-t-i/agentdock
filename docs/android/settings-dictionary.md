# Android 设置词典与存储归属

客户端 DataStore 保存偏好或编辑草稿，不能代替 Core 的最终有效配置。以下字段来自 WorkbenchSettings 与 SettingsStore 的实际读写。布尔默认值用 true/false，空 URI 用空字符串。尚无消费者的字段明确标注未生效。

| 字段/组 | 默认值或范围 | 唯一有效归属 | 当前消费者/边界 |
| --- | --- | --- | --- |
| theme | system；light/dark | APK DataStore | WorkbenchTheme 实际生效 |
| density | comfortable；compact | APK DataStore | 页面内边距生效，完整组件密度适配仍待补 |
| language | system | APK 偏好 | 仅保存，文字本地化未实现 |
| endpoint、remoteEndpointEnabled | http://127.0.0.1:8765；false | APK 连接配置 | CoreClient校验Origin；远程必须显式HTTPS；Bearer仅在绑定Origin生效 |
| notificationsEnabled | true | APK 偏好＋系统授权 | 请求POST_NOTIFICATIONS；不能静默替代系统决定；可选通知开关消费未完整 |
| guardianEnabled、guardianPaused | false、false | APK DataStore | GuardianScheduler、前台服务和Tile消费；暂停不停止Core |
| autoRepairEnabled | false | APK意图＋Termux进程事实 | 仅desired=running时请求受限恢复；持久熔断未完成 |
| bootHealthCheckEnabled | false | APK偏好＋系统准入 | BootReceiver调度延后检查，不直接拉起Core |
| guardianIntervalMinutes | 15；15–1440分钟 | APK DataStore | WorkManager与前台检查周期 |
| onlyOnWifi、onlyWhileCharging | false、false | APK调度约束 | WorkManager已消费；前台服务路径未完整消费 |
| allowMobileData | false | APK下载偏好 | 仅保存，下载策略接线未完成 |
| publicAccessEnabled | false | Core/公网服务负责实际配置 | APK仅保存占位意图，不会创建或停止任何隧道 |
| detailedCalls | false | APK显示偏好 | 调用原始详情按开关展示 |
| toolOutputEnabled | true | APK显示限制；Core仍决定服务端输出 | 关闭后Repository拒绝payload读取 |
| toolOutputMaxChars | 20000；1000–100000 | APK读取上限＋Core计数契约 | 作为limit_chars传给Core；中文/emoji跨端计数回归未完成 |
| customPermissionEnabled | false | Core custom_permissions_enabled | 本地值仅为编辑草稿；保存需服务端字段支持和revision |
| permissionFilesystem/Network/Boundary | write/allow/none | Core permission_profile | 只在保存后以Core最终值为有效权限；工作区继承编辑未完整 |
| approvalPolicy、approvalReviewer | on-request、user | Core approval_policy/reviewer | never不解释成授权全部；granular子项由Core决定 |
| granularFileWrites/Commands/Network/Mcp/Management | true | Core审批类别 | APK保存编辑草稿 |
| granularOther | false | Core审批类别 | APK保存编辑草稿 |
| desiredNodeState | stopped | APK意图＋Termux desired-state | 停止先保存意图；实际进程结果单独确认 |
| projectTreeUri、artifactTreeUri | 空字符串 | Android SAF授权 | 不转换或猜测Termux真实路径；双端授权仍需探针 |
| onboardingComplete | false | APK引导偏好 | 完整引导状态机未接线，不用于伪报节点READY |
| schemaVersion | 1 | APK格式 | 当前默认常量，不是Core协议版本 |

## 秘密与运行状态

Core Bearer、配对及公网秘密不进入本词典对应DataStore。CredentialStore使用Android Keystore AES-GCM包封应用私有密文；桥回执不传凭据，旧桥包含凭据字段时拒绝导入。Origin的scheme、host和实际port与Bearer一并保存在密文中，仅同一Origin可取出。切换地址不会沿用旧凭据，旧未绑定密文保留但不发送，需在明确的Core连接配置中重新保存。本机删除不等于服务器撤销。自动配对仍待独立契约实现。

任务、对话、调用、审批、插入和最终有效权限只从Core读取。页面资源ID、已提交筛选、返回路径、插入草稿使用SavedStateHandle/Compose状态恢复，恢复后重新读取Core。Termux操作摘要独立保存在APK私有目录，不复制Core业务数据库。

## 数据保留

卸载APK会失去其私有配置和Keystore材料，外部Termux节点与用户工程不由APK卸载流程删除。SAF URI授权、Termux目录授权和实际项目文件是不同对象。候选没有自动清理用户工程/身份入口；安装journal和schema兼容回退尚未完成前，不自动清理旧版本恢复点。
