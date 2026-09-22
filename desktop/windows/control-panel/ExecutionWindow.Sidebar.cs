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
    private readonly Dictionary<string, int> _sidebarLimits = new(StringComparer.Ordinal);
    private readonly HashSet<string> _sidebarPaging = new(StringComparer.Ordinal);
    private string _sidebarScope = "";
    private bool _initializingGroup;
    private int _openMenus;
    private long _lastSidebarRefresh;

    private IEnumerable<ExecutionObject> ActivityItems()
    {
        foreach (var item in Objects.Where(item => !item.IsGroupFooter)) yield return item;
        if (_selected is { IsGroupFooter: false } selected && !Objects.Contains(selected)) yield return selected;
    }

    private async Task LoadSidebarAsync()
    {
        var epoch = ++_objectEpoch;
        var scope = _conversationView + "\n" + SearchBox.Text.Trim();
        var selection = _selected?.SelectionKey ?? _preferences.LastConversation;
        var page = await _client.ExecutionPostAsync("/internal/runtime/execution/sidebar", new
        {
            view = _conversationView, search = SearchBox.Text.Trim(),
            limits = _sidebarLimits.ToDictionary(pair => pair.Key, pair => pair.Value),
            selected_id = selection == "unattributed" ? "" : selection
        }, _lifetime.Token);
        if (_closed || epoch != _objectEpoch || scope != _conversationView + "\n" + SearchBox.Text.Trim()) return;
        var freshScope = scope != _sidebarScope;
        _sidebarScope = scope;
        var selectedKeys = ObjectsList.SelectedItems.Cast<ExecutionObject>().Where(item => !item.IsGroupFooter).Select(item => item.SelectionKey).ToHashSet();
        var scroll = FindVisualChild<ScrollViewer>(ObjectsList);
        var offset = scroll?.VerticalOffset ?? 0;
        var old = Objects.ToDictionary(item => item.SelectionKey, StringComparer.Ordinal);
        var incomingGroups = new List<WorkspaceGroupKey>();
        var groupRows = new Dictionary<string, List<ExecutionObject>>(StringComparer.Ordinal);
        var previousOrder = Objects.Select(item => item.WorkspaceKey.Id).Distinct().ToArray();
        foreach (var group in page.Array("groups"))
        {
            var id = group.Text("workspace_id");
            if (!_sidebarGroups.TryGetValue(id, out var key)) _sidebarGroups[id] = key = new(id, group.Text("title"));
            key.Apply(group); incomingGroups.Add(key);
            var rows = new List<ExecutionObject>();
            foreach (var value in group.Array("conversations"))
            {
                var incoming = ExecutionObject.From(value, "conversation");
                incoming.WorkspaceKey = key;
                rows.Add(incoming);
            }
            var previousRows = freshScope ? [] : Objects.Where(item => !item.IsGroupFooter && item.WorkspaceKey.Id == id).Select(item => item.SelectionKey).ToArray();
            rows = SidebarOrdering.Stable(previousRows, rows, item => item.SelectionKey, item => item.SortActivityAt, item => item.Pinned);
            rows.Add(new ExecutionObject { Id = "footer:" + id, IsGroupFooter = true, HasMore = group.Flag("has_more"),
                AutoLoadMore = _sidebarLimits.GetValueOrDefault(id) >= 200, WorkspaceKey = key, WorkspaceId = id });
            groupRows[id] = rows;
        }
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
            for (var index = Objects.Count - 1; index >= 0; index--)
                if (!retained.Contains(Objects[index])) Objects.RemoveAt(index);
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
            if (!previousOrder.SequenceEqual(orderedGroups.Select(key => key.Id)))
                CollectionViewSource.GetDefaultView(Objects).Refresh();
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
            var incoming = ExecutionObject.From(selectedRaw, "conversation");
            if (_selected?.Id == incoming.Id) { _selected.Apply(incoming); selected = _selected; }
            else selected = incoming;
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
        if (Ancestor<System.Windows.Controls.Button>(e.OriginalSource as DependencyObject) is { } button)
            SidebarMore_Click(button, new RoutedEventArgs());
    }

    private async void SidebarMore_Click(object sender, RoutedEventArgs e)
    {
        e.Handled = true;
        if ((sender as FrameworkElement)?.DataContext is not ExecutionObject { IsGroupFooter: true } footer) return;
        var id = footer.WorkspaceKey.Id;
        var current = _sidebarLimits.GetValueOrDefault(id);
        if (current == 0 && (SearchBox.Text.Trim().Length > 0 || _conversationView != "active")) current = 200;
        _sidebarLimits[id] = current == 0 ? 15 : current == 15 ? 200 : checked(current + 200);
        await GuardAsync(LoadSidebarAsync);
    }

    private async void SidebarMore_Loaded(object sender, RoutedEventArgs e)
    {
        if (_updating || _closed || sender is not FrameworkElement { IsVisible: true, DataContext: ExecutionObject { IsGroupFooter: true, HasMore: true, AutoLoadMore: true } footer } element) return;
        if (!element.IsDescendantOf(ObjectsList)) return;
        var bounds = element.TransformToAncestor(ObjectsList).TransformBounds(new Rect(0, 0, element.ActualWidth, element.ActualHeight));
        if (!bounds.IntersectsWith(new Rect(0, 0, ObjectsList.ActualWidth, ObjectsList.ActualHeight))) return;
        var id = footer.WorkspaceKey.Id;
        if (_preferences.CollapsedWorkspaces.Contains(id) || !_sidebarPaging.Add(id)) return;
        try
        {
            var current = _sidebarLimits.GetValueOrDefault(id);
            if (!_sidebarGroups.TryGetValue(id, out var group) || current >= group.Total) return;
            _sidebarLimits[id] = checked(current + 200);
            await GuardAsync(LoadSidebarAsync);
        }
        finally { _sidebarPaging.Remove(id); }
    }

    private void Sidebar_ScrollChanged(object sender, ScrollChangedEventArgs e)
    {
        if (_updating || _closed || _sidebarPaging.Count > 0) return;
        var pending = new Stack<DependencyObject>(); pending.Push(ObjectsList);
        while (pending.TryPop(out var node))
        {
            if (node is System.Windows.Controls.Button { DataContext: ExecutionObject { IsGroupFooter: true, AutoLoadMore: true, HasMore: true } } footer)
                SidebarMore_Loaded(footer, new RoutedEventArgs());
            if (_sidebarPaging.Count > 0) return;
            for (var index = 0; index < System.Windows.Media.VisualTreeHelper.GetChildrenCount(node); index++)
                pending.Push(System.Windows.Media.VisualTreeHelper.GetChild(node, index));
        }
    }

    private void WorkspaceGroup_Loaded(object sender, RoutedEventArgs e)
    {
        if (sender is not Expander expander || expander.DataContext is not CollectionViewGroup { Name: WorkspaceGroupKey key }) return;
        _initializingGroup = true;
        try { expander.IsExpanded = !_preferences.CollapsedWorkspaces.Contains(key.Id); }
        finally { _initializingGroup = false; }
    }
    private async void WorkspaceGroup_Expanded(object sender, RoutedEventArgs e)
    {
        if (!_initialized || _updating || _initializingGroup || sender is not Expander { IsLoaded: true, DataContext: CollectionViewGroup { Name: WorkspaceGroupKey key } }) return;
        if (_preferences.CollapsedWorkspaces.Remove(key.Id))
        {
            _sidebarLimits.Remove(key.Id); SavePreferences();
            await GuardAsync(LoadSidebarAsync);
        }
    }
    private async void WorkspaceGroup_Collapsed(object sender, RoutedEventArgs e)
    {
        if (!_initialized || _updating || _initializingGroup || sender is not Expander { IsLoaded: true, DataContext: CollectionViewGroup { Name: WorkspaceGroupKey key } }) return;
        _preferences.CollapsedWorkspaces.Add(key.Id); _sidebarLimits.Remove(key.Id); SavePreferences();
        await GuardAsync(LoadSidebarAsync);
    }

    private void Project_RightClick(object sender, MouseButtonEventArgs e)
    {
        if (sender is not FrameworkElement { DataContext: CollectionViewGroup { Name: WorkspaceGroupKey key } } anchor) return;
        e.Handled = true;
        var root = key.Root;
        var menu = Menu(anchor);
        ActionMenu(menu, "打开项目目录", () =>
        {
            if (!Path.IsPathFullyQualified(root) || !Directory.Exists(root)) throw new IOException("项目目录不存在或已移动。");
            Process.Start(new ProcessStartInfo { FileName = root, UseShellExecute = true });
            return Task.CompletedTask;
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
