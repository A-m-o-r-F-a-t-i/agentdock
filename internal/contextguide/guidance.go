// Package contextguide owns the shared bootstrap/reuse guidance. It contains
// no authorization decision, cached user content or per-call identifiers.
package contextguide

const Reuse = "首次接入、当前模型确实缺少所需规则或工作区规则变化时，调用一次 agentdock_context。complete=true 表示目标规则与必要的后续绑定协调已在本次请求完成，binding_updated=true 只说明绑定发生变更，不要求再次获取上下文。已取得同一规则作用域的完整上下文后直接继续业务工具，不因下一步、普通失败、短暂网络错误或宿主暂未展示工具而重新 bootstrap。只进入没有新增 AGENTS.md 的子目录时保持上下文并显式传实际路径；新规则作用域使用 workdir 定向刷新。运行中的命令观察原 session；MCP 目录或 Schema 变化只刷新对应服务。模型压缩后确实缺少内容或用户明确要求刷新时，允许重复请求，服务端会返回完整有效快照。不要逐次填写 conversation_id 或 task_id。"
const Description = "Return complete structured AgentDock bootstrap context with capabilities, rules and global/workspace AGENTS.md. Call once when necessary context is missing or the rule scope changes, then reuse the complete response. Workspace selection and continuation binding converge inside the same request; binding_updated never requires another bootstrap. Omitted workdir inherits the conversation workspace. Running sessions and device defaults remain unchanged. Explicit repeat requests return full valid content."
