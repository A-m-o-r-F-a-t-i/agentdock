using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public static class ExecutionJson
{
    public static JsonElement Field(this JsonElement value, string name) => value.ValueKind == JsonValueKind.Object && value.TryGetProperty(name, out var field) ? field : default;
    public static string Text(this JsonElement value, string name, string fallback = "") => value.Field(name).ValueKind == JsonValueKind.String ? value.Field(name).GetString() ?? fallback : fallback;
    public static long Number(this JsonElement value, string name) => value.Field(name).ValueKind == JsonValueKind.Number && value.Field(name).TryGetInt64(out var number) ? number : 0;
    public static long? OptionalNumber(this JsonElement value, string name) => value.Field(name).ValueKind == JsonValueKind.Number && value.Field(name).TryGetInt64(out var number) ? number : null;
    public static DateTimeOffset? Date(this JsonElement value, string name) => DateTimeOffset.TryParse(value.Text(name), out var date) && date.Year > 1 ? date : null;
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

public sealed class WorkspaceGroupKey(string id, string title) : INotifyPropertyChanged
{
	private bool _expanded;
	public bool IsExpanded { get => _expanded; set { if (_expanded == value) return; _expanded = value; PropertyChanged?.Invoke(this, new(nameof(IsExpanded))); } }
	public string Mode { get; private set; } = "auto";
	public int RecentCount { get; private set; }
    public string Id { get; } = id;
    public string Title { get; private set; } = title;
    public string Root { get; private set; } = "";
    public int Total { get; private set; }
    public DateTimeOffset? LastActivityAt { get; private set; }
    public event PropertyChangedEventHandler? PropertyChanged;
    public void Apply(JsonElement value)
    {
        Title = value.Text("title", Title); Root = value.Text("root");
        Total = (int)value.Number("total"); LastActivityAt = value.Date("last_activity_at");
		Mode = value.Text("mode", "auto"); RecentCount = (int)value.Number("recent_count");
        PropertyChanged?.Invoke(this, new(null));
    }
    public override bool Equals(object? value) => value is WorkspaceGroupKey key && key.Id == Id;
    public override int GetHashCode() => StringComparer.Ordinal.GetHashCode(Id);
}
public sealed record ExecutionChoice(string Id, string Title) { public override string ToString() => Title; }

public sealed class ExecutionObject : INotifyPropertyChanged
{
    private bool _recentlyActive;
    public event PropertyChangedEventHandler? PropertyChanged;
    public DateTimeOffset? LastToolCallAt { get; set; }
    public DateTimeOffset? LastActivityAt { get; set; }
    public DateTimeOffset? SortActivityAt { get; set; }
    public bool IsGroupFooter { get; set; }
    public bool HasMore { get; set; }
    public bool AutoLoadMore { get; set; }
    public bool InsertionEligible { get; set; }
	public bool InFlight { get; set; }
    public void Apply(ExecutionObject item)
    {
        if (Id != item.Id || Kind != item.Kind) throw new InvalidOperationException("Row identity changed.");
        Title = item.Title; Detail = item.Detail; Tags = item.Tags; WorkspaceId = item.WorkspaceId;
        ManagementDates = item.ManagementDates; Pinned = item.Pinned; Archived = item.Archived;
        Trashed = item.Trashed; Terminated = item.Terminated; IsUnknown = item.IsUnknown; IsOrphan = item.IsOrphan;
        PendingCount = item.PendingCount; RunningCount = item.RunningCount; Snapshot = item.Snapshot;
		InFlight = item.InFlight;
        LastToolCallAt = item.LastToolCallAt; LastActivityAt = item.LastActivityAt; SortActivityAt = item.SortActivityAt;
        IsGroupFooter = item.IsGroupFooter; HasMore = item.HasMore; AutoLoadMore = item.AutoLoadMore;
        PropertyChanged?.Invoke(this, new(null));
    }
    public bool RecentlyActive
    {
        get => _recentlyActive;
        set { if (_recentlyActive == value) return; _recentlyActive = value; PropertyChanged?.Invoke(this, new(nameof(RecentlyActive))); }
    }
	public void RefreshActivity() => PropertyChanged?.Invoke(this, new(null));
    public string Id { get; set; } = "";
    public string Kind { get; set; } = "conversation";
    public string Title { get; set; } = "";
    public string Detail { get; set; } = "";
    public string Tags { get; set; } = "";
    public string WorkspaceId { get; set; } = "";
    public WorkspaceGroupKey WorkspaceKey { get; set; } = new("", "未归属工作区");
    public string ManagementDates { get; set; } = "";
    public bool Pinned { get; set; }
    public bool Archived { get; set; }
    public bool Trashed { get; set; }
    public bool Terminated { get; set; }
    public bool IsUnknown { get; set; }
    public bool IsOrphan { get; set; }
    public long PendingCount { get; set; }
    public long RunningCount { get; set; }
    public JsonElement Snapshot { get; set; }
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
			InFlight = value.Flag("in_flight"),
            PendingCount = stats.Number("pending"), RunningCount = stats.Number("running"), Snapshot = value.Clone(), LastToolCallAt = stats.Date("last_tool_call_at"), LastActivityAt = stats.Date("last_activity_at") ?? stats.Date("last_tool_call_at"), SortActivityAt = value.Date("last_activity_at") ?? stats.Date("last_activity_at") ?? value.Date("created_at")
        };
    }
}

