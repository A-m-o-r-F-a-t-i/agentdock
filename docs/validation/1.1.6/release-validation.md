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

GitHub Actions 的 Windows、Linux、race、离屏、静态安装契约、打包和校验尚未在写入本文件时完成；对应 run ID 将在候选通过后补入验收矩阵。没有运行安装器、启动新版桌面界面或替换生产 AgentDock。
