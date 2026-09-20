using System.Collections.ObjectModel;
using System.IO;
using System.Net.Http;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Media;
using System.Windows.Threading;
using Button = System.Windows.Controls.Button;
using CheckBox = System.Windows.Controls.CheckBox;
using ComboBox = System.Windows.Controls.ComboBox;
using ListBox = System.Windows.Controls.ListBox;
using MenuItem = System.Windows.Controls.MenuItem;
using MessageBox = System.Windows.MessageBox;
using TextBox = System.Windows.Controls.TextBox;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow : Window
{
    private readonly RuntimeService _runtime;
    private readonly ActivityClient _client;
    private readonly CancellationTokenSource _lifetime = new();
    private CancellationTokenSource? _selectionCancellation;
    private Task? _streamTask;
    private readonly DispatcherTimer _filterTimer = new() { Interval = TimeSpan.FromMilliseconds(300) };
    private readonly DispatcherTimer _pulse = new() { Interval = TimeSpan.FromSeconds(1) };
    private readonly Dictionary<string, ExecutionCallRow> _callsById = new(StringComparer.Ordinal);
    private readonly Dictionary<string,string> _conversationTitles=new(StringComparer.Ordinal);
    private readonly HashSet<string> _detailReads = new(StringComparer.Ordinal);
    private ExecutionPreferences _preferences = new();
    private ExecutionObject? _selected;
    private JsonElement _taskSnapshot;
    private string _currentConversationTaskId = "";
    private long _milestoneCursor;
    private readonly List<string> _milestones = [];
    private readonly DispatcherTimer _callSearchTimer = new() { Interval = TimeSpan.FromMilliseconds(350) };
    private string _view = "conversation";
    private string _kind = "conversation";
    private string _branch = "";
    private string[]? _frozenSelection;
    private string[] _menuSelection = [];
    private int _objectOffset;
    private long _before;
    private ulong _cursor;
    private int _generation;
    private int _streamEpoch;
    private bool _initialized;
    private bool _updating;
    private bool _refreshing;
    private bool _tickRunning;
    private bool _closed;
    private int _ticks;
    private int _lastPending;
    public ObservableCollection<ExecutionObject> Objects { get; } = [];
    public ObservableCollection<ExecutionCallRow> Calls { get; } = [];

    public ExecutionWindow(RuntimeService runtime)
    {
        _runtime = runtime; _client = new ActivityClient(runtime);
        InitializeComponent(); DataContext = this;
        _filterTimer.Tick += async (_, _) => { _filterTimer.Stop(); await GuardAsync(() => LoadObjectsAsync(false)); };
        _pulse.Tick += async (_, _) => await TickAsync();
        _callSearchTimer.Tick += async (_, _) => { _callSearchTimer.Stop(); await GuardAsync(() => LoadCallsAsync(false)); };
    }
    private async void Window_Loaded(object sender, RoutedEventArgs e)
    {
        await GuardAsync(async () =>
        {
            LoadPreferences(); FontSize = _preferences.FontSize;
            _view = _preferences.LastView; _kind = _preferences.LastKind;
            _updating = true; KindCombo.SelectedIndex = _kind == "task" ? 1 : 0; _updating = false;
            await LoadWorkspaceChoicesAsync(); _initialized = true;
            await RefreshOverviewAsync(); await LoadObjectsAsync(false); _pulse.Start();
        });
    }
    private async Task GuardAsync(Func<Task> operation)
    {
        if (_closed) return;
        try { await operation(); }
        catch (OperationCanceledException) { }
        catch (Exception ex) when (ex is HttpRequestException or IOException or JsonException or InvalidOperationException or UnauthorizedAccessException or ArgumentException)
        { Warn(ex.Message); ConnectionText.Text = "请求未完成 · 以服务端记录为准"; }
    }
    private void Warn(string message)
    {
        if (_closed) return; WarningText.Text = message; WarningPanel.Visibility = Visibility.Visible;
    }
    private static string Encode(string value) => Uri.EscapeDataString(value);
    private string ListQuery()
    {
        var view = _view is "archived" or "trash" ? _view : "active";
        return $"view={view}&search={Encode(SearchBox.Text.Trim())}&tag={Encode(TagBox.Text.Trim())}&workspace_id={Encode((WorkspaceCombo.SelectedItem as ExecutionChoice)?.Id ?? "")}";
    }
    private string CallQuery() => CallScopeQuery() + "&search=" + Encode(CallSearchBox.Text.Trim());
    private string CallScopeQuery()
    {
        if (_view == "attention") return "status=attention&top_level=true";
        if (_selected is null) return "top_level=true";
        if (_selected.IsUnknown) return "unattributed=true&top_level=true";
        if (_selected.Kind == "task") return $"task_id={Encode(_selected.Id)}&thread_id={Encode(_branch)}&top_level=true";
        return $"conversation_id={Encode(_selected.Id)}&top_level=true";
    }
    private async Task LoadWorkspaceChoicesAsync()
    {
        var current = (WorkspaceCombo.SelectedItem as ExecutionChoice)?.Id ?? "";
        var response = await _client.ExecutionGetAsync("/internal/runtime/permissions/effective", _lifetime.Token);
        var choices = new List<ExecutionChoice> { new("", "所有工作区") };
        choices.AddRange(response.Array("workspaces").Select(item => new ExecutionChoice(item.Text("workspace_id"), item.Text("name") + " · " + item.Text("root"))));
        _updating = true; WorkspaceCombo.ItemsSource = choices; WorkspaceCombo.SelectedItem = choices.FirstOrDefault(item => item.Id == current) ?? choices[0]; _updating = false;
    }
    private async Task RefreshOverviewAsync()
    {
        var overview = await _client.ExecutionGetAsync("/internal/runtime/execution", _lifetime.Token);
        if (_closed) return;
        var stats = overview.Field("statistics"); var pending = (int)stats.Number("pending");
        OverviewText.Text = $"{stats.Number("running")} 运行中 · {pending} 待审批 · {stats.Number("unknown")} 结果未知 · 默认权限：{ExecutionJson.Mode(overview.Text("permission_mode"))}";
        AttentionButton.Content = $"!  待处理 · {pending + stats.Number("failed") + stats.Number("unknown")}";
        if (_preferences.Notifications && pending > _lastPending && _lastPending >= 0) ConnectionText.Text = "新增待审批请求 · 实际操作尚未执行";
        _lastPending = pending;
    }
    private async Task LoadObjectsAsync(bool append)
    {
        if (!_initialized || _refreshing) return;
        if (_view == "attention")
        {
            _updating = true; Objects.Clear(); _selected = null; _frozenSelection = null; _updating = false;
            ObjectTitle.Text = "待处理执行"; ObjectSubtitle.Text = "集中查看待审批、失败和结果未知的调用。";
            TaskProgressPanel.Visibility = ConversationProgressCard.Visibility = SetCurrentTaskButton.Visibility = LinkTaskButton.Visibility = ContinueButton.Visibility = CancelTaskButton.Visibility = Visibility.Collapsed;
            SelectionText.Text = "按实际调用状态汇总，不创建任务或对话。"; MoreObjectsButton.Visibility = Visibility.Collapsed;
            await LoadCallsAsync(false); return;
        }
        _refreshing = true; MoreObjectsButton.IsEnabled = false;
        try
        {
            var kind = _kind; var view = _view; var query = ListQuery(); var selectedId = _selected?.Id; var selectedUnknown = _selected?.IsUnknown ?? false;
            var selectedIds = ObjectsList.SelectedItems.Cast<ExecutionObject>().Select(item => item.Id).ToHashSet();
            var offset = append ? _objectOffset : 0;
            var path = kind == "task" ? "/internal/runtime/execution/tasks" : "/internal/runtime/conversations";
            var response = await _client.ExecutionGetAsync(path + "?" + query + "&offset=" + offset + "&limit=100", _lifetime.Token);
            if (kind != _kind || view != _view || query != ListQuery() || _closed) return;
            var key = kind == "task" ? "tasks" : "conversations";
            _updating = true;
            if (!append) Objects.Clear();
            foreach (var item in response.Array(key))
            {
                var model = ExecutionObject.From(item, kind);
                if (!Objects.Any(existing => existing.Id == model.Id && existing.IsUnknown == model.IsUnknown)) Objects.Add(model);
            }
            if(kind=="conversation")foreach(var obj in Objects)if(obj.Id!="")_conversationTitles[obj.Id]=obj.Title;
            _objectOffset = (int)response.Number("next_offset"); MoreObjectsButton.Visibility = response.Flag("has_more") ? Visibility.Visible : Visibility.Collapsed;
            if (!append)
            {
                foreach (var item in Objects.Where(item => selectedIds.Contains(item.Id))) ObjectsList.SelectedItems.Add(item);
                var target = Objects.FirstOrDefault(item => item.Id == selectedId && item.IsUnknown == selectedUnknown) ?? Objects.FirstOrDefault();
                if (target is not null && ObjectsList.SelectedItems.Count == 0) ObjectsList.SelectedItem = target;
                _selected = target;
            }
            _updating = false; UpdateSelectionText(response.Number("total"));
            if (response.Array("warnings").Count > 0) Warn(string.Join("\n", response.Array("warnings").Select(item => item.GetString())));
            if (!append && (_selected is null || _selected.Id != selectedId || _selected.IsUnknown != selectedUnknown || Calls.Count == 0)) await SelectObjectAsync(_selected);
            else if (!append && _selected?.Kind == "task") await LoadTaskAsync(_selected, _generation);
            else if (!append && _selected is not null) ObjectTitle.Text=_selected.Title;
        }
        finally { _updating = false; _refreshing = false; MoreObjectsButton.IsEnabled = true; }
    }
    private void UpdateSelectionText(long? total = null)
    {
        var count = _frozenSelection?.Length ?? ObjectsList.SelectedItems.Count;
        SelectionText.Text = $"已选择 {count} 项" + (total is null ? "" : $" · 筛选共 {total} 项");
    }
    private async void Objects_SelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        if (_updating || !_initialized) return;
        _frozenSelection = null; UpdateSelectionText();
        if (ObjectsList.SelectedItem is ExecutionObject item && (item.Id != _selected?.Id || item.Kind != _selected.Kind || item.IsUnknown != _selected.IsUnknown)) await GuardAsync(() => SelectObjectAsync(item));
    }
    private async Task SelectObjectAsync(ExecutionObject? item)
    {
        _selected = item; _branch = ""; _generation++;
        _selectionCancellation?.Cancel(); _selectionCancellation?.Dispose(); _selectionCancellation = null;
        Calls.Clear(); _callsById.Clear(); _detailReads.Clear(); _taskSnapshot = default;
        _currentConversationTaskId = ""; _milestoneCursor = 0; _milestones.Clear(); MilestonesText.Text = "暂无任务进度里程碑。";
        TaskProgressPanel.Visibility = ConversationProgressCard.Visibility = SetCurrentTaskButton.Visibility = LinkTaskButton.Visibility = ContinueButton.Visibility = CancelTaskButton.Visibility = Visibility.Collapsed;
        if (item is null) { ObjectTitle.Text = "没有符合条件的对象"; ObjectSubtitle.Text = "调整筛选条件或在已连接的对话中执行工具。"; EmptyText.Text = "暂无执行记录。"; return; }
        ObjectTitle.Text = item.Title; ObjectSubtitle.Text = item.Detail + (item.Tags == "" ? "" : " · " + item.Tags);
        if (item.Kind == "task") { TaskProgressPanel.Visibility = ContinueButton.Visibility = CancelTaskButton.Visibility = Visibility.Visible; await LoadTaskAsync(item, _generation); }
        else if (!item.IsUnknown)
        {
            LinkTaskButton.Visibility = SetCurrentTaskButton.Visibility = item.Trashed ? Visibility.Collapsed : Visibility.Visible;
            var generation = _generation;
            var detail = await _client.ExecutionGetAsync("/internal/runtime/conversations/" + Encode(item.Id), _lifetime.Token);
            if (generation != _generation) return;
            var conversation = detail.Field("conversation");
            await ShowConversationTaskAsync(conversation, generation);
            ObjectSubtitle.Text = $"来源：{conversation.Text("source")} · {ExecutionJson.Mode(detail.Field("permission").Text("mode"))} · {conversation.Array("task_ids").Count} 个关联任务";
        }
        await LoadCallsAsync(false);
        await LoadMilestonesAsync();
    }
    private async Task LoadTaskAsync(ExecutionObject item, int generation)
    {
        var detail = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Encode(item.Id), _lifetime.Token);
        if (generation != _generation || _selected?.Id != item.Id || _closed) return;
        var task = detail.Field("task"); _taskSnapshot = task.Clone();
        ObjectTitle.Text = task.Text("title"); ObjectSubtitle.Text = ExecutionJson.State(task.Text("status")) + " · " + (task.Text("blocker") is { Length: > 0 } blocker ? blocker : task.Text("summary"));
        TaskGoalText.Text = task.Text("goal");
        var branches = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Encode(item.Id) + "/threads", _lifetime.Token);
        if (generation != _generation) return;
        var choices = new List<ExecutionChoice> { new("", "所有任务分支 · 仅切换查看") };
        choices.AddRange(branches.Array("threads").Select(branch => new ExecutionChoice(branch.Text("id"), branch.Text("title") + " · " + ExecutionJson.State(branch.Text("status")))));
        _updating = true; BranchCombo.ItemsSource = choices; BranchCombo.SelectedItem = choices.FirstOrDefault(choice => choice.Id == _branch) ?? choices[0]; _updating = false;
        if (_branch=="") ShowTaskSteps(task);
        else { var branch=await _client.ExecutionGetAsync("/internal/runtime/tasks/"+Encode(item.Id)+"/threads/"+Encode(_branch),_lifetime.Token);if(generation!=_generation)return;ShowTaskSteps(branch.Field("thread")); }
        TaskAcceptanceText.Text = string.Join(Environment.NewLine, task.Array("conditions").Select(condition => "· " + condition.Text("text") + "  " + condition.Text("status")));
        if (TaskAcceptanceText.Text.Length==0) TaskAcceptanceText.Text="任务尚未声明验收条件。";
        if (task.Field("final_review").ValueKind == JsonValueKind.Object) TaskAcceptanceText.Text += "\n\n最终复核：" + task.Field("final_review").Text("status") + "\n" + task.Field("final_review").Text("summary");
    }
    private async Task ShowConversationTaskAsync(JsonElement conversation, int generation)
    {
        if (generation != _generation || _selected?.Kind != "conversation" || _closed) return;
        var state = conversation.Field("state"); var id = state.Text("active_task_id");
        if (id != _currentConversationTaskId) { _milestoneCursor = 0; _milestones.Clear(); MilestonesText.Text = "暂无任务进度里程碑。"; }
        _currentConversationTaskId = id;
        if (id.Length == 0) { ConversationProgressCard.Visibility = Visibility.Collapsed; return; }
        var detail = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Encode(id), _lifetime.Token);
        if (generation != _generation || _selected?.Kind != "conversation") return;
        var task = detail.Field("task");
        var threadId = state.Text("active_task_thread_id");
        var thread = task.Field("active_thread");
        if (threadId.Length != 0)
        {
            var branch = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Encode(id) + "/threads/" + Encode(threadId), _lifetime.Token);
            if (generation != _generation) return; thread = branch.Field("thread");
        }
        var steps = thread.Array("steps"); if (steps.Count == 0) steps = task.Array("steps");
        var complete = steps.Count(step => step.Text("status") == "completed");
        var currentId = thread.Text("current_step_id");
        var current = steps.FirstOrDefault(step => step.Text("id") == currentId).Text("title");
        CurrentTaskTitle.Text = task.Text("title");
        CurrentTaskStatus.Text = $"{ExecutionJson.State(task.Text("status"))} · {complete}/{steps.Count} 步骤 · 分支 {threadId}";
        CurrentTaskProgress.Value = steps.Count == 0 ? 0 : complete * 100d / steps.Count;
        CurrentTaskNext.Text = "当前步骤：" + (current.Length == 0 ? "未指定" : current) + "\n下一动作：" + (thread.Text("next_action") is { Length: > 0 } next ? next : "尚未记录");
        ConversationProgressCard.Visibility = Visibility.Visible;
    }
    private async Task LoadMilestonesAsync()
    {
        var id = _selected?.Kind == "task" ? _selected.Id : _currentConversationTaskId;
        if (string.IsNullOrEmpty(id)) { MilestonesPanel.Visibility = Visibility.Collapsed; return; }
        var generation = _generation;
        var page = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Encode(id) + "/activity?milestones=true&limit=100&after=" + _milestoneCursor, _selectionCancellation?.Token ?? _lifetime.Token);
        if (generation != _generation) return;
        foreach (var entry in page.Array("events"))
        {
            var time = DateTimeOffset.TryParse(entry.Text("created_at"), out var at) ? at.LocalDateTime.ToString("HH:mm:ss") : "";
            _milestones.Add(time + "  " + ExecutionJson.State(entry.Text("status")) + "  " + entry.Text("summary") + "  [" + entry.Text("kind") + "]");
        }
        _milestoneCursor = page.Number("next_seq");
        if (_milestones.Count > 100) _milestones.RemoveRange(0, _milestones.Count - 100);
        MilestonesText.Text = _milestones.Count == 0 ? "暂无任务进度里程碑。" : string.Join(Environment.NewLine, _milestones);
        MilestonesPanel.Visibility = Visibility.Visible;
        if (page.Flag("gap")) Warn("任务里程碑历史存在保留缺口。");
    }
    private void CallSearch_Changed(object sender, TextChangedEventArgs e)
    { if (!_initialized || _updating) return; _callSearchTimer.Stop(); _callSearchTimer.Start(); }

    private void ShowTaskSteps(JsonElement task)
    {
        var steps = task.Array("steps"); var complete = steps.Count(step => step.Text("status") == "completed");
        TaskProgressBar.Value = steps.Count == 0 ? 0 : complete * 100d / steps.Count;
        TaskStepsText.Text = steps.Count == 0 ? "尚未设置任务步骤。调用数量不会自动改变任务进度。" : string.Join(Environment.NewLine, steps.Select(step => $"{(step.Text("status") == "completed" ? "✓" : "·")} {step.Text("title")}   {ExecutionJson.State(step.Text("status"))}"));
    }
    private async Task LoadCallsAsync(bool older)
    {
        if (_selected is null && _view != "attention") return;
        var generation = _generation; var query = CallQuery();
        if (!older)
        {
            _streamEpoch++;
            _selectionCancellation?.Cancel(); _selectionCancellation?.Dispose();
            _selectionCancellation = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
        }
        var token = _selectionCancellation?.Token ?? _lifetime.Token;
        var response = await _client.ExecutionGetAsync("/internal/runtime/calls?" + query + "&limit=100" + (older && _before > 0 ? "&before=" + _before : ""), token);
        if (generation != _generation || query != CallQuery()) return;
        if (!older) { Calls.Clear(); _callsById.Clear(); }
        foreach (var value in response.Array("calls").Reverse()) UpsertCall(value);
        _before = response.Number("next_before");
        if (response.Flag("gap")) Warn("保留的执行历史存在缺口或已发生轮换。当前列表不代表完整历史；技术详情保留可确认的记录。");
        EmptyText.Text = Calls.Count == 0 ? "暂无符合条件的调用。" : response.Flag("has_more") ? "向前加载可查看更早记录。" : "已显示当前筛选条件下的保留记录。";
        if (!older)
        {
            _cursor = (ulong)Math.Max(0, response.Number("latest_seq"));
            var epoch=_streamEpoch;
            _streamTask = _client.ObserveExecutionsAsync(_view == "attention" ? "top_level=true" : query, _cursor, message => ReceiveStreamAsync(message, generation,epoch), token);
            if (FollowLatestBox.IsChecked == true && Calls.Count > 0) CallsList.ScrollIntoView(Calls[^1]);
        }
    }
    private void UpsertCall(JsonElement value)
    {
        var id = value.Text("call_id"); if (id == "" || value.Text("parent_call_id") != "") return;
        if (_callsById.TryGetValue(id, out var row)) { row.Apply(value); return; }
        row = new ExecutionCallRow(value); _callsById[id] = row;
        var index = 0; while (index < Calls.Count && Calls[index].CreatedSeq < row.CreatedSeq) index++;
        Calls.Insert(index, row);
        if (Calls.Count > 1000 && FollowLatestBox.IsChecked == true) { _callsById.Remove(Calls[0].Id); Calls.RemoveAt(0); }
    }
    private async Task ReceiveStreamAsync(ExecutionStreamMessage message, int generation,int epoch)
    {
        if (_closed || _lifetime.IsCancellationRequested) return;
        await Dispatcher.InvokeAsync(() =>
        {
            if (_closed || generation != _generation || epoch!=_streamEpoch) return;
            _cursor = message.Seq;
            switch (message.Kind)
            {
                case "call":
                    if (_view!="attention" || message.Value.Text("status") is "pending_approval" or "failed" or "unknown") UpsertCall(message.Value);
                    if (_view == "attention" && message.Value.Text("status") is not ("pending_approval" or "failed" or "unknown") && _callsById.Remove(message.Value.Text("call_id"), out var finished)) Calls.Remove(finished);
                    if (FollowLatestBox.IsChecked == true && Calls.Count > 0) CallsList.ScrollIntoView(Calls[^1]);
                    EmptyText.Text = Calls.Count == 0 ? "暂无待处理执行。" : "按同一 call_id 实时更新，不重复插入开始、输出和结束事件。";
                    break;
                case "connected": ConnectionText.Text = "实时连接 · 按调用序号恢复"; break;
                case "disconnected": ConnectionText.Text = "连接已断开 · 正在重连，不会重新执行命令"; break;
                case "gap": Warn("执行历史存在缺口或已轮换。部分早期事件不在保留范围，不能视为完整记录。"); break;
                case "reset": Warn("服务的日志游标已变化，请刷新核对当前执行状态。不会自动重试任何操作。"); break;
                case "warning": Warn(message.Value.Text("reason")); break;
            }
        });
    }
    private async Task LoadSourceTitlesAsync()
    {
        if(_selected?.Kind!="task")return;
        var generation=_generation;
        foreach(var id in Calls.Where(row=>row.ConversationId!="").Select(row=>row.ConversationId).Distinct().Where(id=>!_conversationTitles.ContainsKey(id)).Take(4).ToArray())
        {
            try{var detail=await _client.ExecutionGetAsync("/internal/runtime/conversations/"+Encode(id),_selectionCancellation?.Token??_lifetime.Token);_conversationTitles[id]=detail.Field("conversation").Text("title");}
            catch(HttpRequestException){_conversationTitles[id]="历史对话（管理对象已清理）";}
            if(generation!=_generation)return;
        }
        foreach(var row in Calls)if(_conversationTitles.TryGetValue(row.ConversationId,out var title))row.SourceTitle=title;
    }
    private async Task TickAsync()
    {
        if (_tickRunning || _closed) return; _tickRunning = true;
        try
        {
            await GuardAsync(async () =>
            {
                await RefreshOverviewAsync();
                await LoadSourceTitlesAsync();
                foreach (var row in Calls.Where(item => item.IsExpanded && (item.CanStop || !item.DetailLoaded)).Take(8).ToArray()) await LoadCallDetailAsync(row);
                _ticks++;
                if (_ticks % 5 == 0)
                {
                    if (_selected?.Kind == "task") await LoadTaskAsync(_selected, _generation);
                    else if (_selected is { Kind: "conversation", IsUnknown: false } selected)
                    {
                        var generation = _generation;
                        var detail = await _client.ExecutionGetAsync("/internal/runtime/conversations/" + Encode(selected.Id), _lifetime.Token);
                        if (generation == _generation) await ShowConversationTaskAsync(detail.Field("conversation"), generation);
                    }
                    await LoadMilestonesAsync();
                }
                // Refresh sidebar counts without moving a viewed object or a frozen batch selection.
                if (_ticks % 8 == 0 && _frozenSelection is null && ObjectsList.SelectedItems.Count <= 1 && _view != "attention") await LoadObjectsAsync(false);
            });
        }
        finally { _tickRunning = false; }
    }
    private async Task LoadCallDetailAsync(ExecutionCallRow row)
    {
        if (!_detailReads.Add(row.Id)) return;
        var generation = _generation;
        try
        {
            var detail = await _client.ExecutionGetAsync("/internal/runtime/calls/" + Encode(row.Id), _selectionCancellation?.Token ?? _lifetime.Token);
            if (generation == _generation) row.ApplyDetail(detail);
        }
        finally { _detailReads.Remove(row.Id); }
    }
    private async void Call_Expanded(object sender, RoutedEventArgs e)
    { if (ReferenceEquals(sender, e.OriginalSource) && sender is FrameworkElement { DataContext: ExecutionCallRow row }) await GuardAsync(() => LoadCallDetailAsync(row)); }
    private async void RefreshCall_Click(object sender, RoutedEventArgs e)
    { if (sender is FrameworkElement { DataContext: ExecutionCallRow row }) await GuardAsync(() => LoadCallDetailAsync(row)); }
    private async void Children_Expanded(object sender, RoutedEventArgs e)
    {
        if (!ReferenceEquals(sender, e.OriginalSource) || sender is not FrameworkElement { DataContext: ExecutionCallRow row }) return;
        await GuardAsync(async () =>
        {
            var detail = await _client.ExecutionGetAsync("/internal/runtime/calls?parent_call_id=" + Encode(row.Id) + "&include_output=true&limit=100", _selectionCancellation?.Token ?? _lifetime.Token);
            row.Children.Clear(); foreach (var item in detail.Array("calls").Reverse()) { var child = new ExecutionCallRow(item); child.ApplyDetail(item); row.Children.Add(child); }
        });
    }
    private async void Navigate_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement { Tag: string view }) return;
        _view = view; if (view is "task" or "conversation") _kind = view;
        _updating = true; KindCombo.SelectedIndex = _kind == "task" ? 1 : 0; _updating = false;
        _frozenSelection = null; _selected = null; _generation++; _selectionCancellation?.Cancel();
        await GuardAsync(() => LoadObjectsAsync(false));
    }
    private void Filter_Changed(object sender, SelectionChangedEventArgs e)
    {
        if (!_initialized || _updating) return;
        if (ReferenceEquals(sender, KindCombo)) { _kind = KindCombo.SelectedIndex == 1 ? "task" : "conversation"; if (_view is "conversation" or "task") _view = _kind; }
        QueueFilter();
    }
    private void FilterText_Changed(object sender, TextChangedEventArgs e) { if (_initialized && !_updating) QueueFilter(); }
    private void QueueFilter() { _frozenSelection = null; _filterTimer.Stop(); _filterTimer.Start(); }
    private async void MoreObjects_Click(object sender, RoutedEventArgs e) => await GuardAsync(() => LoadObjectsAsync(true));
    private async void Refresh_Click(object sender, RoutedEventArgs e) => await GuardAsync(async () => { WarningPanel.Visibility = Visibility.Collapsed; await LoadWorkspaceChoicesAsync(); await RefreshOverviewAsync(); await LoadObjectsAsync(false); await LoadCallsAsync(false); });
    private async void OlderCalls_Click(object sender, RoutedEventArgs e) { FollowLatestBox.IsChecked = false; await GuardAsync(() => LoadCallsAsync(true)); }
    private async void Latest_Click(object sender, RoutedEventArgs e) { FollowLatestBox.IsChecked = true; await GuardAsync(() => LoadCallsAsync(false)); }
    private async void Branch_Changed(object sender, SelectionChangedEventArgs e)
    {
        if (_updating || _selected?.Kind != "task" || BranchCombo.SelectedItem is not ExecutionChoice choice) return;
        _branch = choice.Id;
        await GuardAsync(async () =>
        {
            if (_branch == "") ShowTaskSteps(_taskSnapshot);
            else { var result = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Encode(_selected.Id) + "/threads/" + Encode(_branch), _lifetime.Token); ShowTaskSteps(result.Field("thread")); }
            await LoadCallsAsync(false);
        });
    }
    private async void ContinueBranch_Click(object sender, RoutedEventArgs e)
    {
        if (_selected?.Kind != "task" || _branch == "") { Warn("选择一个具体任务分支后，才能设为继续分支。查看所有分支不改变默认恢复点。"); return; }
        await GuardAsync(async () => { await _client.ControlAsync(new { action = "thread_switch", task_id = _selected.Id, thread_id = _branch }, _lifetime.Token); await LoadTaskAsync(_selected, _generation); });
    }
    private void Calls_ScrollChanged(object sender, ScrollChangedEventArgs e)
    {
        if (e.OriginalSource is not ScrollViewer scroll || !ReferenceEquals(FindAncestor<ListBox>(scroll), CallsList)) return;
        if (e.VerticalChange < 0 && scroll.ScrollableHeight - scroll.VerticalOffset > 30) FollowLatestBox.IsChecked = false;
    }
    private void Output_ScrollChanged(object sender, ScrollChangedEventArgs e)
    {
        if (sender is TextBox { DataContext: ExecutionCallRow row } box && e.VerticalChange != 0) row.FollowOutput = box.ExtentHeight - box.ViewportHeight - box.VerticalOffset < 12;
    }
    private void Output_TextChanged(object sender, TextChangedEventArgs e)
    { if (sender is TextBox { DataContext: ExecutionCallRow { FollowOutput: true } } box) box.ScrollToEnd(); }
    private static T? FindAncestor<T>(DependencyObject? current) where T : DependencyObject
    { while (current is not null) { if (current is T match) return match; current = VisualTreeHelper.GetParent(current); } return null; }
    private void Window_KeyDown(object sender, System.Windows.Input.KeyEventArgs e)
    {
        if (e.Key == Key.F5) { Refresh_Click(sender, e); e.Handled = true; }
        else if (e.Key == Key.Escape && _frozenSelection is not null) { ClearSelection_Click(sender, e); e.Handled = true; }
        else if (Keyboard.Modifiers == ModifierKeys.Control && e.Key == Key.F) { SearchBox.Focus(); SearchBox.SelectAll(); e.Handled = true; }
    }
    private void Window_Closed(object? sender, EventArgs e)
    {
        _closed = true; _pulse.Stop(); _filterTimer.Stop(); _callSearchTimer.Stop(); _selectionCancellation?.Cancel(); _lifetime.Cancel();
        SavePreferences(); _client.Dispose(); _selectionCancellation?.Dispose(); _lifetime.Dispose();
        // No stop request is sent: closing a view must not affect actual execution.
    }
    private string PreferencesPath => Path.Combine(_runtime.RuntimeRoot, "execution-center-settings.json");
    private void LoadPreferences()
    {
        try
        {
            if (File.Exists(PreferencesPath) && new FileInfo(PreferencesPath).Length <= 65536)
            {
                var stored = JsonSerializer.Deserialize<ExecutionPreferences>(File.ReadAllText(PreferencesPath));
                if (stored?.SchemaVersion == 1) _preferences = stored;
            }
        }
        catch (Exception ex) when (ex is IOException or JsonException or UnauthorizedAccessException) { Warn("界面偏好无法读取，已使用默认布局。" + ex.Message); }
        _preferences.FontSize = Math.Clamp(_preferences.FontSize, 12, 20); _preferences.RetentionDays = Math.Clamp(_preferences.RetentionDays, 1, 3650);
        if (_preferences.LastView is not ("task" or "conversation" or "archived" or "trash" or "attention")) _preferences.LastView = "conversation";
        if (_preferences.LastKind is not ("task" or "conversation")) _preferences.LastKind = "conversation";
    }
    private void SavePreferences()
    {
        try
        {
            _preferences.LastView = _view; _preferences.LastKind = _kind; _preferences.FontSize = FontSize;
            Directory.CreateDirectory(_runtime.RuntimeRoot); var temp = PreferencesPath + "." + Guid.NewGuid().ToString("N") + ".tmp";
            File.WriteAllText(temp, JsonSerializer.Serialize(_preferences)); File.Move(temp, PreferencesPath, true);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException) { System.Diagnostics.Debug.WriteLine(ex.Message); }
    }
}
