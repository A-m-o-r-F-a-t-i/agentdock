using System.Text.Json;
using System.Windows;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private readonly DispatcherTimer _sidebarRefresh = new(DispatcherPriority.DataBind) { Interval = TimeSpan.FromMilliseconds(100) };
    private readonly Dictionary<string, JsonElement> _sidebarUnacknowledged = new(StringComparer.Ordinal);
    private readonly Dictionary<string, string> _provisionalTitles = new(StringComparer.Ordinal);
    private Task? _sidebarStreamTask;

    private void InitializeSidebar()
    {
        _sidebarRefresh.Tick += async (_, _) =>
        {
            _sidebarRefresh.Stop();
            if (_closed || !_sidebarDirty || _sidebarLoading) return;
            var remaining = SidebarRetryDelay(SidebarScope());
            if (remaining > TimeSpan.Zero) { QueueSidebarRefresh(); return; }
            await GuardAsync(LoadSidebarAsync);
        };
    }
    private void QueueSidebarRefresh()
    {
        if (_closed) return;
        _sidebarDirty = true;
        if (_sidebarLoading || _sidebarRefresh.IsEnabled) return;
        if (_sidebarFailures.TryGetValue(SidebarScope(), out var failure) && failure.Attempts > 5) return;
        var remaining = SidebarRetryDelay(SidebarScope());
        _sidebarRefresh.Interval = remaining > TimeSpan.FromMilliseconds(100) ? remaining : TimeSpan.FromMilliseconds(100);
        _sidebarRefresh.Start();
    }
    private void StartSidebarStream(ulong cursor)
    {
        if (_closed || _sidebarStreamTask is not null) return;
        _sidebarStreamTask = ObserveSidebarAsync(cursor);
    }
    private async Task ObserveSidebarAsync(ulong cursor)
    {
        await GuardAsync(() => _client.ObserveExecutionsAsync("top_level=true&view=all", cursor, async message =>
        {
            await Dispatcher.InvokeAsync(() =>
            {
                if (_closed) return;
                if (message.Kind == "call") ObserveSidebarRoot(message.Value);
                else if (message.Kind is "connected" or "reset" or "gap") { ResetSidebarRecoveryBudget(); QueueSidebarRefresh(); }
            }, DispatcherPriority.DataBind);
        }, _lifetime.Token));
    }
    private void StopSidebar()
    {
        _sidebarRefresh.Stop(); _sidebarRequest?.Cancel();
    }

    private void PreserveProvisionalTitle(ExecutionObject item, string suppliedTitle)
    {
        if (suppliedTitle.Length > 0 && suppliedTitle != "新对话") { _provisionalTitles.Remove(item.Id); return; }
        if (_provisionalTitles.TryGetValue(item.Id, out var title)) item.Title = title;
    }
    private static bool IsSidebarRoot(JsonElement call) => call.Text("conversation_id").Length > 0 && call.Text("parent_call_id").Length == 0 && call.Text("visibility") != "diagnostic" && call.Date("request_received_at") is not null;

    private void ObserveSidebarRoot(JsonElement call)
    {
        if (!IsSidebarRoot(call)) return;
        ResetSidebarRecoveryBudget();
        var id = call.Text("conversation_id");
        if (_sidebarUnacknowledged.TryGetValue(id, out var previous) && previous.Number("updated_seq") >= call.Number("updated_seq")) return;
        _sidebarUnacknowledged[id] = call.Clone();
        if (_sidebarUnacknowledged.Count > 4096) _sidebarUnacknowledged.Remove(_sidebarUnacknowledged.MinBy(pair => pair.Value.Number("updated_seq")).Key);
        QueueSidebarRefresh();
        if (_conversationView != "active" || SearchBox.Text.Trim().Length > 0) return;
        var existing = Objects.FirstOrDefault(item => !item.IsGroupFooter && item.Id == id);
        _updating = true; _initializingGroup = true;
        try
        {
            if (existing is not null) ApplySidebarActivity(existing, call);
            else
            {
                var workspace = call.Text("workspace_id"); if (workspace.Length == 0) workspace = "unassigned";
                if (CurrentNavigation().For(workspace).Mode == ProjectNavigationMode.History) return;
                var item = SidebarLiveObject(call, CurrentNavigation());
                var insertion = Objects.ToList().FindIndex(row => row.WorkspaceKey.Id == item.WorkspaceKey.Id);
                if (insertion < 0)
                {
                    Objects.Add(item);
                    Objects.Add(new ExecutionObject { Id = "footer:" + item.WorkspaceKey.Id, IsGroupFooter = true, WorkspaceKey = item.WorkspaceKey, WorkspaceId = item.WorkspaceKey.Id });
                }
                else Objects.Insert(insertion, item);
                SidebarEmpty.Visibility = Visibility.Collapsed;
            }
        }
        finally { _updating = false; _initializingGroup = false; }
        _activityClock.Refresh(); RefreshAutoProjectExpansion();
    }

    private void ApplySidebarActivity(ExecutionObject item, JsonElement call)
    {
        var requested = call.Date("request_received_at");
        var changed = call.Date("last_activity_at") ?? requested;
        if (requested is not null && (item.LastToolCallAt is null || requested > item.LastToolCallAt)) item.LastToolCallAt = requested;
        if (changed is not null && (item.LastActivityAt is null || changed > item.LastActivityAt)) item.LastActivityAt = item.SortActivityAt = changed;
        item.RefreshActivity();
    }

    private ExecutionObject SidebarLiveObject(JsonElement call, SidebarNavigationState navigation)
    {
        var id = call.Text("conversation_id");
        var workspace = call.Text("workspace_id"); if (workspace.Length == 0) workspace = "unassigned";
        var title = _conversationTitles.GetValueOrDefault(id, "");
        if (title.Length == 0)
        {
            var tool = call.Text("tool_name");
            var label = call.Text("display_title", call.Text("title", tool));
            title = (label == tool ? tool : tool + " · " + label) + " · " + call.Date("request_received_at")?.ToLocalTime().ToString("HH:mm:ss.fff");
            _provisionalTitles[id] = title;
            if (_provisionalTitles.Count > 4096) _provisionalTitles.Remove(_provisionalTitles.Keys.First());
        }
        var state = navigation.For(workspace);
        if (!_sidebarGroups.TryGetValue(workspace, out var key))
        {
            var name = _workspaceNames.GetValueOrDefault(workspace, workspace == "unassigned" ? "未关联项目" : "项目");
            _sidebarGroups[workspace] = key = new(workspace, name);
            key.Apply(JsonSerializer.SerializeToElement(new { title = name, workspace_id = workspace, total = 1, recent_count = 1, mode = state.ProtocolMode, last_activity_at = call.Date("last_activity_at") ?? call.Date("request_received_at") }));
        }
        key.IsExpanded = state.Expanded(1);
        var snapshot = JsonSerializer.SerializeToElement(new
        {
            conversation_id = id, title, source = call.Text("source"), created_at = call.Date("request_received_at"),
            state = new { workspace_id = workspace }, task_ids = Array.Empty<string>(),
            statistics = new { last_tool_call_at = call.Date("request_received_at"), last_activity_at = call.Date("last_activity_at") ?? call.Date("request_received_at") }
        });
        var item = ExecutionObject.From(snapshot, "conversation"); item.WorkspaceKey = key;
        _conversationTitles[id] = title;
        return item;
    }

    private void MergeUnacknowledgedSidebarCalls(long acknowledged, SidebarNavigationState navigation, List<WorkspaceGroupKey> groups, Dictionary<string, List<ExecutionObject>> rows, IReadOnlySet<string> protectedGroups)
    {
        foreach (var id in _sidebarUnacknowledged.Where(pair => pair.Value.Number("updated_seq") <= acknowledged).Select(pair => pair.Key).ToArray()) _sidebarUnacknowledged.Remove(id);
        if (_conversationView != "active" || SearchBox.Text.Trim().Length > 0) return;
        foreach (var call in _sidebarUnacknowledged.Values.OrderBy(value => value.Number("updated_seq")))
        {
            var protectedWorkspace = call.Text("workspace_id"); if (protectedWorkspace.Length == 0) protectedWorkspace = "unassigned";
            if (protectedGroups.Contains(protectedWorkspace)) continue;
            var existing = rows.Values.SelectMany(value => value).FirstOrDefault(item => !item.IsGroupFooter && item.Id == call.Text("conversation_id"));
            if (existing is not null) { ApplySidebarActivity(existing, call); continue; }
            var workspace = call.Text("workspace_id"); if (workspace.Length == 0) workspace = "unassigned";
            if (navigation.For(workspace).Mode == ProjectNavigationMode.History) continue;
            var item = SidebarLiveObject(call, navigation); var key = item.WorkspaceKey;
            if (!rows.TryGetValue(key.Id, out var group))
            {
                groups.Add(key); group = [new ExecutionObject { Id = "footer:" + key.Id, IsGroupFooter = true, WorkspaceKey = key, WorkspaceId = key.Id }]; rows[key.Id] = group;
            }
            group.Insert(0, item);
        }
    }
}
