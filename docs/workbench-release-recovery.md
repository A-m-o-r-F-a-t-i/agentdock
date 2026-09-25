# AgentDock Workbench：已验证构建的恢复发布

　　全平台构建使用 `workbench-release.yml`。只有源码解析、Linux/macOS 两种架构的原生测试与打包、Windows 双架构包验证以及通用 macOS App 验证全部成功，才组装完整发行资产。构建、安装测试、上传与正式发布分别记录结果。

## 1. 草稿定位与上传

　　GitHub 的 `GET /repos/{owner}/{repo}/releases/tags/{tag}` 用于读取已发布版本。草稿通过已认证的 releases 列表按标签定位，随后使用数字 Release ID 读取与发布。列表读取失败、重复草稿或达到搜索上限时停止写入，不把查询失败当作“版本不存在”。官方接口说明见 https://docs.github.com/en/rest/releases/releases 。

　　首次创建使用 REST POST 返回的 Release ID，不再要求紧接着的列表查询必须已显示新草稿。已确认创建后，读取、逐项上传和发布全程使用该数字 ID，不把再次查询标签交给其他 CLI 路径。补传只上传缺失文件，HTTP 二进制上传使用经过编码的文件名和原始文件字节，返回的上传状态、大小和摘要必须匹配。

　　发布器核对远端标签与构建 SHA，再检查每项现存资产的名称、上传状态、大小和 SHA-256。已经匹配的文件不重复上传，仅补齐缺项。存在多余文件、重复名称或不同内容时保留草稿并停止，不覆盖原资产。完整校验成功后按数字 ID 更新为非草稿、非预发布，并指定 Latest，再独立读取 Latest 核对。

## 2. 恢复入口

　　构建及组装已经成功、发布阶段失败时，使用 `AgentDock Workbench Publish Verified Build`，对应 `.github/workflows/workbench-publish-existing.yml`。输入原 `run_id`、原始 40 位 `commit` 和不带 v 的 `version`。入口只允许用户 fork 的 main 分支执行，不操作上游仓库。

　　恢复工作流验证原工作流路径、仓库、SHA、完成状态、构建/验证任务及完整资产组装步骤。1.1.8 起额外核对两个 Windows 原生恢复任务与 ARM64 成品安装任务，总计十二个必须成功的作业，x64 安装及 Linux 包事务还须通过发行报告检查。随后下载该 run 的原始平台资产和报告，以原 SHA 独立检出的源码目录重新执行同一个发布器的完整组装、摘要和报告一致性检查。恢复脚本提交与构建源码提交分别写入 `publication-result.json`，不改动既有发行标签，不重新编译或混用其他 run 的安装包。

　　`publish-workbench.py --source-root` 显式指定原构建源码目录。版本、发行说明和素材从该目录读取，HEAD 与受 Git 管理的文件必须匹配。发布器所在的新脚本目录仅负责执行恢复逻辑，不冒充安装包来源。

## 3. 验证与中断处理

　　`python -B scripts/test/test-workbench-release.py` 使用临时文件与模拟命令覆盖完整资产、缺项、错摘要、跨提交报告、冲突副本，以及草稿列表分页、重复草稿、已发布版本拒绝修改、按 ID 发布和不重复上传。测试不会向 GitHub写入。

　　恢复成功以 `publication-result.json`、远端正式 Release/Latest 状态及完整资产摘要为准。原失败 run 保留真实失败记录，新恢复 run 单独记录成功，不把历史失败改写成构建成功，也不通过移动标签解决发布错误。
