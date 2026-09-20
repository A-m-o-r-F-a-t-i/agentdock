using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Globalization;
using System.Runtime.CompilerServices;
using System.Text.Json;

namespace AgentDock.ControlPanel;

internal static class ExecutionJson
{
    internal static JsonElement Field(this JsonElement value, string name) => value.ValueKind == JsonValueKind.Object && value.TryGetProperty(name, out var item) ? item : default;
    internal static string Text(this JsonElement value, string name) { var item = value.Field(name); return item.ValueKind == JsonValueKind.String ? item.GetString() ?? "" : ""; }
    internal static long Number(this JsonElement value, string name) => value.Field(name).TryNumber();
    private static long TryNumber(this JsonElement value) => value.ValueKind == JsonValueKind.Number && value.TryGetInt64(out var number) ? number : 0;
    internal static bool Flag(this JsonElement value, string name) => value.Field(name).ValueKind == JsonValueKind.True;
    internal static IReadOnlyList<JsonElement> Array(this JsonElement value, string name) => value.Field(name).ValueKind == JsonValueKind.Array ? value.Field(name).EnumerateArray().Select(item => item.Clone()).ToArray() : [];
    internal static string Pretty(this JsonElement value) => value.ValueKind is JsonValueKind.Undefined or JsonValueKind.Null ? "" : JsonSerializer.Serialize(value, new JsonSerializerOptions { WriteIndented = true, Encoder = System.Text.Encodings.Web.JavaScriptEncoder.UnsafeRelaxedJsonEscaping });
    internal static string State(string state) => state switch
    {
        "created" => "准备执行", "pending_approval" => "等待审批 · 尚未执行", "running" => "正在执行",
        "succeeded" or "success" => "成功", "partial" => "部分完成", "failed" => "失败", "cancelled" => "已取消",
        "unknown" => "结果未知 · 需核对", "active" => "进行中", "blocked" => "已阻塞", "completed" => "已完成",
        "pending" => "待执行", "in_progress" => "执行中", "open" => "开放", "closed" => "关闭", _ => state
    };
    internal static string Mode(string mode) => mode switch { "readonly" => "只读检查", "full" => "完全权限（免审批）", _ => "按规则审批" };
}

public sealed class ExecutionObject
{
    public string Id { get; init; } = "";
    public string Kind { get; init; } = "conversation";
    public string Title { get; init; } = "";
    public string Detail { get; init; } = "";
    public string Tags { get; init; } = "";
    public string WorkspaceId { get; init; } = "";
    public string ManagementDates { get; init; } = "";
    public bool Pinned { get; init; }
    public bool Archived { get; init; }
    public bool Trashed { get; init; }
    public bool IsUnknown { get; init; }
    public JsonElement Snapshot { get; init; }
    public string Icon => IsUnknown ? "?" : Pinned ? "◆" : Kind == "task" ? "▣" : "◉";
    internal static ExecutionObject From(JsonElement value, string kind)
    {
        var stats = value.Field("statistics");
        var tags = string.Join(" · ", value.Array("tags").Select(item => item.GetString()));
        var detail = kind == "task" ? $"{ExecutionJson.State(value.Text("status"))} · {value.Number("completed_steps")}/{value.Number("step_count")} 步骤"
            : $"{stats.Number("total")} 次调用 · {stats.Number("running")} 运行 · {stats.Number("pending")} 待审批";
        var expiry = value.Text("purge_after");
        return new() { Id = value.Text(kind == "task" ? "id" : "conversation_id"), Kind = kind, Title = value.Text("title"), Detail = detail,
            Tags = tags, WorkspaceId = value.Text("workspace_id"), Pinned = value.Flag("pinned"), Archived = value.Text("archived_at") != "", Trashed = value.Text("trashed_at") != "",
            IsUnknown = value.Flag("is_unattributed"), ManagementDates = expiry == "" ? "" : "保留至 " + (DateTimeOffset.TryParse(expiry, out var date) ? date.LocalDateTime.ToString("yyyy-MM-dd HH:mm") : expiry), Snapshot = value.Clone() };
    }
}

