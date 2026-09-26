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
| 写入隔离 | Fixture 模式拒绝真实 Core 写入、Termux 操作和守护启动 | WorkbenchNavigationTest |
| 权限协作 | 按 WB02 的 custom_permissions_enabled 字段提交开关；未整合该字段的 Core 明确返回 pending_integration | WorkbenchViewModel.savePermissions |
| 仪器测试 | Espresso 固定至 3.7.0，测试全部 16 个入口并采集空态/失败态；移除无条件重跑 | parallel-android.yml、run-emulator-tests.sh |

## 证据标准

Android 编译、Lint、单元测试及仪器测试只在 GitHub Actions 执行。模拟器覆盖 API 26、33、34、35、37。每个模拟器必须记录真实 API、非零测试数量和全部 18 张规定截图，缺失报告或截图会失败。传输测试使用实际 loopback HTTP 加固定响应，不能替代真实 Core/Termux 联调。

## 尚未完成

- 外部 Termux 桥的完整事务恢复、进程身份确认、安全归档解包、只读发现和自动恢复限次仍需补齐。当前初始脚本不作为真机部署验收结果。
- 凭据自动交接需改用不经过 Intent 明文结果的受控通道，并完成协议回归；当前源码仍需安全收敛。
- 工作区创建/完整管理、调用父子树与历史游标、审批历史、插件完整配置、项目文件实际导入导出仍需完成各自交互与集成验证。
- 权限的工作区作用域、有效值与配置值的完整编辑回读等待完成 WB02 对接；未合并任何其他分支。
- 语言切换、完整设置字典、前台守护网络/充电约束、横屏/平板/大字体/深色截图和完整 Windows 对照矩阵仍需补齐。
- Linux ARM64 签名清单、真实 Core 联调、手机实际 RUN_COMMAND、30 分钟/2 小时/8 小时保活及用户设备安装均未验收。本轮不修改现有手机节点。

不得将页面可打开、测试签名 APK 构建通过或 Fixture 截图通过等同于以上未完成项通过。
