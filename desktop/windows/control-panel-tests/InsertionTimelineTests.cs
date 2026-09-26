using System.Text.Json;
using AgentDock.ControlPanel;

internal static class InsertionTimelineTests
{
    private static JsonElement Json(object value) => JsonSerializer.SerializeToElement(value);
    internal static void Run(Action<bool, string> check)
    {
        var start = DateTimeOffset.Parse("2026-09-25T00:00:00Z");
        JsonElement Message(string status, int attempt = 1, int update = 2, string evidence = "", string reason = "outer_projection_failed", bool? manual = null) => Json(new {
            insertion_id = "ins_1", conversation_id = "conv_1", task_id = "task_1", text = "你好，我是帅哥\n后续要求",
            status, delivery_attempts = attempt, automatic_attempts_remaining = Math.Max(0,3-attempt), total_attempts_remaining = Math.Max(0,6-attempt),
            manual_retry_available = manual ?? ((status is "inner_appended" or "outer_forwarded" or "delivery_unknown") && attempt < 6),
            next_retry_at = attempt < 3 ? start.AddSeconds(30).ToString("O") : null,
            receipt_type = evidence.Length > 0 ? evidence : status == "outer_forwarded" ? "outer_forwarded" : "none", delivery_reason = reason,
            created_at = start.AddSeconds(1), updated_at = start.AddSeconds(update), expires_at = start.AddMinutes(5),
            receipt_token = "must-not-appear", owner = "private-owner", run_id = "private-run", tool_name = "exec_command", approval_id = "forged-approval", elapsed_ms = 100,
            acknowledged_by = evidence, acknowledged_at = status == "acknowledged" ? start.AddSeconds(update).ToString("O") : null,
            call_id = "call_original", outer_call_id = "outer_projection"
        });
        var a = new ExecutionCallRow(Json(new { call_id = "call_a", conversation_id = "conv_1", task_id = "task_1", created_at = start, created_seq = 1, tool_name = "read_file", status = "succeeded" }));
        var b = new ExecutionCallRow(Json(new { call_id = "call_b", conversation_id = "conv_1", task_id = "task_1", created_at = start.AddSeconds(3), created_seq = 2, tool_name = "list_dir", status = "running" }));
        var rows = InsertionTimeline.Merge([a,b], [Message("delivery_unknown"),Message("delivery_unknown")], "conv_1", "", "", "", "active", start.AddSeconds(5));
        check(rows.Select(row=>row.Id).SequenceEqual(new[]{"call_a","ins_1","call_b"}), "supplement is interleaved once by send time");
        var message = rows[1];
        check(message.IsInsertion && message.Title == "用户补充" && message.Duration == "" && message.Tool == "", "message is not a fabricated tool execution");
        check(!message.CanStop && !message.CanRetry && !message.NeedsApproval && !message.HasEditStatistics, "message never offers tool stop/replay/approval/statistics");
        check(message.CanRedeliverInsertion && message.CanCancelInsertion, "unconfirmed message allows bounded supplement-only controls");
        check(!message.Technical.Contains("must-not-appear") && !message.Technical.Contains("private-owner") && !message.Technical.Contains("forged-approval"), "timeline whitelist excludes receipt and executable control fields");
        check(message.InsertionDetails.Contains("你好，我是帅哥\n后续要求") && message.InsertionDetails.Contains("call_original") && message.InsertionDetails.Contains("outer_projection") && message.InsertionDetails.Contains("自动余量 2 次") && message.InsertionDetails.Contains("不能修复当前连接缺失的 insertion_ack 入口"), "message details retain content, correlation IDs and server-owned retry facts");
        check(message.InsertionPreview == "你好，我是帅哥 后续要求", "one-line preview does not alter original message");
        check(InsertionTimeline.Anchor(rows, 45, 36) == ("ins_1",9.0), "mixed-height scroll anchor includes message rows");
        check(InsertionTimeline.OffsetOf(rows,"call_b",36) == 98 && InsertionTimeline.OffsetOf(rows,"ins_1",36) == 36, "mixed-height restore keeps tool or message identity");
        check(InsertionTimeline.OffsetOf(rows,"missing",36) == null && InsertionTimeline.Anchor([],0,36).Id == "", "missing anchors and empty timeline remain explicit");
        for (var cycle=0; cycle<100; cycle++) rows=InsertionTimeline.Merge(rows,[Message("delivery_unknown",2)],"conv_1","","","","active",start.AddSeconds(6));
        check(rows.Count==3 && ReferenceEquals(rows[1],message) && rows.Count(row=>!row.IsInsertion)==2, "repeated updates retain one message and unchanged tool count");
        rows=InsertionTimeline.Merge(rows,[Message("outer_forwarded",2,3)],"conv_1","","","","active",start.AddSeconds(6));
        check(rows[1].State=="已转发，待上下文确认", "forwarded stage does not claim context receipt");
        check(InsertionPresentation.State(Message("inner_appended",reason:"awaiting_receiver_receipt"))=="已附加，等待接收回执", "ordinary attachment is distinct from host forwarding");
        check(InsertionPresentation.State(Message("inner_appended",reason:"awaiting_host_receipt"))=="已附加，等待宿主转发回执", "negotiated host attachment has its own state");
        var manualOnly=ExecutionCallRow.FromInsertion(Message("delivery_unknown",4,reason:"outer_projection_failed",manual:true),start.AddSeconds(6));
        check(manualOnly.CanRedeliverInsertion && manualOnly.InsertionHint.Contains("自动重投已结束；还可人工请求 2 次"), "desktop consumes server-owned manual-only budget without a local constant");
        rows=InsertionTimeline.Merge(rows,[Message("acknowledged",2,4,"receiver_receipt")],"conv_1","","","","active",start.AddSeconds(6));
        check(rows[1].State=="接收端已确认收到" && !rows[1].CanRedeliverInsertion && !rows[1].CanCancelInsertion, "receiver receipt stops retries without asserting host context commit");
        rows=InsertionTimeline.Merge(rows,[Message("pending",1,2)],"conv_1","","","","active",start.AddSeconds(6));
        check(rows[1].Status=="acknowledged", "older queue snapshot cannot roll back confirmed UI evidence");
        var committed=ExecutionCallRow.FromInsertion(Message("acknowledged",2,5,"host_context_committed"),start.AddSeconds(6));
        check(committed.State=="模型上下文已确认接收", "host context receipt uses its separate evidence label");
        foreach (var status in new[]{"pending","reserved","inner_appended","outer_forwarded","delivery_unknown","attached","expired","cancelled","target_changed"})
            check(!InsertionPresentation.State(Message(status)).Contains("已确认接收"), "nonterminal state must not imply model receipt: "+status);
        check(!ExecutionCallRow.FromInsertion(Message("delivery_unknown",6,manual:false),start.AddSeconds(6)).CanRedeliverInsertion, "total redelivery cap is visible");
        check(!ExecutionCallRow.FromInsertion(Json(new { insertion_id="ins_old",conversation_id="conv_1",text="old",status="delivery_unknown",delivery_attempts=1,created_at=start,updated_at=start,expires_at=start.AddMinutes(5) }),start.AddSeconds(6)).CanRedeliverInsertion, "older server records remain readable but do not fabricate retry policy");
        check(!ExecutionCallRow.FromInsertion(Message("delivery_unknown"),start.AddMinutes(5)).CanRedeliverInsertion, "expired message does not offer retry");
        check(!ExecutionCallRow.FromInsertion(Message("delivery_unknown")).CanRedeliverInsertion, "unknown server time cannot enable retry");
        foreach (var (conversation,task,view,status,search) in new[]{("other","","active","",""),("conv_1","other","active","",""),("conv_1","","trash","",""),("conv_1","","active","running",""),("conv_1","","active","","absent")})
            check(InsertionTimeline.Merge([a,b],[Message("delivery_unknown")],conversation,task,search,status,view,start).All(row=>!row.IsInsertion), "scope/filter excludes unrelated supplements");
        check(InsertionTimeline.Merge([a,b],[Message("delivery_unknown")],"conv_1","task_1","帅哥","unknown","active",start).Count(row=>row.IsInsertion)==1, "message text and unconfirmed filters are supported");
        check(ExecutionTitleFormatter.Format("plugin_load","Load a heavy plugin","")=="plugin_load · 展开插件", "historical default Heavy label becomes neutral expansion");
        check(ExecutionTitleFormatter.Format("plugin_load","展开 GitHub 技能","")=="plugin_load · 展开 GitHub 技能", "concrete plugin label remains intact");
        check(ExecutionTitleFormatter.Format("insertion_ack","Acknowledge received supplements","")=="insertion_ack · 确认收到补充", "receipt action has deterministic Chinese title");
    }
}
