# AgentDock 1.1.6：本地发布候选验证

源码提交：5f03d4fa59312b3365059c4902fa247b22dcadac。

| 验证 | 结果 |
| --- | --- |
| Windows go test -p 2 ./... -count=1 -timeout=8m | 通过 |
| go vet ./... | 通过 |
| internal/activity race | 通过 |
| 调用生命周期重点用例连续 5 轮 | 通过 |
| 离屏 WPF | 14,445 项断言、36 个渲染样本通过 |
| 桌面纯策略 | 674 项断言通过 |
| Windows 安装静态契约 | 通过 |
| Context 性能 | 960 样本、0 失败，全部门槛通过 |
| MCP 端到端性能 | 121 样本、0 失败 |

GitHub Actions 候选 35980716534 已通过，绑定提交 e4ea5c4fc4e0ae539c2f532cabddc3dde147148d；相对上述源码提交仅增加验证文档和样本。Windows 全量 Go 回归、go vet、桌面策略、离屏 WPF、静态安装契约、x64 ZIP/Setup 打包及元信息和校验和核对全部成功。Linux 全量回归及 snapshot/plugin/activity/wslfilehelper race 成功。安装和卸载步骤明确跳过，发布步骤因候选模式跳过。正式 tag 的构建与发布是独立阶段，以 v1.1.6 对应 Actions 和下载后的交付记录为准。没有运行安装器、启动新版桌面界面或替换生产 AgentDock。
