using System.Diagnostics;
using System.IO;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Data;
using System.Windows.Input;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private readonly Dictionary<string, WorkspaceGroupKey> _sidebarGroups = new(StringComparer.Ordinal);
    private readonly Dictionary<string, SidebarNavigationState> _sidebarViews = new(StringComparer.Ordinal);
    private readonly HashSet<string> _sidebarPaging = new(StringComparer.Ordinal);
    private readonly SemaphoreSlim _sidebarPageGate = new(1, 1);
    private SidebarNavigationState? _searchNavigation;
    private string _searchNavigationScope = "", _sidebarScope = "";
    private bool _allProjectsCollapsed, _initializingGroup, _sidebarLoading, _sidebarDirty;
    private CancellationTokenSource? _sidebarRequest;
    private int _openMenus;
    private long _lastSidebarRefresh;

    private SidebarNavigationState CurrentNavigation()
    {
        var search = SearchBox.Text.Trim();
        if (search.Length > 0)
        {
            var scope = _conversationView + "\n" + search;
            if (_searchNavigation is null || scope != _searchNavigationScope)
            { _searchNavigation = new(); _searchNavigationScope = scope; }
            return _searchNavigation;
        }
        _searchNavigation = null; _searchNavigationScope = "";
        if (!_sidebarViews.TryGetValue(_conversationView, out var state)) _sidebarViews[_conversationView] = state = new(_allProjectsCollapsed);
        return state;
    }

    private IEnumerable<ExecutionObject> ActivityItems()
    {
        foreach (var item in Objects.Where(item => !item.IsGroupFooter)) yield return item;
        if (_selected is { IsGroupFooter: false } selected && !Objects.Contains(selected)) yield return selected;
    }

    private Task LoadSidebarAsync() => LoadSidebarAsync(null);
    private async Task LoadSidebarAsync(SidebarNavigationState? candidate)
    {
        var target = CurrentNavigation();
        var revision = target.Revision;
        candidate ??= target.Copy();
        var selectionGeneration = _generation;
        var epoch = ++_objectEpoch;
        _sidebarRequest?.Cancel(); _sidebarRequest?.Dispose();
        var cancellation = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
        _sidebarRequest = cancellation; _sidebarLoading = true; _sidebarDirty = false;
        try { await LoadSidebarCoreAsync(epoch, cancellation.Token, candidate, target, revision, selectionGeneration); }
        finally
        {
            if (ReferenceEquals(_sidebarRequest, cancellation)) { _sidebarRequest = null; _sidebarLoading = false; }
            cancellation.Dispose();
            if (!_closed && _sidebarDirty && !_sidebarLoading) QueueSidebarRefresh();
        }
    }

    private async Task LoadSidebarCoreAsync(int epoch, CancellationToken token, SidebarNavigationState navigation, SidebarNavigationState target, long revision, int selectionGeneration)
    {
        var search = SearchBox.Text.Trim();
        var scope = _conversationView + "\n" + search;
        var selection = _selected?.SelectionKey ?? _preferences.LastConversation;
        var page = await _client.ExecutionPostAsync("/internal/runtime/execution/sidebar", new
        {
            view = _conversationView, search, limits = navigation.Limits, modes = navigation.Modes,
            cursors = navigation.Cursors, default_mode = navigation.DefaultCollapsed ? "collapsed" : "auto",
            selected_id = selection == "unattributed" ? "" : selection
        }, token);
        token.ThrowIfCancellationRequested();
        if (_closed || epoch != _objectEpoch || selectionGeneration != _generation ||
            scope != _conversationView + "\n" + SearchBox.Text.Trim() || !ReferenceEquals(target, CurrentNavigation())) return;
        var parsedRows = ParseSidebarRows(page);
        foreach (var group in page.Array("groups"))
            if (group.Text("mode") == "history") navigation.For(group.Text("workspace_id")).AcceptHistory(group.Text("history_cursor"), (int)group.Number("history_limit"));
        if (!target.TryCommit(navigation, revision)) return;
        StartSidebarStream((ulong)page.Number("latest_seq"));
        var freshScope = scope != _sidebarScope; _sidebarScope = scope;
        var selectedKeys = ObjectsList.SelectedItems.Cast<ExecutionObject>().Where(item => !item.IsGroupFooter).Select(item => item.SelectionKey).ToHashSet();
        var scroll = FindVisualChild<ScrollViewer>(ObjectsList); var offset = scroll?.VerticalOffset ?? 0;
        var old = Objects.ToDictionary(item => item.SelectionKey, StringComparer.Ordinal);
        var incomingGroups = new List<WorkspaceGroupKey>();
        var groupRows = new Dictionary<string, List<ExecutionObject>>(StringComparer.Ordinal);
        var previousOrder = Objects.Select(item => item.WorkspaceKey.Id).Distinct().ToArray();
        _initializingGroup = true;
        try
        {
            foreach (var group in page.Array("groups"))
            {
                var id = group.Text("workspace_id");
                if (!_sidebarGroups.TryGetValue(id, out var key)) _sidebarGroups[id] = key = new(id, group.Text("title"));
                key.Apply(group); incomingGroups.Add(key);
                var state = target.For(id);
                key.IsExpanded = group.Text("mode") == "history" || state.Expanded(key.RecentCount);
                var rows = parsedRows[id];
                foreach (var incoming in rows)
                {
                    incoming.WorkspaceKey = key;
                    PreserveProvisionalTitle(incoming, incoming.Snapshot.Text("title"));
                }
                // The server supplies the immutable history order. Client-side
                // recency promotion must not invalidate its pagination boundary.
                rows.Add(new ExecutionObject { Id = "footer:" + id, IsGroupFooter = true, HasMore = group.Flag("has_more"), IsPaging = _sidebarPaging.Contains(id), WorkspaceKey = key, WorkspaceId = id });
                groupRows[id] = rows;
            }
            MergeUnacknowledgedSidebarCalls(page.Number("latest_seq"), target, incomingGroups, groupRows);
        }
        finally { _initializingGroup = false; }
        var orderedGroups = SidebarOrdering.Stable(freshScope ? [] : previousOrder, incomingGroups, key => key.Id, key => key.LastActivityAt);
        var desired = orderedGroups.SelectMany(key => groupRows[key.Id]).ToList();
        _updating = true;
        try
        {
            for (var index = 0; index < desired.Count; index++)
            {
                var incoming = desired[index];
                if (!old.TryGetValue(incoming.SelectionKey, out var existing) || existing.WorkspaceKey.Id != incoming.WorkspaceKey.Id) continue;
                existing.Apply(incoming); desired[index] = existing;
            }
            var retained = desired.ToHashSet();
            for (var index = Objects.Count - 1; index >= 0; index--) if (!retained.Contains(Objects[index])) Objects.RemoveAt(index);
            for (var index = 0; index < desired.Count; index++)
            {
                if (index < Objects.Count && ReferenceEquals(Objects[index], desired[index])) continue;
                var oldIndex = Objects.IndexOf(desired[index]);
                if (oldIndex >= 0) Objects.Move(oldIndex, index); else Objects.Insert(index, desired[index]);
            }
            foreach (var item in Objects.Where(item => !item.IsGroupFooter))
            {
                if (item.Id.Length > 0) _conversationTitles[item.Id] = item.Title;
                if (selectedKeys.Contains(item.SelectionKey) && !ObjectsList.SelectedItems.Contains(item)) ObjectsList.SelectedItems.Add(item);
            }
            if (!previousOrder.SequenceEqual(orderedGroups.Select(key => key.Id))) CollectionViewSource.GetDefaultView(Objects).Refresh();
            MoreObjectsButton.Visibility = Visibility.Collapsed;
            SidebarEmpty.Visibility = incomingGroups.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
        }
        finally { _updating = false; }
        foreach (var stale in _sidebarGroups.Keys.Except(incomingGroups.Select(key => key.Id)).ToArray()) _sidebarGroups.Remove(stale);
        _lastSidebarRefresh = Stopwatch.GetTimestamp();
        var selected = Objects.FirstOrDefault(item => !item.IsGroupFooter && item.SelectionKey == selection);
        var selectedRaw = page.Field("selected");
        if (selected is null && selectedRaw.ValueKind == JsonValueKind.Object)
        {
            var incoming = ExecutionObject.From(selectedRaw, "conversation"); PreserveProvisionalTitle(incoming, selectedRaw.Text("title"));
            if (_selected?.Id == incoming.Id) { _selected.Apply(incoming); selected = _selected; } else selected = incoming;
        }
        selected ??= Objects.FirstOrDefault(item => !item.IsGroupFooter);
        _activityClock.Synchronize(page.Date("server_now"));
        if (selected?.SelectionKey != _selected?.SelectionKey)
        {
            _updating = true;
            try { ObjectsList.SelectedItem = Objects.Contains(selected!) ? selected : null; }
            finally { _updating = false; }
            await SelectObjectAsync(selected);
        }
        else if (selected is not null)
        {
            var previous = _conversationSnapshot;
            _selected = selected; ObjectTitle.Text = selected.Title; ObjectTitle.ToolTip = selected.Title;
            if (!selected.IsUnknown && !selected.IsOrphan)
            {
                _conversationSnapshot = selected.Snapshot;
                var oldState = previous.Field("state"); var state = _conversationSnapshot.Field("state");
                if (!previous.Array("task_ids").Select(item => item.ToString()).SequenceEqual(_conversationSnapshot.Array("task_ids").Select(item => item.ToString())) ||
                    oldState.Text("active_task_id") != state.Text("active_task_id") || oldState.Text("active_task_thread_id") != state.Text("active_task_thread_id"))
                    await GuardAsync(() => LoadConversationTasksAsync(_generation));
            }
        }
        _activityClock.Refresh(); UpdateStopButton();
        if (!freshScope && scroll is not null && Math.Abs(scroll.VerticalOffset - offset) > 0.1)
            await Dispatcher.InvokeAsync(() => scroll.ScrollToVerticalOffset(offset), DispatcherPriority.Loaded);
    }

    private static Dictionary<string, List<ExecutionObject>> ParseSidebarRows(JsonElement page)
    {
        if (page.ValueKind != JsonValueKind.Object || page.Field("groups").ValueKind != JsonValueKind.Array)
            throw new JsonException("项目列表响应缺少 groups。");
        var parsed = new Dictionary<string, List<ExecutionObject>>(StringComparer.Ordinal);
        var ids = new HashSet<string>(StringComparer.Ordinal);
        foreach (var group in page.Array("groups"))
        {
            var id = group.Text("workspace_id");
            if (id.Length == 0 || parsed.ContainsKey(id) || group.Field("conversations").ValueKind != JsonValueKind.Array ||
                group.Number("history_limit") is < 0 or > int.MaxValue)
                throw new JsonException("项目分页响应无效，原列表已保留。");
            var rows = new List<ExecutionObject>();
            foreach (var raw in group.Array("conversations"))
            {
                var row = ExecutionObject.From(raw, "conversation");
                if (row.Id.Length == 0 || row.Id.StartsWith("footer:", StringComparison.Ordinal) || !ids.Add(row.Id))
                    throw new JsonException("对话标识缺失或重复，原列表已保留。");
                rows.Add(row);
            }
            parsed.Add(id, rows);
        }
        return parsed;
    }

    private async void SidebarFooter_PreviewMouseDown(object sender, MouseButtonEventArgs e)
    {
        await GuardAsync(async () =>
        {
            if (sender is not ListBoxItem { DataContext: ExecutionObject { IsGroupFooter: true } footer }) return;
            e.Handled = true;
            await LoadMoreProjectConversationsAsync(footer);
        });
    }
    private async void SidebarMore_Click(object sender, RoutedEventArgs e)
    {
        await GuardAsync(async () =>
        {
            e.Handled = true;
            if ((sender as FrameworkElement)?.DataContext is ExecutionObject { IsGroupFooter: true } footer)
                await LoadMoreProjectConversationsAsync(footer);
        });
    }
    private async Task LoadMoreProjectConversationsAsync(ExecutionObject footer)
    {
        if (!_initialized || _closed || !footer.CanLoadMore) return;
        var id = footer.WorkspaceKey.Id;
        if (!_sidebarPaging.Add(id)) return;
        footer.IsPaging = true;
        var target = CurrentNavigation();
        var intent = target.For(id).IntentRevision;
        var scope = _conversationView + "\n" + SearchBox.Text.Trim();
        var selection = _generation;
        var acquired = false;
        try
        {
            // Serialize different projects without losing an accepted click.
            // Duplicate clicks for the same project never enter this queue.
            await _sidebarPageGate.WaitAsync(_lifetime.Token);
            acquired = true;
            if (_closed || selection != _generation || scope != _conversationView + "\n" + SearchBox.Text.Trim() ||
                !ReferenceEquals(target, CurrentNavigation()) || target.For(id).IntentRevision != intent) return;
            var candidate = target.Copy();
            candidate.For(id).More();
            await LoadSidebarAsync(candidate);
        }
        finally
        {
            if (acquired) _sidebarPageGate.Release();
            _sidebarPaging.Remove(id);
            footer.IsPaging = false;
            foreach (var row in Objects.Where(row => row.IsGroupFooter && row.WorkspaceKey.Id == id)) row.IsPaging = false;
        }
    }

    private void WorkspaceGroup_Loaded(object sender, RoutedEventArgs e)
    {
        if (sender is not Expander expander || expander.DataContext is not CollectionViewGroup { Name: WorkspaceGroupKey key }) return;
        _initializingGroup = true;
        try { expander.SetCurrentValue(Expander.IsExpandedProperty, key.IsExpanded); }
        finally { _initializingGroup = false; }
    }
    private async void WorkspaceGroup_Expanded(object sender, RoutedEventArgs e)
    {
        if (!_initialized || _updating || _initializingGroup || sender is not Expander { IsLoaded: true, DataContext: CollectionViewGroup { Name: WorkspaceGroupKey key } }) return;
        CurrentNavigation().For(key.Id).Expand(); await GuardAsync(LoadSidebarAsync);
    }
    private async void WorkspaceGroup_Collapsed(object sender, RoutedEventArgs e)
    {
        if (!_initialized || _updating || _initializingGroup || sender is not Expander { IsLoaded: true, DataContext: CollectionViewGroup { Name: WorkspaceGroupKey key } }) return;
        CurrentNavigation().For(key.Id).Collapse(); await GuardAsync(LoadSidebarAsync);
    }
    private async void CollapseAllProjects_Click(object sender, RoutedEventArgs e)
    {
        _allProjectsCollapsed = true;
        foreach (var navigation in _sidebarViews.Values) navigation.CollapseAll();
        _searchNavigation?.CollapseAll(); CurrentNavigation().CollapseAll();
        _initializingGroup = true;
        try { foreach (var group in _sidebarGroups.Values) group.IsExpanded = false; }
        finally { _initializingGroup = false; }
        await GuardAsync(LoadSidebarAsync);
    }
    private void RefreshAutoProjectExpansion()
    {
        if (_closed || !_initialized || _updating || _initializingGroup) return;
        var navigation = CurrentNavigation();
        var active = Objects.Where(item => !item.IsGroupFooter && item.RecentlyActive).GroupBy(item => item.WorkspaceKey.Id).ToDictionary(group => group.Key, group => group.Count());
        _initializingGroup = true;
        try
        {
            foreach (var group in _sidebarGroups.Values)
            {
                var state = navigation.For(group.Id);
                if (group.Mode == "history" && state.Mode != ProjectNavigationMode.Collapsed) continue;
                group.IsExpanded = state.Expanded(active.GetValueOrDefault(group.Id));
            }
        }
        finally { _initializingGroup = false; }
    }

    private void Project_RightClick(object sender, MouseButtonEventArgs e)
    {
        if (sender is not FrameworkElement { DataContext: CollectionViewGroup { Name: WorkspaceGroupKey key } } anchor) return;
        e.Handled = true; var root = key.Root; var menu = Menu(anchor);
        ActionMenu(menu, "打开项目目录", () =>
        {
            if (!Path.IsPathFullyQualified(root) || !Directory.Exists(root)) throw new IOException("项目目录不存在或已移动。");
            Process.Start(new ProcessStartInfo { FileName = root, UseShellExecute = true }); return Task.CompletedTask;
        }, !string.IsNullOrWhiteSpace(root));
        OpenMenu(menu);
    }
    private async Task OpenSidebarObjectAsync(ExecutionObject row)
    {
        if (row.IsGroupFooter) return;
        _updating = true;
        try { ObjectsList.SelectedItem = row; }
        finally { _updating = false; }
        if (_selected?.SelectionKey != row.SelectionKey) await SelectObjectAsync(row);
    }
}
