using System.Text.Json;

namespace AgentDock.ControlPanel;

public static class InsertionPresentation
{
    public static bool Unconfirmed(string status) => status is "inner_appended" or "outer_forwarded" or "delivery_unknown";
    public static string State(JsonElement item) => item.Text("status") switch
    {
        "pending" => "等待下一次工具调用",
        "reserved" => "已由下一次调用领取",
        "inner_appended" => "已写入内层响应，待转发",
        "outer_forwarded" => "已转发，待上下文确认",
        "acknowledged" => item.Text("acknowledged_by") == "host_context_committed" ? "模型上下文已确认接收" : "接收端已确认收到",
        "attached" => "历史内层响应已附加，接收未确认",
        "delivery_unknown" => "未确认收到",
        "target_changed" => "已暂停：目标任务或工作区变化",
        "expired" => "未领取，已过期",
        "cancelled" => "已停止投递",
        _ => "投递状态未记录"
    };

    public static string Reason(JsonElement item) => item.Text("delivery_reason") switch
    {
        "legacy_inner_response_without_receipt" => "旧版本只记录了内层附加，没有接收回执；不自动重发历史消息。",
        "receipt_missing_deadline_elapsed" => "有效期内没有收到确认，停止重投。需要继续使用时请重新发送。",
        "host_receipt_not_negotiated" => "当前宿主未声明端到端确认能力；后续新调用会在有效期和次数范围内重投补充。",
        "awaiting_host_receipt" => "等待宿主转发与上下文提交回执。",
        "awaiting_context_commit" => "外层已转发，但尚未确认进入模型上下文。",
        "process_restarted_before_receipt" => "服务重启前未获得确认，后续新调用可在剩余有效期内重投。",
        "inner_response_not_committed" => "内层响应没有确认提交，保留未送达状态。",
        "outer_projection_failed" => "外层结果投影失败，没有确认送达。",
        "context_commit_failed" => "外层上下文提交失败，尚未确认接收。",
        _ => ""
    };

    // Old clients and future fields cannot expose delivery secrets or create a
    // fake executable tool row. The UI keeps only the presentation whitelist.
    public static JsonElement Snapshot(JsonElement item)
    {
        var result = new Dictionary<string, object?> { ["record_kind"] = "insertion" };
        foreach (var key in new[] { "insertion_id", "conversation_id", "task_id", "thread_id", "workspace_id", "text", "status", "created_at", "updated_at", "expires_at", "call_id", "inner_appended_at", "outer_forwarded_at", "acknowledged_at", "acknowledged_by", "outer_call_id", "host_type", "delivery_reason" })
            if (item.Field(key).ValueKind == JsonValueKind.String) result[key == "call_id" ? "inner_call_id" : key] = item.Text(key);
        result["delivery_attempts"] = item.Number("delivery_attempts");
        result["sequence"] = item.Number("sequence");
        result["retry_requested"] = item.Flag("retry_requested");
        return JsonSerializer.SerializeToElement(result);
    }
}

public sealed partial class ExecutionCallRow
{
    private DateTimeOffset? _insertionNow;
    public bool IsInsertion => _value.Text("record_kind") == "insertion";
    public string InsertionText => IsInsertion ? _value.Text("text") : "";
    public string InsertionPreview => InsertionText.Replace('\r', ' ').Replace('\n', ' ');
    public DateTimeOffset TimelineAt => _value.Date("request_received_at") ?? _value.Date("created_at") ?? DateTimeOffset.MinValue;
    public bool CanRedeliverInsertion => IsInsertion && InsertionPresentation.Unconfirmed(Status) && !_value.Flag("retry_requested") && _value.Number("delivery_attempts") is > 0 and < 6 && _insertionNow is { } now && _value.Date("expires_at") > now;
    public bool CanCancelInsertion => IsInsertion && (Status is "pending" or "target_changed" || InsertionPresentation.Unconfirmed(Status));
    public string InsertionHint => State + "\n" + InsertionPresentation.Reason(_value) + (_value.Number("delivery_attempts") > 0 ? $"\n已尝试 {_value.Number("delivery_attempts")} 次；只重投本条补充，不重复原工具。" : "") + (_value.Flag("retry_requested") ? "\n已请求随下一次新调用重投。" : "");
    public string InsertionDetails => InsertionText + "\n\n" + InsertionHint + "\n消息：" + Id + "\n发送：" + TimelineAt.ToLocalTime().ToString("yyyy-MM-dd HH:mm:ss") +
        (_value.Text("inner_call_id").Length > 0 ? "\n内层调用：" + _value.Text("inner_call_id") : "") +
        (_value.Text("outer_call_id").Length > 0 ? "\n外层调用：" + _value.Text("outer_call_id") : "") +
        (_value.Date("acknowledged_at") is { } at ? "\n确认：" + at.ToLocalTime().ToString("yyyy-MM-dd HH:mm:ss") : "");

    public static ExecutionCallRow FromInsertion(JsonElement item, DateTimeOffset? now = null)
    {
        var row = new ExecutionCallRow(InsertionPresentation.Snapshot(item));
        row._insertionNow = now;
        return row;
    }

    public void ApplyInsertion(JsonElement item, DateTimeOffset? now)
    {
        if (!IsInsertion || Id != item.Text("insertion_id") || ConversationId != item.Text("conversation_id")) throw new InvalidOperationException("Insertion row identity changed.");
        if (_value.Date("updated_at") > item.Date("updated_at")) return;
        _value = InsertionPresentation.Snapshot(item); _insertionNow = now; Notify();
    }
}
