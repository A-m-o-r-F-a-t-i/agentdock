using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public static class ExecutionJson
{
    public static JsonElement Field(this JsonElement value, string name) => value.ValueKind == JsonValueKind.Object && value.TryGetProperty(name, out var field) ? field : default;
    public static string Text(this JsonElement value, string name, string fallback = "") => value.Field(name).ValueKind == JsonValueKind.String ? value.Field(name).GetString() ?? fallback : fallback;
    public static long Number(this JsonElement value, string name) => value.Field(name).ValueKind == JsonValueKind.Number && value.Field(name).TryGetInt64(out var number) ? number : 0;
    public static bool Flag(this JsonElement value, string name) => value.Field(name).ValueKind == JsonValueKind.True;
    public static JsonElement[] Array(this JsonElement value, string name) => value.Field(name).ValueKind == JsonValueKind.Array ? value.Field(name).EnumerateArray().Select(item => item.Clone()).ToArray() : [];
    public static string Pretty(this JsonElement value) => value.ValueKind == JsonValueKind.Undefined ? "" : JsonSerializer.Serialize(value, new JsonSerializerOptions { WriteIndented = true });
    public static string State(string state) => state switch
    {
        "created" => "等待执行", "running" or "in_progress" => "运行中", "pending_approval" => "待审批", "succeeded" or "completed" => "已完成",
        "failed" => "失败", "partial" => "部分完成", "cancelled" => "已取消", "unknown" => "结果待核对", "blocked" => "受阻", "pending" => "未开始", _ => state
    };
    public static string Mode(string mode) => mode switch { "full" => "完全权限", "readonly" or "read_only" => "只读", "rules" or "ask" or "guarded" or "default" => "需要审批", _ => mode };
    public static bool HasDate(this JsonElement value, string name) => value.Field(name).ValueKind == JsonValueKind.String;
}

public sealed record WorkspaceGroupKey(string Id, string Title);
public sealed record ExecutionChoice(string Id, string Title) { public override string ToString() => Title; }

public sealed class ExecutionObject
{
    public string Id { get; init; } = "";
    public string Kind { get; init; } = "conversation";
    public string Title { get; init; } = "";
    public string Detail { get; init; } = "";
    public string Tags { get; init; } = "";
    public string WorkspaceId { get; init; } = "";
    public WorkspaceGroupKey WorkspaceKey { get; set; } = new("", "未归属工作区");
    public string ManagementDates { get; init; } = "";
    public bool Pinned { get; init; }
    public bool Archived { get; init; }
    public bool Trashed { get; init; }
    public bool Terminated { get; init; }
    public bool IsUnknown { get; init; }
    public bool IsOrphan { get; init; }
    public long PendingCount { get; init; }
    public long RunningCount { get; init; }
    public JsonElement Snapshot { get; init; }
    public string SelectionKey => IsUnknown ? "unattributed" : Id;
    public static ExecutionObject From(JsonElement value, string kind)
    {
        var title = value.Text("title");
        var created = DateTimeOffset.TryParse(value.Text("created_at"), out var date) ? date.ToLocalTime().ToString("MM-dd HH:mm") : "历史记录";
        if (string.IsNullOrWhiteSpace(title) || title == "新对话") title = "对话 · " + created;
        var workspace = value.Field("state").Text("workspace_id", value.Text("workspace_id"));
        var workspaces = value.Array("workspace_ids");
        if (workspace.Length == 0 && workspaces.Length > 0 && workspaces[0].ValueKind == JsonValueKind.String) workspace = workspaces[0].GetString() ?? "";
        var stats = value.Field("statistics");
        return new ExecutionObject
        {
            Id = value.Text(kind == "task" ? "task_id" : "conversation_id", value.Text("id")), Kind = kind, Title = title,
            WorkspaceId = workspace, Tags = string.Join("、", value.Array("tags").Select(tag => tag.GetString())),
            Detail = kind == "task" ? ExecutionJson.State(value.Text("status")) : value.Text("source"),
            Pinned = value.Flag("pinned"), Archived = value.HasDate("archived_at"), Trashed = value.HasDate("trashed_at"), Terminated = value.HasDate("terminated_at"),
            ManagementDates = created, IsUnknown = value.Flag("is_unattributed"), IsOrphan = value.Flag("is_orphan"),
            PendingCount = stats.Number("pending"), RunningCount = stats.Number("running"), Snapshot = value.Clone()
        };
    }
}

