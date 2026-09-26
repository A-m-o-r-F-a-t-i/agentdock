# WB07 开发与验收状态

本分支仍为候选开发线，不表示 Android 完整产品验收完成。产品版本从 Core 的 buildinfo 读取，候选标识使用源码提交、Actions run 和 attempt。测试签名 APK 不进入生产发布。

## 本轮实现

| 模块 | 已实现行为 | 验证入口 |
| --- | --- | --- |
| 任务与对话 | 接入真实管理列表路径、工作区/状态/标签/关键字筛选、显式分页，每次只保留一页 | ManagementContractTest、CoreClientHttpTest |
| 批量管理 | 冻结明确的对象 ID，支持置顶、标签、重命名、归档、回收站、恢复和永久删除确认；逐项呈现跳过/失败 | ManagementContractTest、ManagementPage |
| 错误处理 | HTTP 200 的业务拒绝不再标成成功，分页缺失与真实空列表分离，SSE 鉴权错误终止且冷失败次数有上限 | CoreClientHttpTest、ManagementContractTest |
| 调用详情 | 列表不预载输出；请求/响应/源输出按 Core 字节游标分段读取；移除不存在的调用重试动作 | WorkbenchRepository、Pages |
| 客户端状态 | 保存选中对象、已提交筛选、返回路径和插入草稿；连接变更取消旧订阅；Bearer 输入不进入保存状态 | WorkbenchNavigationTest |
| 写入隔离 | Fixture 模式拒绝真实 Core 写入、Termux 操作、守护启动和连接凭据编辑 | WorkbenchNavigationTest |
| 凭据作用域 | Bearer 与 Origin 一起加密保存，不跨 scheme/host/port 发送；旧未绑定密文保留但不发送 | CoreCredentialBindingTest、CoreCredentialStoreTest |
| 权限协作 | 按 WB02 的 custom_permissions_enabled 字段提交开关；未整合该字段的 Core 明确返回 pending_integration | WorkbenchViewModel.savePermissions |
| 仪器测试 | Espresso 固定至 3.7.0，测试全部 16 个入口并采集空态/失败态；移除无条件重跑 | parallel-android.yml、run-emulator-tests.sh |

## 证据标准

Android 编译、Lint、单元测试及仪器测试只在 GitHub Actions 执行。模拟器覆盖 API 26、33、34、35、37。每个模拟器必须记录真实 API、至少 9 项通过的仪器测试和全部 18 张规定截图，缺失报告或截图会失败。传输测试使用实际 loopback HTTP 加固定响应，不能替代真实 Core/Termux 联调。

## 本轮回归补充

Termux 回执覆盖年龄边界、终态重放、嵌套凭据字段、错误文本脱敏及 UTF-8 字节上限。操作存储测试验证“回执先完成、调度后返回”不会将终态改回运行中，未决操作也不会被容量清理删除。桥脚本回归检查只读探测不建节点目录、未知 PID 不收信号，以及 9 种归档输入。

截图由受控模拟器的 UiAutomation shell 写入专用截图目录，规避目标进程无权写测试 APK 私有目录的问题，不给产品增加存储权限。返回栈回归调用 Activity 的 OnBackPressedDispatcher 并断言真实页面回退；系统手势和厂商返回交互仍需真机验证。

Origin 绑定新增 6 项 JVM 回归和 2 项真实 Keystore 仪器测试，覆盖更换节点、默认端口、非法输入、旧密文保留和密文不含凭据明文。本机删除只清除本地材料，服务器撤销需要对应服务能力。

## 尚未完成

- 外部 Termux 桥已补只读探测、PID/启动时间/boot ID/进程组核验、凭据不出回执、安全解包和下载限额。完整事务恢复、schema备份与回退、单回退版本清理及恢复限次仍需补齐；不能据此接管现有手机节点。
- Intent 明文凭据交接已删除，旧桥的凭据字段会被拒绝。独立的受控自动配对通道仍待共享接口集成，当前保留显式连接配置。
- 工作区创建/完整管理、调用父子树与历史游标、审批历史、插件完整配置、项目文件实际导入导出仍需完成各自交互与集成验证。
- 权限的工作区作用域、有效值与配置值的完整编辑回读等待完成 WB02 对接；未合并任何其他分支。
- 设置词典和逐项 Windows 对照矩阵已提供；语言实际切换、前台守护网络/充电约束、横屏/平板/大字体/深色截图及设置效果回归仍需补齐。
- Linux ARM64 签名清单、真实 Core 联调、手机实际 RUN_COMMAND、30 分钟/2 小时/8 小时保活及用户设备安装均未验收。本轮不修改现有手机节点。

不得将页面可打开、测试签名 APK 构建通过或 Fixture 截图通过等同于以上未完成项通过。
