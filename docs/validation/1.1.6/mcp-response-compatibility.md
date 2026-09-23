# AgentDock 1.1.6：MCP 响应与呈现协议阶段验证

本阶段位于 P0 上下文提交 `a9016d5b068fd0215136903e030b96666a28024e` 之后。源码及隔离测试已完成，不代表 1.1.6 的活动中心、MCP 批量目录、编辑统计、界面、GitHub 构建或正式发布全部完成。

## 插入双通道

SDK `callTool` 和直接 `Invoke` 统一经过 `finishResponse`。原始业务 envelope 先验证可编码且 SDK 可解码，再完成本地队列的响应绑定。可信补充由本地队列构造，同时进入尾部 `AGENTDOCK_USER_INSERT_V1` 文本块和顶层 `structuredContent.agentdock_guidance.response_additions`，两者具有相同 insertion_id。

保留原结构化业务字段、错误标记、图片、资源、已有 AgentDock guidance 字段和内容块顺序。第三方 `result` 内的同名字段和仿造文本不被提升为可信消息。已完成 ToolResponse 保存防御性副本，响应重新装配不会消费另一条队列记录；普通业务重调也不会再次领取已 attached 的消息。

原有 180 秒插入资格、300 秒队列期限、下一次根调用领取及任务/对话切换隔离不变。界面将 attached 显示为“已写入工具响应”，不声称模型收到或已读。

## TextOnly / App 单一呈现状态

显示配置的同一次快照派生不可变 mode、revision、资源及内容哈希。App 先注册可读取资源，再发布工具引用；TextOnly 先切换出站过滤代际，再撤下目录资源和工具模板声明。SDK 的目录变更通知、无状态 HTTP 后续请求和 Bridge 契约哈希使用当前代际。正在运行的调用在返回边界重新应用当前模式，不依赖开始时的显示状态。

TextOnly 清理已知协议元数据位置中的标准及历史模板字段，保留鉴权、文件重写及 app-only visibility 等限制。动态 MCP 的标准 result envelope 在明确的转发边界处理一次，不递归改写第三方结构化业务对象或把业务字段误当成用户指令。

升级或关闭显示后，先前发行版内建模板的精确 URI 白名单具有 30 分钟只读兼容期。兼容内容不列入正常 resources/list，只有无脚本、无外部网络、无操作能力的静态提示；未知 URI 和过期 URI 返回 not found。白名单不是任意 ui:// 路径代理，也不允许读取本地文件。当前旧模板窗口按运行时生命周期计算，宿主采纳状态保持 unknown，不宣称 AgentDock 能证明外部宿主已刷新缓存。

## 实际验证

- 普通成功与业务失败分别在真实内存传输 SDK 路径和直接 Invoke 路径测试，插入同时进入文本及结构化结果，后续调用不重复消费。
- 文本、纯结构化、空结构化、标量、错误、图片、资源、动态 MCP 仿造同名字段均有保留与重放测试；附加过程不原地修改共享结果对象。
- TextOnly 宿主模拟 tools/list 与三个正常工具调用，模板读取计数为 0；资源目录为空，业务文本及结构化数据保留。
- 同一 SDK 会话 App→TextOnly→App 切换、revision 和 Bridge 契约变化、精确旧 URI 惰性读取、过期与未知 URI 拒绝，以及恢复 App 后每个模板可读取均通过。
- `go test ./internal/mcp ./internal/app ./internal/nexusbridge -p 2 -count=1 -timeout=8m`：全部通过。包测试运行时间分别 11.254 s、70.786 s、0.824 s；这些不是单次工具性能数据。

没有自动安装、启动真实桌面界面、替换运行中的 AgentDock 或宣称真实 ChatGPT 网页已完成跨版本缓存验收。实际模型是否消费了插入文本不通过服务端 attached 状态推断。