public sealed class ExecutionCallRow : INotifyPropertyChanged
{
	public ExecutionPayloadView RequestPayload { get; } = new("调用");
	public ExecutionPayloadView ResponsePayload { get; } = new("输出");
	public string RequestText => RequestPayload.Text;
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
	public string Title
	{
		get
		{
			var label = _value.Text("activity_label", _value.Text("display_title", _value.Text("title", Tool))).Replace('\r',' ').Replace('\n',' ');
			if (Tool == "file_edit" && label.StartsWith("EDIT_FILE", StringComparison.Ordinal)) label = label[9..].TrimStart(' ', '·', ':');
			return Tool.Length == 0 || label == Tool ? label : label.Length == 0 ? Tool : Tool + " · " + label;
		}
	}
    public DateTimeOffset? RequestReceivedAt => _value.Date("request_received_at");
    public DateTimeOffset? LastActivityAt => _value.Date("last_activity_at");
    public long? RpcElapsedMs => _value.OptionalNumber("rpc_elapsed_ms");
    public long? TotalElapsedMs => RpcElapsedMs is >= 0 ? RpcElapsedMs : _value.OptionalNumber("elapsed_ms") is >= 0 ? _value.OptionalNumber("elapsed_ms") : _value.OptionalNumber("operation_elapsed_ms") is >= 0 ? _value.OptionalNumber("operation_elapsed_ms") : null;
    public string DurationSource => RpcElapsedMs is >= 0 ? "rpc" : _value.OptionalNumber("elapsed_ms") is >= 0 ? "legacy" : _value.OptionalNumber("operation_elapsed_ms") is >= 0 ? "operation" : "unknown";
    public string Duration => FormatDuration(TotalElapsedMs);
    public string TotalTimingDetails => DurationSource switch
    {
        "rpc" => "RPC 耗时：" + Duration,
        "legacy" => "历史总耗时：" + Duration + "；原记录未保存 RPC/执行/等待分项。",
        "operation" => "操作完成耗时：" + Duration + "（来源：operation_elapsed_ms）",
        _ => "总耗时：未记录"
    };
    public string ExecutionDuration => FormatDuration(_value.OptionalNumber("execution_elapsed_ms"));
    public string WaitDuration => FormatDuration(_value.OptionalNumber("wait_elapsed_ms"));
    public string ActualTool => Tool;
	public bool HasEditStatistics => _value.Text("parent_call_id").Length == 0 && _value.Field("file_edit").ValueKind == JsonValueKind.Object;
	public bool EditPreview => HasEditStatistics && _value.Field("file_edit").Flag("dry_run");
	public bool EditCountsKnown => HasEditStatistics && !EditPreview && (_value.Field("file_edit").Text("stats_state") is "" or "known" or "partial") && _value.Field("file_edit").OptionalNumber("insertions") is >= 0 && _value.Field("file_edit").OptionalNumber("deletions") is >= 0;
	public bool PartialEditStatistics => _value.Field("file_edit").Text("stats_state") == "partial";
	public string AddedLinesText => !HasEditStatistics ? "" : EditCountsKnown ? "+" + _value.Field("file_edit").Number("insertions") + (PartialEditStatistics ? "*" : "") : EditPreview ? "预演" : "—";
	public string DeletedLinesText => EditCountsKnown ? "−" + _value.Field("file_edit").Number("deletions") : "";
	public string EditCountsHint => PartialEditStatistics ? "部分执行：仅计入已确认仍落盘的变化；未核实的路径保留未知。" : EditPreview ? "预演未落盘；拟议行数在文件详情中。" : EditCountsKnown ? "本次根调用实际落盘的新增/删除逻辑行数。" : "本次修改行数未统计或结果未知，未计为零。";
    public string Started => _value.Date("started_at")?.ToLocalTime().ToString("HH:mm:ss.fff") ?? When;
    public string SourceType => _value.Text("source", "未记录");
    public string TimingDetails => string.Join("\n", new[]
    {
        "工具：" + Tool,
        "RPC 返回：" + (_value.Date("rpc_completed_at")?.ToLocalTime().ToString("yyyy-MM-dd HH:mm:ss.fff") ?? "未记录"),
        TotalTimingDetails,
        "执行阶段：" + ExecutionDuration,
        "执行前等待：" + WaitDuration + "（含已观测到的准备及审批等待）",
        "操作完成耗时：" + FormatDuration(_value.OptionalNumber("operation_elapsed_ms")),
        "后台命令进程：" + FormatDuration(_value.OptionalNumber("process_elapsed_ms")),
        "RPC 与后台命令分别计时。并发调用的累计耗时不等于实际经过时间。"
    });
    public string FileEditDetails
    {
        get
        {
            var edit = _value.Field("file_edit");
            if (edit.ValueKind != JsonValueKind.Object) return "文件操作详情未记录。";
            var changed = edit.Field("changed").ValueKind switch { JsonValueKind.True => "是", JsonValueKind.False => "否", _ => "结果未知" };
            var preview = edit.Flag("dry_run");
            var addedKey = preview ? "proposed_insertions" : "insertions";
            var removedKey = preview ? "proposed_deletions" : "deletions";
            var files = edit.Array("affected_files").Select(file => file.Text("path") + (file.Text("move_to").Length > 0 ? " → " + file.Text("move_to") : "") + "  +" + (file.OptionalNumber(addedKey)?.ToString() ?? "—") + " −" + (file.OptionalNumber(removedKey)?.ToString() ?? "—"));
            return $"{Tool} · {edit.Text("action")}\n目标：{edit.Text("path")}\n预览：{(edit.Flag("dry_run") ? "是，未写入" : "否")}\n已派发：{(edit.Flag("executed") ? "是" : "否")}\n实际修改：{changed}\n影响文件数：{edit.OptionalNumber("affected_count")?.ToString() ?? "未记录"}\n统计：{(PartialEditStatistics ? "部分执行，仅含已确认变化" : preview ? "预演计划" : edit.Text("stats_state", "旧记录"))}\n{(preview ? "拟议" : "实际")}新增/删除行：{edit.OptionalNumber(addedKey)?.ToString() ?? "未记录"} / {edit.OptionalNumber(removedKey)?.ToString() ?? "未记录"}\n" + string.Join("\n", files) + (edit.Flag("files_truncated") ? "\n文件明细超过预览上限。" : "") + "\n\n" + edit.Text("diff_preview") + (edit.Flag("diff_truncated") ? "\n差异预览已截断。" : "");
        }
    }
    private static string FormatDuration(long? milliseconds) => milliseconds is >= 0 ? (milliseconds.Value / 1000.0).ToString("0.000") + " s" : "未记录";
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
	public bool HasChanges => HasEditStatistics || _value.Array("file_changes").Length > 0;
    public string Changes => string.Join("\n", _value.Array("file_changes").Select(change => change.Text("path") + (change.Flag("stats_known") ? $"  +{change.Number("insertions")} −{change.Number("deletions")}" : "")));
    public string Technical => _value.Pretty();
    public bool IsExpanded { get => _expanded; set { _expanded = value; Notify(); } }
    public bool FollowOutput { get; set; } = true;
    public bool DetailLoaded { get; private set; }
	public string Output => ResponsePayload.Reference.Length == 0 && _output.Length > 0 ? _output : ResponsePayload.Text;
	public string ProgressOutput => _output;
	public bool HasSupplementalProgress => ResponsePayload.Reference.Length > 0 && _output.Length > 0;
    public string HistoryWarning => _value.Flag("history_incomplete") ? "该记录的部分历史已不可用。" : "";
	public ExecutionCallRow(JsonElement value)
	{
		_value = value.Clone();
		RequestPayload.PropertyChanged += (_, _) => Notify(); ResponsePayload.PropertyChanged += (_, _) => Notify();
		RequestPayload.Describe(value.Field("request"), value.Text("parent_call_id")); ResponsePayload.Describe(value.Field("response"), value.Text("parent_call_id"));
	}
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
	public void Apply(JsonElement value)
	{
		if (value.Number("updated_seq") < UpdatedSeq) return;
		if (value.Number("updated_seq") > UpdatedSeq) DetailLoaded = false;
		_value = value.Clone();
		RequestPayload.Describe(value.Field("request"), value.Text("parent_call_id")); ResponsePayload.Describe(value.Field("response"), value.Text("parent_call_id"));
		Notify();
	}
    public void ApplyDetail(JsonElement value)
    {
        if (value.Number("updated_seq") < UpdatedSeq) return;
        Apply(value); DetailLoaded = true;
        var output = value.Text("output_preview"); var error = value.Text("stderr_preview");
        if (error.Length > 0) output += (output.Length > 0 ? "\n\n" : "") + "标准错误\n" + error;
        if (value.Flag("stdout_truncated") || value.Flag("stderr_truncated")) output = "输出已截断，仅显示保留部分。\n\n" + output;
        _output = output; Notify();
    }
    private void Notify() => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(null));
}

public sealed class ExecutionPreferences
{
    public int SchemaVersion { get; set; } = 3;
    public int RetentionDays { get; set; } = 30;
    public double FontSize { get; set; } = 14;
    public bool Notifications { get; set; } = true;
    public string LastView { get; set; } = "conversation";
    public string LastKind { get; set; } = "conversation";
    public string Theme { get; set; } = "system";
    public bool DetailedCalls { get; set; }
    public string LastConversation { get; set; } = "";
    public HashSet<string> CollapsedWorkspaces { get; set; } = [];
    public Dictionary<string, string[]> SavedFilters { get; set; } = [];
    public HashSet<string> DismissedNotices { get; set; } = [];
    [System.Text.Json.Serialization.JsonExtensionData]
    public Dictionary<string, JsonElement> AdditionalPreferences { get; set; } = [];
}