public sealed class ExecutionCallRow : INotifyPropertyChanged
{
    private JsonElement _value;
    private string _output = "";
    private bool _expanded;
    private string _sourceTitle = "";
    private string _sourceState = "unavailable";
    public event PropertyChangedEventHandler? PropertyChanged;
    public ObservableCollection<ExecutionCallRow> Children { get; } = [];
    public string Id => _value.Text("call_id");
    public long CreatedSeq => _value.Number("created_seq");
    public long UpdatedSeq => _value.Number("updated_seq");
    public string Status => _value.Text("status");
    public string State => ExecutionJson.State(Status);
    public string StatusGlyph => Status switch { "succeeded" => "✓", "failed" => "×", "partial" or "unknown" => "!", "pending_approval" => "审", "cancelled" => "–", _ => "…" };
    public string Tool => _value.Text("tool_name");
    public string ApprovalId => _value.Text("approval_id");
    public string ConversationId => _value.Text("conversation_id");
    public string TaskId => _value.Text("task_id");
    public string Command => _value.Text("display_command");
    public string Workdir => _value.Text("workdir");
    public string Parameters => _value.Text("parameter_summary");
    public bool ReadOnlyLegacy => _value.Flag("read_only_legacy");
    public string Summary => _value.Text("summary");
    public string Title => _value.Text("activity_label", _value.Text("display_title", _value.Text("title", Tool))).Replace('\r', ' ').Replace('\n', ' ');
    public string Duration => (_value.Number("elapsed_ms") / 1000.0).ToString("0.000") + " s";
    public string When => DateTimeOffset.TryParse(_value.Text("created_at"), out var date) ? date.ToLocalTime().ToString("HH:mm:ss") : "";
    public string Rule => string.Join(" · ", new[] { _value.Text("rule_id"), ExecutionJson.Mode(_value.Text("permission_mode")) }.Where(value => value.Length > 0));
    public string SourceState => _sourceState;
    public string Origin => ConversationId.Length == 0 ? "未归属" : _sourceState switch
    {
        "resolved" => _sourceTitle, "loading" => "正在读取来源", "deleted" => "来源已删除", "error" => "来源读取失败", _ => "来源暂不可用"
    };
    public string SourceTitle { get => _sourceTitle; set => SetSource(value, "resolved"); }
    public void SetSource(string title, string state) { _sourceTitle = title; _sourceState = state; Notify(); }
    public bool CanRetry => !ReadOnlyLegacy && Status is "failed" or "cancelled";
    public bool CanStop => !ReadOnlyLegacy && Status is "created" or "running" or "pending_approval";
    public bool NeedsApproval => Status == "pending_approval";
    public bool NeedsVerification => Status == "unknown";
    public bool HasChanges => _value.Array("file_changes").Length > 0;
    public string Changes => string.Join("\n", _value.Array("file_changes").Select(change => change.Text("path") + (change.Flag("stats_known") ? $"  +{change.Number("insertions")} −{change.Number("deletions")}" : "")));
    public string Technical => _value.Pretty();
    public bool IsExpanded { get => _expanded; set { _expanded = value; Notify(); } }
    public bool FollowOutput { get; set; } = true;
    public bool DetailLoaded { get; private set; }
    public string Output => _output;
    public string HistoryWarning => _value.Flag("history_incomplete") ? "该记录的部分历史已不可用。" : "";
    public ExecutionCallRow(JsonElement value) { _value = value.Clone(); }
    public bool VisibleIn(string view)
    {
        if (_value.HasDate("deleted_at")) return false;
        return view switch
        {
            "all" => true,
            "trash" => _value.HasDate("trashed_at"),
            "archived" => !_value.HasDate("trashed_at") && _value.HasDate("archived_at"),
            "isolated" => !_value.HasDate("trashed_at") && _value.HasDate("isolated_at"),
            _ => !_value.HasDate("trashed_at") && !_value.HasDate("archived_at") && !_value.HasDate("isolated_at")
        };
    }
    public void Apply(JsonElement value) { if (value.Number("updated_seq") < UpdatedSeq) return; _value = value.Clone(); Notify(); }
    public void ApplyDetail(JsonElement value)
    {
        if (value.Number("updated_seq") < UpdatedSeq) return;
        Apply(value); DetailLoaded = true;
        var output = value.Text("output_preview"); var error = value.Text("stderr_preview");
        if (error.Length > 0) output += (output.Length > 0 ? "\n\n" : "") + "标准错误\n" + error;
        if (output.Length == 0) output = value.Text("summary", "没有输出。");
        if (value.Flag("stdout_truncated") || value.Flag("stderr_truncated")) output = "输出已截断，仅显示保留部分。\n\n" + output;
        _output = output; Notify();
    }
    private void Notify() => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(null));
}

public sealed class ExecutionPreferences
{
    public int SchemaVersion { get; set; } = 2;
    public int RetentionDays { get; set; } = 30;
    public double FontSize { get; set; } = 14;
    public bool Notifications { get; set; } = true;
    public string LastView { get; set; } = "conversation";
    public string LastKind { get; set; } = "conversation";
    public string Theme { get; set; } = "system";
    public string LastConversation { get; set; } = "";
    public HashSet<string> CollapsedWorkspaces { get; set; } = [];
    public Dictionary<string, string[]> SavedFilters { get; set; } = [];
}
