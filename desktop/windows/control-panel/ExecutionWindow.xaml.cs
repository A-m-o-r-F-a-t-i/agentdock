using System.Collections.ObjectModel;
using System.ComponentModel;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Data;
using System.Windows.Input;
using System.Windows.Media;
using System.Windows.Threading;
using Microsoft.Win32;
using Button = System.Windows.Controls.Button;
using ComboBox = System.Windows.Controls.ComboBox;
using RadioButton = System.Windows.Controls.RadioButton;
using ListBox = System.Windows.Controls.ListBox;
using KeyEventArgs = System.Windows.Input.KeyEventArgs;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow : Window
{
    private readonly RuntimeService _runtime;
    private readonly ActivityClient _client;
    private readonly ConversationActivityClock _activityClock;
    private readonly CancellationTokenSource _lifetime = new();
    private CancellationTokenSource? _selectionCancellation, _streamCancellation;
    private Task? _streamTask;
    private readonly DispatcherTimer _filterTimer = new() { Interval = TimeSpan.FromMilliseconds(300) };
    private readonly DispatcherTimer _callSearchTimer = new() { Interval = TimeSpan.FromMilliseconds(350) };
    private readonly DispatcherTimer _pulse = new() { Interval = TimeSpan.FromSeconds(1) };
    private readonly Dictionary<string, ExecutionCallRow> _callsById = [];
    private readonly Dictionary<string, string> _workspaceNames = [];
    private readonly Dictionary<string, string> _conversationTitles = [];
    private readonly Dictionary<string, (double Offset, bool Follow)> _scrollStates = [];
    private readonly HashSet<string> _detailReads = [];
    private ExecutionPreferences _preferences = new();
    private ExecutionObject? _selected;
    private ExecutionCallRow? _detailCall;
    private JsonElement _conversationSnapshot, _taskSnapshot;
    private string _currentConversationTaskId = "", _selectedTaskId = "", _branch = "", _taskFilter = "";
    private string _conversationView = "active", _callView = "active";
    private int _generation, _objectEpoch, _streamEpoch, _taskEpoch, _ticks;
    private ulong _before, _cursor;
    private bool _initialized, _updating, _tickRunning, _closed, _following = true, _preferencesWritable = true;
    private bool _streamConnected;
    private long _lastPending;
    private string[] _menuSelection = [];
    private string[]? _frozenSelection;
    public ObservableCollection<ExecutionObject> Objects { get; } = [];
    public ObservableCollection<ExecutionCallRow> Calls { get; } = [];
    public static readonly DependencyProperty ShowTimestampsProperty = DependencyProperty.Register(nameof(ShowTimestamps), typeof(bool), typeof(ExecutionWindow), new PropertyMetadata(true));
    public bool ShowTimestamps { get => (bool)GetValue(ShowTimestampsProperty); set => SetValue(ShowTimestampsProperty, value); }
    private CancellationToken SelectionToken => _selectionCancellation?.Token ?? _lifetime.Token;

    public ExecutionWindow(RuntimeService runtime) : this(runtime, new ActivityClient(runtime)) { }
    internal ExecutionWindow(RuntimeService runtime, ActivityClient client)
    {
        _runtime = runtime; _client = client;
        _activityClock = new ConversationActivityClock(ActivityItems);
        DesktopTheme.Initialize(runtime.RuntimeRoot);
        InitializeComponent(); DataContext = this;
        InitializeComposer();
		InitializeSidebar();
		_activityClock.Changed += (_, _) => { UpdateStopButton(); RefreshAutoProjectExpansion(); };
        CollectionViewSource.GetDefaultView(Objects).GroupDescriptions.Add(new PropertyGroupDescription(nameof(ExecutionObject.WorkspaceKey)));
        _filterTimer.Tick += async (_, _) => { _filterTimer.Stop(); await GuardAsync(() => LoadObjectsAsync()); };
        _callSearchTimer.Tick += async (_, _) => { _callSearchTimer.Stop(); await GuardAsync(() => LoadCallsAsync(false)); };
        _pulse.Tick += async (_, _) => await TickAsync();
    }
    private async void Window_Loaded(object sender, RoutedEventArgs e)
    {
        LoadPreferences(); FontSize = _preferences.FontSize; ApplyTheme(); ApplyCallPresentation();
        _conversationView = _preferences.LastView is "archived" or "trash" ? _preferences.LastView : "active";
        DesktopTheme.Changed += Theme_Changed;
        await GuardAsync(async () => { await LoadWorkspacesAsync(); _initialized = true; await RefreshOverviewAsync(); await LoadObjectsAsync(); });
        _initialized = true; _pulse.Start(); _ready.TrySetResult();
    }
    private async Task GuardAsync(Func<Task> action)
    {
        try { await action(); }
        catch (OperationCanceledException) { }
        catch (Exception ex) when (ex is HttpRequestException or IOException or JsonException or InvalidOperationException or UnauthorizedAccessException or ArgumentException)
        { if (!_closed) Warn(ex.Message); }
    }
    private string _warningCode = "";
    private void Warn(string text, string code = "")
    {
        if (code.Length > 0 && _preferences.DismissedNotices.Contains(code)) return;
        _warningCode = code;
        WarningText.Text = text; WarningPanel.Visibility = string.IsNullOrWhiteSpace(text) ? Visibility.Collapsed : Visibility.Visible;
    }
    private static string Escape(string value) => Uri.EscapeDataString(value);
    private static string ComboValue(ComboBox combo) => (combo.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "";
    private string ListQuery(bool selection = false) => $"view={_conversationView}&search={Escape(SearchBox.Text.Trim())}&limit=200" + (selection ? "&selection=true" : "");
    private string CallScopeQuery()
    {
        var scope = _selected is null ? "unattributed=true" : _selected.IsUnknown ? "unattributed=true" : "conversation_id=" + Escape(_selected.Id);
        if (_taskFilter.Length > 0) scope += "&task_id=" + Escape(_taskFilter);
        return scope + "&top_level=true&view=" + _callView + "&status=" + Escape(ComboValue(CallStatusCombo)) + "&search=" + Escape(CallSearchBox.Text.Trim());
    }
    private async Task LoadWorkspacesAsync()
    {
        var value = await _client.ExecutionGetAsync("/internal/runtime/permissions/effective", _lifetime.Token);
        _workspaceNames.Clear();
        foreach (var workspace in value.Array("workspaces")) _workspaceNames[workspace.Text("workspace_id")] = workspace.Text("name");
    }
    private async Task RefreshOverviewAsync()
    {
        var value = await _client.ExecutionGetAsync("/internal/runtime/execution", _lifetime.Token);
        var activities = value.Field("conversation_activity");
        foreach (var item in ActivityItems())
        {
            var facts = activities.Field(item.Id);
            var latest = facts.Date("last_tool_call_at");
            if (latest is not null && (item.LastToolCallAt is null || latest > item.LastToolCallAt)) item.LastToolCallAt = latest;
            var changed = facts.Date("last_activity_at");
            if (changed is not null && (item.LastActivityAt is null || changed > item.LastActivityAt)) item.LastActivityAt = changed;
            item.PendingCount = facts.Number("pending"); item.RunningCount = facts.Number("running");
			item.InFlight = value.Field("in_flight").Flag(item.Id); item.RefreshActivity();
        }
        _activityClock.Synchronize(value.Date("server_now"));
        var pending = value.Field("statistics").Number("pending");
        AttentionButton.Visibility = pending > 0 ? Visibility.Visible : Visibility.Collapsed;
        AttentionButton.Content = "待处理 " + pending;
        if (_initialized && _preferences.Notifications && pending > _lastPending && _lastPending > 0 && WarningPanel.Visibility != Visibility.Visible) Warn($"新增 {pending - _lastPending} 项待审批请求。");
        _lastPending = pending;
    }
    private Task LoadObjectsAsync(bool more = false) => LoadSidebarAsync();
    private async Task SelectObjectAsync(ExecutionObject? item)
    {
        if (_selected is not null) _scrollStates[_selected.SelectionKey] = (FindVisualChild<ScrollViewer>(CallsList)?.VerticalOffset ?? 0, _following);
        SaveComposerDraft();
        _generation++; _taskEpoch++; _streamEpoch++;
        _selectionCancellation?.Cancel(); _selectionCancellation?.Dispose();
        _selectionCancellation = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
        _streamCancellation?.Cancel();
        _selected = item; RestoreComposerDraft(); _taskFilter = ""; _selectedTaskId = ""; _currentConversationTaskId = ""; _branch = "";
        _conversationSnapshot = _taskSnapshot = default;
        CloseDetails(); Calls.Clear(); _callsById.Clear(); TaskChoiceCombo.ItemsSource = null;
        ConversationProgressCard.Visibility = Visibility.Collapsed;
        FilterTaskButton.Content = "筛选此任务"; Warn("");
        ObjectTitle.Text = item?.Title ?? "选择对话"; ObjectTitle.ToolTip = item?.Title;
        EmptyPanel.Visibility = Visibility.Visible; EmptyText.Text = item is null ? "暂无对话记录" : "正在读取执行记录";
        UpdateStopButton();
        if (item is null) return;
        _preferences.LastConversation = item.SelectionKey;
        _following = !_scrollStates.TryGetValue(item.SelectionKey, out var position) || position.Follow;
        UpdateFollowButton();
        var generation = _generation;
        if (!item.IsUnknown && !item.IsOrphan)
        {
            var value = await _client.ExecutionGetAsync("/internal/runtime/conversations/" + Escape(item.Id), SelectionToken);
            if (generation != _generation) return;
            _conversationSnapshot = value.Field("conversation");
            if (_conversationSnapshot.HasDate("terminated_at")) Warn("此对话已终止，后续执行已被拦截。历史记录仍可查看和管理。");
            await GuardAsync(() => LoadConversationTasksAsync(generation));
        }
        await LoadCallsAsync(false);
        if (generation != _generation) return;
        if (!_following && _scrollStates.TryGetValue(item.SelectionKey, out position))
            await Dispatcher.InvokeAsync(() => FindVisualChild<ScrollViewer>(CallsList)?.ScrollToVerticalOffset(position.Offset), DispatcherPriority.Loaded);
        UpdateStopButton();
    }
    private async Task LoadConversationTasksAsync(int generation)
    {
        var state = _conversationSnapshot.Field("state");
        _currentConversationTaskId = state.Text("active_task_id");
        var ids = _conversationSnapshot.Array("task_ids").Where(value => value.ValueKind == JsonValueKind.String).Select(value => value.GetString()!).ToList();
        if (_currentConversationTaskId.Length > 0) { ids.Remove(_currentConversationTaskId); ids.Insert(0, _currentConversationTaskId); }
        var choices = new List<ExecutionChoice>();
        foreach (var id in ids.Take(50))
        {
            try
            {
                var result = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Escape(id), SelectionToken);
                var task = result.Field("task"); if (task.ValueKind == JsonValueKind.Undefined) task = result;
                choices.Add(new(id, task.Text("title", "历史任务")));
            }
            catch (HttpRequestException ex) when (ex.StatusCode is HttpStatusCode.NotFound or HttpStatusCode.Gone) { }
        }
        if (generation != _generation) return;
        var chosen = choices.FirstOrDefault(choice => choice.Id == _selectedTaskId) ?? choices.FirstOrDefault();
        _updating = true;
        try { TaskChoiceCombo.ItemsSource = choices; TaskChoiceCombo.SelectedValue = chosen?.Id; }
        finally { _updating = false; }
        ConversationProgressCard.Visibility = Visibility.Visible;
        TaskChoiceCombo.Visibility = choices.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        NoTaskPanel.Visibility = choices.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
        TaskActionsPanel.Visibility = choices.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        CurrentTaskProgress.Visibility = choices.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        if (choices.Count == 0)
        {
            CurrentTaskStatus.Text = CurrentTaskNext.Text = "";
            CurrentTaskProgress.Visibility = Visibility.Collapsed;
        }
        _selectedTaskId = chosen?.Id ?? "";
        if (_selectedTaskId.Length > 0) await LoadTaskAsync(_selectedTaskId, "", false);
    }
    private async Task LoadTaskAsync(string id, string branch, bool details)
    {
        var generation = _generation; var epoch = ++_taskEpoch;
        var raw = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Escape(id), SelectionToken);
        var task = raw.Field("task"); if (task.ValueKind == JsonValueKind.Undefined) task = raw;
        JsonElement threadList;
        try { threadList = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Escape(id) + "/threads", SelectionToken); }
        catch (HttpRequestException ex) when (ex.StatusCode == HttpStatusCode.NotFound) { threadList = default; }
        var threads = threadList.Array("threads");
        var active = branch.Length > 0 ? branch : id == _currentConversationTaskId ? _conversationSnapshot.Field("state").Text("active_task_thread_id", "main") : task.Text("active_thread_id", "main");
        if (active.Length == 0) active = "main";
        JsonElement threadRaw;
        try { threadRaw = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Escape(id) + "/threads/" + Escape(active), SelectionToken); }
        catch (HttpRequestException ex) when (ex.StatusCode == HttpStatusCode.NotFound) { threadRaw = default; }
        var thread = threadRaw.Field("thread");
        if (generation != _generation || epoch != _taskEpoch) return;
        _taskSnapshot = task;
        var steps = thread.Field("steps").ValueKind == JsonValueKind.Array ? thread.Array("steps") : task.Array("steps");
        var done = steps.Count(step => step.Text("status") == "completed");
        var currentId = thread.Text("current_step_id", task.Text("current_step_id"));
        var current = steps.FirstOrDefault(step => step.Text("id") == currentId).Text("title");
        if (!details)
        {
            CurrentTaskStatus.Text = steps.Length == 0 ? "进度未记录" : $"{done}/{steps.Length}";
            CurrentTaskProgress.Visibility = steps.Length == 0 ? Visibility.Collapsed : Visibility.Visible;
            CurrentTaskProgress.Value = steps.Length == 0 ? 0 : done * 100.0 / steps.Length;
            CurrentTaskNext.Text = current; CurrentTaskNext.ToolTip = current;
        }
        if (details || TaskDetailsPanel.Visibility == Visibility.Visible)
        {
            _branch = active;
            _updating = true;
            try
            {
                BranchCombo.ItemsSource = threads.Select(value => new ExecutionChoice(value.Text("id", "main"), value.Text("title", value.Text("id", "main")))).ToArray();
                BranchCombo.SelectedValue = active;
            }
            finally { _updating = false; }
            var next = thread.Text("next_action", task.Text("next_action"));
            TaskGoalText.Text = task.Text("goal") + (next.Length > 0 ? "\n下一动作：" + next : "");
            TaskStepsText.Text = steps.Length == 0 ? "进度未记录" : string.Join("\n", steps.Select(step => ExecutionJson.State(step.Text("status")) + "  " + step.Text("title")));
            var conditions = task.Array("conditions"); if (conditions.Length == 0) conditions = task.Array("completion_conditions");
            TaskAcceptanceText.Text = "验收条件\n" + (conditions.Length == 0 ? "未记录" : string.Join("\n", conditions.Select(condition => condition.ValueKind == JsonValueKind.String ? condition.GetString() : condition.Text("text", condition.Pretty()))));
            var milestones = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Escape(id) + "/activity?milestones=true&limit=100&after=0", SelectionToken);
            if (generation != _generation || epoch != _taskEpoch) return;
            MilestonesText.Text = string.Join("\n", milestones.Array("events").Select(value => value.Text("summary")));
        }
    }
    private async Task LoadCallsAsync(bool older)
    {
        if (_selected is null) return;
        var query = CallScopeQuery(); var generation = _generation;
        var epoch = older ? _streamEpoch : ++_streamEpoch;
        if (!older) { _streamCancellation?.Cancel(); _historyReadFailed = false; HistoryRetryButton.Visibility = Visibility.Collapsed; }
        var value = await _client.ExecutionGetAsync("/internal/runtime/calls?" + query + "&limit=100" + (older && _before > 0 ? "&before=" + _before : ""), SelectionToken);
        if (generation != _generation || epoch != _streamEpoch || query != CallScopeQuery()) return;
        var previousUpdating = _updating;
        _updating = true;
        try
        {
            if (!older) { Calls.Clear(); _callsById.Clear(); }
            foreach (var call in value.Array("calls").Reverse()) UpsertCall(call);
        }
        finally { _updating = previousUpdating; }
        _before = (ulong)value.Number("next_before");
		_hasOlderCalls = value.Flag("has_more");
        if (value.Flag("gap")) Warn("部分历史记录已过保留期限，当前显示现有记录。", "activity_retention_gap");
        UpdateEmpty();
        if (!older)
        {
            _cursor = (ulong)value.Number("latest_seq");
            _streamCancellation?.Dispose(); _streamCancellation = CancellationTokenSource.CreateLinkedTokenSource(SelectionToken);
            _streamTask = _client.ObserveExecutionsAsync(query, _cursor, message => Dispatcher.InvokeAsync(() => ApplyStreamAsync(message, generation, epoch)).Task.Unwrap(), _streamCancellation.Token);
        }
        if (!older && _following && Calls.Count > 0) await Dispatcher.InvokeAsync(() => CallsList.ScrollIntoView(Calls[^1]), DispatcherPriority.Loaded);
    }
    private bool MatchesScope(ExecutionCallRow row)
    {
        if (_selected is null || _selected.IsUnknown && row.ConversationId.Length > 0 || !_selected.IsUnknown && row.ConversationId != _selected.Id) return false;
        if (_taskFilter.Length > 0 && row.TaskId != _taskFilter) return false;
        return true;
    }
    private void UpsertCall(JsonElement value)
    {
        var incoming = new ExecutionCallRow(value);
        var activeItem = ActivityItems().FirstOrDefault(candidate => candidate.Id == incoming.ConversationId);
        if (activeItem is not null)
        {
            if (incoming.RequestReceivedAt is { } received && (activeItem.LastToolCallAt is null || received > activeItem.LastToolCallAt)) activeItem.LastToolCallAt = received;
            if (incoming.LastActivityAt is { } changed && (activeItem.LastActivityAt is null || changed > activeItem.LastActivityAt)) activeItem.LastActivityAt = changed;
            activeItem.RefreshActivity(); _activityClock.Refresh();
        }
        if (!MatchesScope(incoming)) return;
        var status = ComboValue(CallStatusCombo);
        if (!incoming.VisibleIn(_callView) || status.Length > 0 && incoming.Status != status)
        {
            if (_callsById.Remove(incoming.Id, out var removed)) { Calls.Remove(removed); if (_detailCall?.Id == removed.Id) CloseDetails(); }
            return;
        }
        if (_callsById.TryGetValue(incoming.Id, out var existing)) existing.Apply(value);
        else
        {
            _callsById[incoming.Id] = incoming;
            var index = Calls.Count; while (index > 0 && Calls[index - 1].CreatedSeq > incoming.CreatedSeq) index--;
            Calls.Insert(index, incoming);
            if (_conversationTitles.TryGetValue(incoming.ConversationId, out var title)) incoming.SourceTitle = title;
        }
        var limit = _following ? 1000 : 10000;
        while (Calls.Count > limit) { var removed = _following ? Calls[0] : Calls[^1]; Calls.Remove(removed); _callsById.Remove(removed.Id); }
        UpdateStopButton();
    }
    private async Task ApplyStreamAsync(ExecutionStreamMessage message, int generation, int epoch)
    {
        if (_closed || generation != _generation || epoch != _streamEpoch) return;
		if (message.Kind == "connected") { _streamConnected = true; FollowButton.ToolTip = "跟随最新调用"; }
		else if (message.Kind == "disconnected") { _streamConnected = false; FollowButton.ToolTip = "执行流重连中：" + message.Message; }
        else if (message.Kind == "call")
        {
            _cursor = Math.Max(_cursor, message.Seq); UpsertCall(message.Value); UpdateEmpty();
            if (_following && Calls.Count > 0) CallsList.ScrollIntoView(Calls[^1]);
        }
        else if (message.Kind is "gap" or "warning") Warn(message.Message.Length > 0 ? message.Message : "部分历史记录已不可用。", message.Kind == "gap" ? "activity_retention_gap" : "");
        else if (message.Kind == "reset") await GuardAsync(() => LoadCallsAsync(false));
    }
    private async Task LoadCallDetailAsync(ExecutionCallRow row)
    {
        if (!_detailReads.Add(row.Id)) return;
        try
        {
            var value = await _client.ExecutionGetAsync("/internal/runtime/calls/" + Escape(row.Id), SelectionToken);
			if (!_closed && ReferenceEquals(_detailCall, row))
			{
				row.ApplyDetail(value);
				await Task.WhenAll(LoadPayloadPageAsync(row, "request", false, true), LoadPayloadPageAsync(row, "response", false, true));
			}
        }
        finally { _detailReads.Remove(row.Id); }
    }
    private async Task LoadSourceAsync(ExecutionCallRow row)
    {
        if (row.ConversationId.Length == 0) { row.SetSource("", "unavailable"); SourceDetailsText.Text = "未归属：记录没有可验证的来源对话。调用 ID 可用于导出、隔离、归档与删除。"; return; }
        row.SetSource("", "loading");
        try
        {
            var value = await _client.ExecutionGetAsync("/internal/runtime/conversations/" + Escape(row.ConversationId), SelectionToken);
            row.SetSource(value.Field("conversation").Text("title"), "resolved");
        }
        catch (HttpRequestException ex) when (ex.StatusCode == HttpStatusCode.Gone) { row.SetSource("", "deleted"); }
        catch (HttpRequestException ex) when (ex.StatusCode == HttpStatusCode.NotFound) { row.SetSource("", "unavailable"); }
        catch (OperationCanceledException) { row.SetSource("", "unavailable"); }
        catch (Exception ex) when (ex is HttpRequestException or IOException or JsonException) { row.SetSource("", "error"); }
        if (_detailCall?.Id != row.Id) return;
        var source = row.SourceState == "resolved" && row.ConversationId == _selected?.Id ? "来源已解析" : row.Origin;
        SourceDetailsText.Text = source + (row.Workdir.Length > 0 ? "\n工作目录：" + row.Workdir : "") + (row.Rule.Length > 0 ? "\n权限：" + row.Rule : "") + (row.HasChanges ? "\n\n文件变更\n" + row.Changes : "") + (row.HistoryWarning.Length > 0 ? "\n" + row.HistoryWarning : "");
    }
    private async Task TickAsync()
    {
        if (_closed || !_initialized || _tickRunning) return;
        _tickRunning = true;
        try
        {
            _ticks++;
            if (_ticks % 3 == 0) await GuardAsync(RefreshOverviewAsync);
			if (_detailCall is { } row && CallDetailsTabs.Visibility == Visibility.Visible && (row.CanStop || !row.DetailLoaded || row.RequestPayload.NeedsLoad || row.ResponsePayload.NeedsLoad)) await GuardAsync(() => LoadCallDetailAsync(row));
            if (_ticks % 5 == 0 && _selectedTaskId.Length > 0 && TaskDetailsPanel.Visibility != Visibility.Visible) await GuardAsync(() => LoadTaskAsync(_selectedTaskId, "", false));
			if (System.Diagnostics.Stopwatch.GetElapsedTime(_lastSidebarRefresh) >= TimeSpan.FromSeconds(_streamConnected ? 60 : 3) && ObjectsList.SelectedItems.Count <= 1 && _frozenSelection is null && _openMenus == 0 && _sidebarPaging.Count == 0 && !_sidebarLoading) await GuardAsync(() => LoadObjectsAsync());
            if (_ticks % 3 == 0) await GuardAsync(RefreshInsertionsAsync);
            UpdateStopButton();
        }
        finally { _tickRunning = false; }
    }
    private void UpdateStopButton() => UpdateComposerAvailability();
    private void UpdateEmpty() { EmptyPanel.Visibility = Calls.Count == 0 ? Visibility.Visible : Visibility.Collapsed; EmptyText.Text = "暂无符合条件的执行记录"; }
    private void UpdateFollowButton() { FollowButton.Content = _following ? "跟随" : "继续跟随"; FollowButton.SetResourceReference(Button.BackgroundProperty, _following ? "SelectionBackground" : "PanelBackground"); }
    private void OpenDetails(string title, FrameworkElement pane)
    {
        DetailsPanel.Height = pane == InsertionPanel ? double.NaN : Math.Clamp(ActualHeight * 0.36, 180, 300);
        DetailsPanel.Visibility = Visibility.Visible; DetailsTitle.Text = title;
        DetailsHeader.Visibility = pane == InsertionPanel ? Visibility.Collapsed : Visibility.Visible;
        _bottomPane = pane;
        foreach (var element in new FrameworkElement[] { InsertionPanel, CallDetailsTabs, TaskDetailsPanel, InfoDetailsText, DataManagementPanel }) element.Visibility = element == pane ? Visibility.Visible : Visibility.Collapsed;
    }
    private void CloseDetails()
    {
        HideBottomPane();
        UpdateComposerAvailability();
    }
    private void ShowInfo(string title, string text) { InfoDetailsText.Text = text; OpenDetails(title, InfoDetailsText); }
    private async void Objects_Changed(object sender, SelectionChangedEventArgs e)
    {
        if (_updating || !_initialized) return;
        _frozenSelection = null;
        if (ObjectsList.SelectedItem is ExecutionObject { IsGroupFooter: false } item && item.SelectionKey != _selected?.SelectionKey) await GuardAsync(() => SelectObjectAsync(item));
    }
    private async void Calls_Changed(object sender, SelectionChangedEventArgs e)
    {
		if (_updating || e.Source != CallsList || CallsList.SelectedItem is not ExecutionCallRow row) return;
        _detailCall = row; CallDetailsTabs.DataContext = row; CallDetailsTabs.SelectedIndex = 0;
        OpenDetails(row.Title, CallDetailsTabs); await GuardAsync(() => LoadCallDetailAsync(row));
    }
    private async void DetailTab_Changed(object sender, SelectionChangedEventArgs e)
    {
        if (e.Source != CallDetailsTabs || _detailCall is not { } row) return;
        if ((CallDetailsTabs.SelectedItem as TabItem)?.Tag?.ToString() == "source")
            await GuardAsync(() => LoadSourceAsync(row));
    }
    private async void TaskChoice_Changed(object sender, SelectionChangedEventArgs e) { if (_updating || !_initialized || TaskChoiceCombo.SelectedItem is not ExecutionChoice choice) return; _selectedTaskId = choice.Id; await GuardAsync(() => LoadTaskAsync(choice.Id, "", false)); }
    private async void Branch_Changed(object sender, SelectionChangedEventArgs e) { if (!_updating && _initialized && BranchCombo.SelectedItem is ExecutionChoice branch && _selectedTaskId.Length > 0) await GuardAsync(() => LoadTaskAsync(_selectedTaskId, branch.Id, true)); }
    private async void TaskDetails_Click(object sender, RoutedEventArgs e) { if (_selectedTaskId.Length == 0) return; OpenDetails((TaskChoiceCombo.SelectedItem as ExecutionChoice)?.Title ?? "任务详情", TaskDetailsPanel); await GuardAsync(() => LoadTaskAsync(_selectedTaskId, "", true)); }
    private async void FilterTask_Click(object sender, RoutedEventArgs e) { _taskFilter = _taskFilter == _selectedTaskId ? "" : _selectedTaskId; FilterTaskButton.Content = _taskFilter.Length == 0 ? "筛选此任务" : "显示全部"; await GuardAsync(() => LoadCallsAsync(false)); }
    private void Search_Changed(object sender, TextChangedEventArgs e)
    {
        if (!_initialized) return;
        // Invalidate immediately, even for A -> B -> A inside the debounce.
        _objectEpoch++; _sidebarRequest?.Cancel();
        _filterTimer.Stop(); _filterTimer.Start();
    }
    private void CallSearch_Changed(object sender, TextChangedEventArgs e) { if (!_initialized) return; _callSearchTimer.Stop(); _callSearchTimer.Start(); }
    private async void CallFilter_Changed(object sender, SelectionChangedEventArgs e) { if (_initialized) await GuardAsync(() => LoadCallsAsync(false)); }
    private async void MoreObjects_Click(object sender, RoutedEventArgs e) => await GuardAsync(() => LoadObjectsAsync(true));
    private void Follow_Click(object sender, RoutedEventArgs e) { _following = !_following; UpdateFollowButton(); if (_following && Calls.Count > 0) CallsList.ScrollIntoView(Calls[^1]); }
    private async void LinkTask_Click(object sender, RoutedEventArgs e)
    {
        if (_selected is { IsUnknown: false, IsOrphan: false, Terminated: false } selected)
            await GuardAsync(() => LinkTaskAsync(selected.Id));
    }
    private void ApplyCallPresentation()
    {
        var detailed = _preferences.DetailedCalls;
        CallsList.ItemTemplate = (DataTemplate)Resources[detailed ? "DetailedCallRowTemplate" : "CallRowTemplate"];
        DetailedCallsHeader.Visibility = detailed ? Visibility.Visible : Visibility.Collapsed;
		UpdateCallTableWidth();
		CallPresentationButton.Content = detailed ? "详细" : "简洁";
		CallPresentationButton.ToolTip = detailed ? "切换到简洁视图" : "切换到详细视图";
    }
	private async void CallPresentation_Click(object sender, RoutedEventArgs e)
    {
		if (!_initialized) return;
		var anchor = CaptureCallAnchor();
		_preferences.DetailedCalls = !_preferences.DetailedCalls;
        SavePreferences();
        ApplyCallPresentation();
		await RestoreCallAnchorAsync(anchor);
    }
	private async void Calls_Wheel(object sender, MouseWheelEventArgs e)
	{
		if (e.Delta <= 0) return;
		_following = false; UpdateFollowButton();
		await TryLoadOlderCallsAsync();
	}
	private async void Calls_ScrollChanged(object sender, ScrollChangedEventArgs e)
	{
		if (_restoringCallScroll || _loadingOlderCalls || _updating || e.ExtentHeightChange != 0 || e.VerticalChange >= 0) return;
		_following = false; UpdateFollowButton();
		await TryLoadOlderCallsAsync();
	}
    private async void Refresh_Click(object sender, RoutedEventArgs e) => await GuardAsync(async () => { await LoadWorkspacesAsync(); await LoadObjectsAsync(); await LoadCallsAsync(false); await RefreshOverviewAsync(); });
    private void CloseDetails_Click(object sender, RoutedEventArgs e) => CloseDetails();
    private void DismissWarning_Click(object sender, RoutedEventArgs e)
    {
        var code = _warningCode;
        Warn("");
        if (code.Length > 0) { _preferences.DismissedNotices.Add(code); SavePreferences(); }
    }
    private void Window_SizeChanged(object sender, SizeChangedEventArgs e) { ShowTimestamps = ActualWidth >= 1050; if (DetailsPanel is not null && DetailsPanel.Visibility == Visibility.Visible && _bottomPane != InsertionPanel) DetailsPanel.Height = Math.Clamp(ActualHeight * 0.36, 180, 300); }
    private static T? FindVisualChild<T>(DependencyObject parent) where T : DependencyObject
    { for (var i = 0; i < VisualTreeHelper.GetChildrenCount(parent); i++) { var child = VisualTreeHelper.GetChild(parent, i); if (child is T match) return match; var nested = FindVisualChild<T>(child); if (nested is not null) return nested; } return null; }
    private static T? Ancestor<T>(DependencyObject? item) where T : DependencyObject { while (item is not null) { if (item is T found) return found; item = item is Visual or System.Windows.Media.Media3D.Visual3D ? VisualTreeHelper.GetParent(item) : LogicalTreeHelper.GetParent(item); } return null; }
    private void LoadPreferences()
    {
        try
        {
            var path = Path.Combine(_runtime.RuntimeRoot, "execution-center-settings.json"); if (!File.Exists(path)) return;
            if (new FileInfo(path).Length > 131072) throw new IOException("显示设置超过大小限制，原文件已保留。");
            _preferences = JsonSerializer.Deserialize<ExecutionPreferences>(File.ReadAllText(path), ActivityClient.JsonOptions) ?? new();
            _preferences.FontSize = Math.Clamp(_preferences.FontSize, 12, 20); _preferences.RetentionDays = Math.Clamp(_preferences.RetentionDays, 1, 3650);
            _preferences.CollapsedWorkspaces ??= []; _preferences.SavedFilters ??= [];
            _preferences.DismissedNotices ??= []; _preferences.AdditionalPreferences ??= [];
            if (_preferences.SchemaVersion is < 1 or > 3) throw new JsonException("显示设置版本不受支持，原文件已保留。");
            if (_preferences.Theme is not ("system" or "light" or "dark")) _preferences.Theme = "system";
        }
        catch (Exception ex) when (ex is IOException or JsonException or UnauthorizedAccessException) { _preferencesWritable = false; _preferences = new(); Warn("显示设置未加载，原文件已保留：" + ex.Message); }
    }
    private void SavePreferences()
    {
        if (!_preferencesWritable) return;
        _preferences.Theme = DesktopTheme.Preference;
        var path = Path.Combine(_runtime.RuntimeRoot, "execution-center-settings.json"); var temporary = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
        try { if (File.Exists(path)) {
            if (new FileInfo(path).Length > 131072) throw new IOException("显示设置超过大小限制。");
            var current = JsonSerializer.Deserialize<ExecutionPreferences>(File.ReadAllText(path), ActivityClient.JsonOptions);
            if (current is { SchemaVersion: > 3 }) throw new IOException("显示设置版本已更新，未覆盖。");
            if (current?.DismissedNotices is { } dismissed) _preferences.DismissedNotices.UnionWith(dismissed);
            if (current?.AdditionalPreferences is { } additional) foreach (var pair in additional) _preferences.AdditionalPreferences.TryAdd(pair.Key, pair.Value);
        }
        _preferences.SchemaVersion = 3; _preferences.LastView = _conversationView; _preferences.LastKind = "conversation"; Directory.CreateDirectory(_runtime.RuntimeRoot); File.WriteAllText(temporary, JsonSerializer.Serialize(_preferences, ActivityClient.JsonOptions)); File.Move(temporary, path, true); }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or JsonException) { if (!_closed) Warn("显示设置未保存：" + ex.Message); }
        finally { try { if (File.Exists(temporary)) File.Delete(temporary); } catch (IOException) { } }
    }
    internal void ApplyTheme(string? selection = null)
    {
        if (selection is not null) DesktopTheme.Save(selection);
        _preferences.Theme = DesktopTheme.Preference;
    }
    private void Theme_Changed(object? sender, EventArgs e) { _preferences.Theme = DesktopTheme.Preference; }
    private void Window_Closed(object? sender, EventArgs e)
    {
        if (_closed) return; _closed = true; SavePreferences();
        DesktopTheme.Changed -= Theme_Changed;
        _activityClock.Dispose();
		StopSidebar();
        _filterTimer.Stop(); _callSearchTimer.Stop(); _pulse.Stop(); _lifetime.Cancel(); _selectionCancellation?.Cancel(); _streamCancellation?.Cancel();
        _client.Dispose(); _selectionCancellation?.Dispose(); _streamCancellation?.Dispose(); _lifetime.Dispose();
        // Closing an observer window never stops tasks or command processes.
    }
}
