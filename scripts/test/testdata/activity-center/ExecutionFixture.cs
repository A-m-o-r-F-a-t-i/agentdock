using System.Net;
using System.Text;
using System.Text.Json;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private sealed partial class LocalFixture
    {
        internal bool ExecutionMode { get; set; }
        internal const string ConversationA = "conv_11111111111111111111111111111111";
        internal const string ConversationB = "conv_22222222222222222222222222222222";
        internal const string ReadCall = "call_11111111111111111111111111111111";
        internal const string PendingCall = "call_22222222222222222222222222222222";
        internal const string ApprovalId = "approval_11111111111111111111111111111111";
        internal string[] LastBatchIDs { get; private set; } = [];
        internal string LastBatchAction { get; private set; } = "";
        private int _callUpdate;
        internal int BranchReads {get;private set;}
        private object ExecutionCall(bool pending = false) => new
        {
            schema_version = 2, call_id = pending ? PendingCall : ReadCall, conversation_id = ConversationA,
            task_id = pending ? TaskId : "", thread_id = pending ? "main" : "", workspace_id = "wsp_fixture",
            created_seq = pending ? 2 : 1, updated_seq = pending ? 2 : Volatile.Read(ref _callUpdate) > 0 ? 4 : 1,
            created_at = "2026-09-20T14:00:00Z", updated_at = "2026-09-20T14:00:02Z", elapsed_ms = 126,
            tool_name = pending ? "exec_command" : "read_file", title = pending ? "运行命令 · 核对Windows编译" : "读取文件 · 执行记录模型.go",
            status = pending ? "pending_approval" : "succeeded", approval_id = pending ? ApprovalId : "",
            summary = pending ? "需要确认命令及工作区，当前尚未执行。" : "已读取指定范围。完整文件正文不在执行日志中重复保存。",
            display_command = pending ? "go test ./internal/app" : "", workdir = _root, output_preview = "隔离测试输出🙂\n第二行\n",
            stderr_preview = "", stdout_truncated = false, stderr_truncated = false,
            file_changes = Array.Empty<object>(), rule_id = pending ? "review-side-effects" : "builtin-safe"
        };
        private object Conversation(string id, string title) => new
        {
            conversation_id = id, title, source = "chatgpt", attribution = "host_metadata", state = new { active_task_id = id == ConversationA ? TaskId : "", active_task_thread_id = id == ConversationA ? "main" : "", workspace_id = "wsp_fixture", binding_revision = 1 }, updated_at = "2026-09-20T14:00:00Z", created_at = "2026-09-20T14:00:00Z",
            task_ids = id == ConversationA ? new[] { TaskId } : [], workspace_ids = new[] { "wsp_fixture" }, tags = new[] { "AgentDock", "执行中心" },
            statistics = new { total = 2, running = 0, pending = id == ConversationA ? 1 : 0, failed = 0, unknown = 0 }, pinned = id == ConversationA
        };
        private async Task<bool> RespondExecutionAsync(HttpListenerContext context)
        {
            if (await RespondExecutionScaleAsync(context)) return true;
            var path = context.Request.Url!.AbsolutePath;
            if (path == "/internal/runtime/execution")
            { await JsonAsync(context, new { statistics = new { total = 3, running = 0, pending = 1, failed = 0, unknown = 0 }, permission_mode = "rules", policy_revision = 1 }); return true; }
            if (path == "/internal/runtime/permissions/effective")
            { await JsonAsync(context, new { policy = new { schema_version = 1, revision = 1, global_mode = "rules", scopes = Array.Empty<object>(), rules = Array.Empty<object>() }, effective = new { mode = "rules", scope = "global", revision = 1 }, workspaces = new[] { new { workspace_id = "wsp_fixture", name = "隔离工作区", root = _root } } }); return true; }
            if (path == "/internal/runtime/conversations")
            {
                await JsonAsync(context, new { conversations = new[] { Conversation(ConversationA, "执行中心升级 · 当前对话"), Conversation(ConversationB, "独立并行对话 · 不串线") }, total = 2, next_offset = 2, has_more = false, selected_ids = new[] { ConversationA, ConversationB } }); return true;
            }
            if (path == "/internal/runtime/conversations/" + ConversationA || path == "/internal/runtime/conversations/" + ConversationB)
            { var id = path.Split('/')[^1]; await JsonAsync(context, new { conversation = Conversation(id, id == ConversationA ? "执行中心升级 · 当前对话" : "独立并行对话"), permission = new { mode = "rules" } }); return true; }
            if (path == "/internal/runtime/execution/tasks")
            { await JsonAsync(context, new { tasks = new[] { new { id = TaskId, title = "实现任务与执行中心", status = "active", step_count = 3, completed_steps = 1, workspace_id = "wsp_fixture", tags = new[] { "P0" } } }, total = 1, next_offset = 1, has_more = false, selected_ids = new[] { TaskId } }); return true; }
            if(path=="/internal/runtime/tasks/"+TaskId+"/threads/main") { await JsonAsync(context,new {thread=Threads()[0]});return true; }
            if(path=="/internal/runtime/tasks/"+TaskId+"/activity") { await JsonAsync(context,new {events=context.Request.QueryString["after"]=="0" ? new[]{new{seq=10,kind="task.checkpoint",status="success",created_at="2026-09-20T14:00:00Z",summary="任务检查点：界面记录聚合已验证。"}}:[],next_seq=10,latest_seq=10,has_more=false,gap=false});return true; }
            if(path=="/internal/runtime/tasks/"+TaskId+"/threads/"+BranchId){BranchReads++;await JsonAsync(context,new {thread=Threads().Cast<ActivityThread>().Single(thread=>thread.Id==BranchId)});return true;}
            if (path == "/internal/runtime/calls")
            {
                var byTask = context.Request.QueryString["task_id"] == TaskId;
                var otherConversation = context.Request.QueryString["conversation_id"] == ConversationB;
                var child = context.Request.QueryString["parent_call_id"];
                var calls = child is { Length: > 0 } || otherConversation ? Array.Empty<object>() : byTask ? [ExecutionCall(true)] : new[] { ExecutionCall(true), ExecutionCall() };
                await JsonAsync(context, new { calls, latest_seq = 3, next_seq = 3, next_before = 1, pruned_through = 0, has_more = false, gap = false }); return true;
            }
            if (path == "/internal/runtime/calls/" + ReadCall || path == "/internal/runtime/calls/" + PendingCall)
            { await JsonAsync(context, ExecutionCall(path.EndsWith(PendingCall, StringComparison.Ordinal))); return true; }
            if (path == "/internal/runtime/calls/stream")
            {
                Interlocked.Increment(ref _activeStreams);
                try
                {
                    context.Response.ContentType = "text/event-stream; charset=utf-8"; context.Response.SendChunked = true;
                    Interlocked.Exchange(ref _callUpdate, 1);
                    Cursors.Enqueue(context.Request.Headers["Last-Event-ID"] ?? "");
                    await WireAsync(context, "retry: 1000\n\n");
                    if (!ExecutionScaleMode && context.Request.QueryString["conversation_id"] == ConversationA)
                    {
                        var value = JsonSerializer.Serialize(ExecutionCall(), ActivityClient.JsonOptions);
                        await WireAsync(context, "id: 4\nevent: call\ndata: " + value + "\n\n");
                        await WireAsync(context, "id: 4\nevent: call\ndata: " + value + "\n\n");
                    }
                    while (!_stop.IsCancellationRequested) { await Task.Delay(100, _stop.Token); await WireAsync(context, ": heartbeat\n\n"); }
                }
                finally { Interlocked.Decrement(ref _activeStreams); }
                return true;
            }
            if (path.EndsWith("/batch", StringComparison.Ordinal))
            {
                using var body = await JsonDocument.ParseAsync(context.Request.InputStream);
                LastBatchIDs = body.RootElement.Array("ids").Select(item => item.GetString()!).ToArray(); LastBatchAction = body.RootElement.Text("action");
                await JsonAsync(context, new { items = LastBatchIDs.Select(id => new { id, status = "succeeded", message = "管理元数据已更新，源码未改动。" }), succeeded = LastBatchIDs.Length, failed = 0, skipped = 0, status = "succeeded" }); return true;
            }
            return false;
        }
    }
}
