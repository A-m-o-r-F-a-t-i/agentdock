# Android 实现基点与候选范围

仓库：A-m-o-r-F-a-t-i/agentdock。共享源码起点为 b367eaab95202873fb213b8713440bf7822878c4；本线为 parallel/20260925/android。Android 工程位于 mobile/android，未合并其他工作线。

| 对象 | 当前可核对状态 | 使用限制 |
| --- | --- | --- |
| 已有共享实现 | Windows ExecutionWindow.Actions.cs 提供任务/对话/调用批量管理、回收站、重命名、标签、关联、停止和导出入口 | Android 复用已存在的 Runtime HTTP 路径，不复制状态机 |
| Android 工程 | Kotlin/Compose 页面、Core HTTP/SSE、DataStore、Keystore、Termux 固定桥 | 具体功能状态见 android-feature-matrix.md |
| 已验证候选构建 | 6e5cdcf447d220233f758b37f4ede7709cfd47fc 的 Actions 36245969437 构建作业通过 | 该次模拟器回归失败，不能视为完整验收 |
| 旧手机原型 | 仅作为历史定位线索，未迁移接管或验证等价性 | 本轮不安装候选、不改正在运行的节点 |
| 待实现/集成 | 完整事务恢复、自动配对、共有管理缺口和真实 Core 联调 | 不以静态界面或模拟器截图代替 |

## 技术组合

| 参数 | 当前源码值 |
| --- | --- |
| AGP / Compose 编译插件 | 9.2.1 / 2.2.10 |
| Gradle / JDK | 9.4.1 / 17 |
| Compose BOM | 2026.09.00 |
| Espresso / AndroidX Test Runner | 3.7.0 / 1.7.0 |
| compileSdk / targetSdk / minSdk | 37 / 37 / 26 |
| 产品版本 | 从 internal/buildinfo/buildinfo.go 读取，当前为 1.1.7 |
| versionCode | 候选流水线的 101070000 + GITHUB_RUN_NUMBER |
| applicationId | dev.agentdock.workbench；debug 后缀 .candidate |
| APK 内容 | 不内嵌 Core 或原生库，不声明 ARM64-only APK |
| 外部 Core | Linux ARM64，运行在外部 Termux 的 Debian PRoot |
| 候选签名 | Android debug/test key，不是生产升级签名 |

源码、测试、APK 和截图必须对应同一远端 SHA。最终运行身份以候选 artifact 的 candidate.json/result.json 为准。手机不执行 Gradle、安装包或真机接管验收。
