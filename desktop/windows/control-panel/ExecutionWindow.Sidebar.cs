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

    private async Task LoadSidebarAsync()
    {
        var epoch = ++_objectEpoch;
        _sidebarRequest?.Cancel(); _sidebarRequest?.Dispose();
        var cancellation = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
        _sidebarRequest = cancellation; _sidebarLoading = true; _sidebarDirty = false;
        try { await LoadSidebarCoreAsync(epoch, cancellation.Token); }
        finally
        {
            if (ReferenceEquals(_sidebarRequest, cancellation)) { _sidebarRequest = null; _sidebarLoading = false; }
            cancellation.Dispose();
            if (!_closed && _sidebarDirty && !_sidebarLoading) QueueSidebarRefresh();
        }
    }

    private async Task LoadSidebarCoreAsync(int epoch, CancellationToken token)
    {
        var search = SearchBox.Text.Trim();
        var scope = _conversationView + "\n" + search;
        var navigation = CurrentNavigation();
        var selection = _selected?.SelectionKey ?? _preferences.LastConversation;
        var page = await _client.ExecutionPostAsync("/internal/runtime/execution/sidebar", new
        {
            view = _conversationView, search, limits = navigation.Limits, modes = navigation.Modes,
            cursors = navigation.Cursors, default_mode = navigation.DefaultCollapsed ? "collapsed" : "auto",
            selected_id = selection == "unattributed" ? "" : selection
        }, token);
        if (_closed || epoch != _objectEpoch || scope != _conversationView + "\n" + SearchBox.Text.Trim()) return;
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
                var state = navigation.For(id);
                if (group.Text("mode") == "history") state.AcceptHistory(group.Text("history_cursor"), (int)group.Number("history_limit"));
                key.IsExpanded = group.Text("mode") == "history" || state.Expanded(key.RecentCount);
                var rows = new List<ExecutionObject>();
                foreach (var value in group.Array("conversations"))
                {
                    var incoming = ExecutionObject.From(value, "conversation"); incoming.WorkspaceKey = key;
                    PreserveProvisionalTitle(incoming, value.Text("title")); rows.Add(incoming);
                }
                // The server supplies the immutable history order. Client-side
                // recency promotion must not invalidate its pagination boundary.
                rows.Add(new ExecutionObject { Id = "footer:" + id, IsGroupFooter = true, HasMore = group.Flag("has_more"), WorkspaceKey = key, WorkspaceId = id });
                groupRows[id] = rows;
            }
            MergeUnacknowledgedSidebarCalls(page.Number("latest_seq"), navigation, incomingGroups, groupRows);
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

    private void SidebarFooter_PreviewMouseDown(object sender, MouseButtonEventArgs e)
    {
        if (sender is not ListBoxItem { DataContext: ExecutionObject { IsGroupFooter: true } }) return;
        e.Handled = true;
        if (Ancestor<System.Windows.Controls.Button>(e.OriginalSource as DependencyObject) is { } button) SidebarMore_Click(button, new RoutedEventArgs());
    }
    private async void SidebarMore_Click(object sender, RoutedEventArgs e)
    {
        e.Handled = true;
        if ((sender as FrameworkElement)?.DataContext is not ExecutionObject { IsGroupFooter: true, HasMore: true } footer) return;
        var id = footer.WorkspaceKey.Id;
        if (!_sidebarPaging.Add(id)) return;
        try { CurrentNavigation().For(id).More(); await GuardAsync(LoadSidebarAsync); }
        finally { _sidebarPaging.Remove(id); }
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
        ActionMenu(menu, UiText.Get("ExecutionOpenProjectFolder"), () =>
        {
            if (!Path.IsPathFullyQualified(root) || !Directory.Exists(root)) throw new IOException(UiText.Get("ExecutionProjectFolderMissing"));
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