public sealed class ExecutionCallRow : INotifyPropertyChanged
{
    private JsonElement _value;
    private string _output = "";
    private bool _expanded;
    private string _sourceTitle="";
    public event PropertyChangedEventHandler? PropertyChanged;
    public ObservableCollection<ExecutionCallRow> Children { get; } = [];
    public string Id => _value.Text("call_id");
    public long CreatedSeq => _value.Number("created_seq");
    public long UpdatedSeq => _value.Number("updated_seq");
    public string Status => _value.Text("status");
    public string State => ExecutionJson.State(Status);
    public string Tool => _value.Text("tool_name");
    public string ApprovalId => _value.Text("approval_id");
    public string ConversationId => _value.Text("conversation_id");
    public string TaskId => _value.Text("task_id");
    public string Command => _value.Text("display_command");
    public string Workdir => _value.Text("workdir");
    public string Parameters => _value.Text("parameter_summary");
    public bool ReadOnlyLegacy => _value.Flag("read_only_legacy") || _value.Flag("legacy");
    public string Summary => _value.Text("summary");
    public string Title => _value.Text("display_title") is { Length: > 0 } display ? display : _value.Text("title") is { Length: > 0 } title ? title : Tool;
    public string Duration => $"{Math.Max(0, _value.Number("elapsed_ms")) / 1000d:0.000} s";
    public string When => DateTimeOffset.TryParse(_value.Text("created_at"), out var time) ? time.LocalDateTime.ToString("HH:mm:ss") : "";
    public string Rule => _value.Text("rule_id");
    public string Origin => TaskId == "" ? (ConversationId == "" ? "来源未识别 · 未绑定任务" : _value.Text("binding_quality") == "connection_fallback" ? "连接级降级 · 未绑定任务" : "对话调用 · 未绑定任务") : "任务分支 "+_value.Text("thread_id")+" · "+(ConversationId=="" ? "对话来源未知" : _sourceTitle=="" ? "来源对话加载中" : "来自 "+_sourceTitle);
    internal string SourceTitle {get=>_sourceTitle;set{_sourceTitle=value;Changed(nameof(Origin));}}
    public bool CanRetry => !ReadOnlyLegacy && Status is "failed" or "cancelled";
    public bool HasCommand => Command.Length>0;
    public bool CanStop => !ReadOnlyLegacy && Status is "created" or "running" or "pending_approval";
    public bool NeedsApproval => !ReadOnlyLegacy && Status == "pending_approval";
    public bool NeedsVerification => Status == "unknown";
    public bool HasChanges => _value.Array("file_changes").Count > 0;
    public string Changes => string.Join(Environment.NewLine, _value.Array("file_changes").Select(item => item.Text("path") + (item.Flag("stats_known") ? $"   +{item.Number("insertions")} / −{item.Number("deletions")}" : "   变更统计未知"))) + (_value.Flag("changes_truncated") ? "\n变更条目过多，摘要已截断。" : "");
    public string Technical => _value.Pretty();
    public bool IsExpanded { get => _expanded; set { _expanded = value; Changed(); } }
    public bool FollowOutput { get; set; } = true;
    public bool DetailLoaded { get; set; }
    public string Output { get => _output; private set { _output = value; Changed(); } }
    public string HistoryWarning => ReadOnlyLegacy ? "旧版只读记录 · 对话来源未知；可确认的会话事件保留聚合，禁止直接重放。" : _value.Flag("history_incomplete") ? "该调用的保留历史存在缺口，不能视作完整执行记录。" : "";
    public ExecutionCallRow(JsonElement value) { _value = value.Clone(); _expanded = Status is "pending_approval" or "failed" or "unknown"; }
    internal void Apply(JsonElement value)
    {
        if (value.Number("updated_seq") < UpdatedSeq) return;
        _value = value.Clone();
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(null));
    }
    internal void ApplyDetail(JsonElement value)
    {
        Apply(value); DetailLoaded = true;
        var stdout = value.Text("output_preview"); var stderr = value.Text("stderr_preview");
        Output = (value.Flag("stdout_truncated") ? "[stdout 仅保留末尾摘要；较早输出已截断]\n" : "") + stdout
            + (stderr.Length == 0 && !value.Flag("stderr_truncated") ? "" : "\n[stderr]" + (value.Flag("stderr_truncated") ? "（仅保留末尾摘要）" : "") + "\n" + stderr);
        if (Output.Length == 0) Output = "没有持久化输出。读取文件仅记录路径和摘要，不复制文件正文。";
        if (value.Field("exit_code").ValueKind == JsonValueKind.Number) Output += "\n[退出码] " + value.Number("exit_code");
    }
    private void Changed([CallerMemberName] string? name = null) => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));
}

public sealed record ExecutionChoice(string Id, string Title)
{
    public override string ToString() => Title;
}

internal sealed class ExecutionPreferences
{
    public int SchemaVersion { get; set; } = 1;
    public int RetentionDays { get; set; } = 30;
    public double FontSize { get; set; } = 14;
    public string LastView { get; set; } = "conversation";
    public string LastKind { get; set; } = "conversation";
    public bool Notifications { get; set; } = true;
    public Dictionary<string, string[]> SavedFilters { get; set; } = [];
}
