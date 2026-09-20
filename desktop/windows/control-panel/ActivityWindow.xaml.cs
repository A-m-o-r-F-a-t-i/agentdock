using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Threading.Channels;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Data;
using System.Windows.Threading;
using MessageBox = System.Windows.MessageBox;

namespace AgentDock.ControlPanel;

public partial class ActivityWindow : Window
{
    private sealed record StateChoice(string Value, string Label);
    private readonly ActivityClient _client;
    private readonly CancellationTokenSource _lifetime = new();
    private readonly ObservableCollection<ActivityTask> _tasks = [];
    private readonly ActivityTimeline _timeline = new();
    private readonly DispatcherTimer _pulse = new(DispatcherPriority.Background) { Interval = TimeSpan.FromMilliseconds(100) };
    private CancellationTokenSource? _detailCancellation;
    private CancellationTokenSource? _streamCancellation;
    private Channel<ActivityStreamMessage>? _channel;
    private ActivityTask? _task;
    private string? _selectedTaskId;
    private string _selectedThreadId = "";
    private string _preferredThreadId = "";
    private int _selectionGeneration;
    private bool _ready, _suppressSelection, _refreshing, _controlBusy, _needsRefresh;
    private DateTimeOffset _lastRefresh;

    public ActivityWindow(RuntimeService runtime)
    {
        InitializeComponent();
        _client = new ActivityClient(runtime);
        TaskList.ItemsSource = _tasks;
        CollectionViewSource.GetDefaultView(_tasks).Filter = item => item is ActivityTask task &&
            (TaskSearch.Text.Length == 0 || task.SearchText.Contains(TaskSearch.Text.Trim(), StringComparison.CurrentCultureIgnoreCase));
        CollectionViewSource.GetDefaultView(_tasks).GroupDescriptions.Add(new PropertyGroupDescription(nameof(ActivityTask.GroupLabel)));
        TimelineList.ItemsSource = _timeline.Rows;
        CollectionViewSource.GetDefaultView(_timeline.Rows).Filter = item => item is ActivityRow row &&
            (CategoryFilter.SelectedItem is not StateChoice choice || choice.Value.Length == 0 || row.Category == choice.Value);
        CategoryFilter.ItemsSource = new[] {
            new StateChoice("", ActivityText.Get("CategoryAll")), new StateChoice("command", ActivityText.Get("CategoryCommand")),
            new StateChoice("file", ActivityText.Get("CategoryFile")), new StateChoice("checkpoint", ActivityText.Get("CategoryCheckpoint")),
            new StateChoice("lifecycle", ActivityText.Get("CategoryLifecycle")) };
        CategoryFilter.SelectedIndex = 0;
        TimelineList.AddHandler(ScrollViewer.ScrollChangedEvent, new ScrollChangedEventHandler(Timeline_ScrollChanged));
        StateFilter.ItemsSource = new[] { new StateChoice("", ActivityText.Get("All")), new StateChoice("active", ActivityText.State("active")), new StateChoice("blocked", ActivityText.State("blocked")), new StateChoice("completed", ActivityText.Get("EndedTasks")) };
        StateFilter.SelectedIndex = 0;
        _pulse.Tick += Pulse;
    }

    private async void Window_Loaded(object sender, RoutedEventArgs e)
    {
        if (_ready) return;
        _ready = true;
        _pulse.Start();
        await RefreshTasksAsync();
    }

    private async Task RefreshTasksAsync()
    {
        if (!_ready || _refreshing) return;
        _refreshing = true;
        _lastRefresh = DateTimeOffset.UtcNow;
        try
        {
            var status = (StateFilter.SelectedItem as StateChoice)?.Value ?? "";
            var result = await _client.TasksAsync(status, ShowArchived.IsChecked == true, _lifetime.Token);
            if (!_ready) return;
            _suppressSelection = true;
            try
            {
                _tasks.Clear();
                foreach (var item in result.Tasks.OrderBy(item => item.GroupRank).ThenByDescending(item => item.UpdatedAt)) _tasks.Add(item);
                _tasks.Add(new ActivityTask { Id = "", Title = ActivityText.Get("AllActivity"), Status = "", UpdatedAt = DateTimeOffset.Now });
                var selected = _tasks.FirstOrDefault(task => task.Id == _selectedTaskId) ??
                    _tasks.FirstOrDefault(task => task.Status == "active") ?? _tasks[0];
                TaskList.SelectedItem = selected;
                TaskCountNotice.Text = result.Count >= 200 ? ActivityText.Get("MoreTasks") : "";
            }
            finally { _suppressSelection = false; }
            var selectedId = (TaskList.SelectedItem as ActivityTask)?.Id ?? "";
            if (_selectedTaskId != selectedId || _channel is null) await SelectTaskAsync(selectedId);
            else if (selectedId.Length > 0) await LoadTaskDetailsAsync(selectedId, _selectionGeneration, _detailCancellation?.Token ?? _lifetime.Token);
            _needsRefresh = false;
            _lastSuccessfulRefresh = DateTimeOffset.Now;
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested || !_ready) { }
        catch (Exception ex) when (ex is IOException or System.Net.Http.HttpRequestException or JsonException or OperationCanceledException)
        {
            if (_ready) ConnectionStatus.Text = ActivityText.Get("Unavailable") + LastSuccessfulCheck() + Environment.NewLine + ex.Message;
        }
        finally { _refreshing = false; }
    }

    private async Task SelectTaskAsync(string id)
    {
        if (!_ready) return;
        _detailCancellation?.Cancel(); _detailCancellation?.Dispose();
        _detailCancellation = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
        var token = _detailCancellation.Token;
        var generation = ++_selectionGeneration;
        StopStream();
        _selectedTaskId = id; _selectedThreadId = ""; _task = null;
        _timeline.Reset(); JournalWarning.Text = "";
        ClearLiveState();
        EmptyTimeline.Visibility = Visibility.Visible;
        ConnectionStatus.Text = ActivityText.Get("Loading");
        TaskTitle.Text = _tasks.FirstOrDefault(task => task.Id == id)?.Title ?? id;
        try
        {
            if (id.Length == 0)
            {
                _suppressSelection = true;
                ThreadSelector.ItemsSource = null;
                ThreadSelector.IsEnabled = false;
                _suppressSelection = false;
                ThreadSelector.Visibility = ThreadLabel.Visibility = Visibility.Collapsed;
                ThreadInfo.Text = TaskStateText.Text = TaskTechnicalInfo.Text = "";
                AcceptanceInfo.Text = ActivityText.Get("Facts");
                WorkspaceInfo.Text = ActivityText.Get("NoWorkspace");
                _ = RefreshLiveAsync(true);
                StartStream("", ""); UpdateActions();
                return;
            }
            await LoadTaskDetailsAsync(id, generation, token);
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested) { }
        catch (Exception ex) when (ex is IOException or System.Net.Http.HttpRequestException or JsonException or OperationCanceledException)
        {
            if (_ready && generation == _selectionGeneration) ConnectionStatus.Text = ex.Message;
        }
    }

    private async Task LoadTaskDetailsAsync(string id, int generation, CancellationToken token)
    {
        var detailRequest = _client.TaskAsync(id, token);
        var threadRequest = _client.ThreadsAsync(id, token);
        await Task.WhenAll(detailRequest, threadRequest);
        if (!_ready || generation != _selectionGeneration || id != _selectedTaskId) return;
        _task = detailRequest.Result.Task;
        var threads = threadRequest.Result.Threads;
        var desired = _preferredThreadId.Length > 0 ? _preferredThreadId : _selectedThreadId.Length > 0 ? _selectedThreadId : _task.ActiveThreadId;
        var selected = threads.FirstOrDefault(thread => thread.EffectiveId == desired) ?? threads.FirstOrDefault(thread => thread.EffectiveId == _task.ActiveThreadId) ?? threads.FirstOrDefault();
        _preferredThreadId = "";
        _suppressSelection = true;
        try { ThreadSelector.ItemsSource = threads; ThreadSelector.SelectedItem = selected; ThreadSelector.IsEnabled = threads.Count > 0; }
        finally { _suppressSelection = false; }
        ThreadSelector.Visibility = ThreadLabel.Visibility = threads.Count > 1 ? Visibility.Visible : Visibility.Collapsed;
        TaskTitle.Text = _task.Title;
        ShowThreadDetails(selected);
        if (selected is not null && (_selectedThreadId != selected.EffectiveId || _channel is null))
        {
            _selectedThreadId = selected.EffectiveId;
            StartStream(id, selected.EffectiveId);
        }
        UpdateActions();
        await RefreshLiveAsync(true);
    }

    private void ShowThreadDetails(ActivityThread? thread)
    {
        if (_task is null) return;
        var steps = ActivityPresentation.Steps(_task, thread);
        TaskStateText.Text = _task.StateLabel + " · " + ActivityText.Get("Progress") + $": {steps.Count(item => item.Status == "completed")}/{steps.Count}";
        ThreadInfo.Text = ActivityPresentation.ThreadSummary(_task, thread);
        TaskTechnicalInfo.Text = $"Task: {_task.Id}\nThread: {thread?.EffectiveId}\n{ActivityText.Get("DefaultThread")}: {_task.ActiveThreadId}\nWorkspace: {thread?.WorkspaceId ?? _task.WorkspaceId}\nCheckpoint: {thread?.CheckpointEventId}";
        var text = new StringBuilder();
        text.AppendLine(ActivityText.Get(thread?.Steps.Count > 0 ? "BranchSteps" : "TaskSteps"));
        foreach (var item in steps) text.AppendLine($"[{ActivityText.State(item.Status)}] {item.Title}");
        if (steps.Count == 0) text.AppendLine(ActivityText.Get("NoStep"));
        text.AppendLine().AppendLine(ActivityText.Get("Conditions"));
        foreach (var condition in _task.Conditions) text.AppendLine($"{condition.Id}: {condition.Text}");
        if (_task.FinalReview is { } review)
        {
            text.AppendLine().AppendLine($"{ActivityText.Get("Review")}: {ActivityText.State(review.Status)}").AppendLine(review.Summary);
            foreach (var fact in review.VerifiedFacts) text.AppendLine(fact);
            if (review.OpenRisks.Count + review.MissingChecks.Count > 0) text.AppendLine(ActivityText.Get("Risks"));
            foreach (var risk in review.OpenRisks.Concat(review.MissingChecks)) text.AppendLine(risk);
        }
        AcceptanceInfo.Text = text.ToString();
    }

    private void StartStream(string taskId, string threadId)
    {
        StopStream();
        _timeline.Reset(); JournalWarning.Text = "";
        _streamCancellation = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
        var token = _streamCancellation.Token;
        var channel = Channel.CreateBounded<ActivityStreamMessage>(new BoundedChannelOptions(512) { FullMode = BoundedChannelFullMode.Wait, SingleReader = true, SingleWriter = true });
        _channel = channel;
        ConnectionStatus.Text = ActivityText.Get("Loading");
        _ = ObserveStreamAsync(channel, taskId, threadId, token);
    }

    private async Task ObserveStreamAsync(Channel<ActivityStreamMessage> channel, string taskId, string threadId, CancellationToken token)
    {
        try { await _client.ObserveAsync(taskId, threadId, 0, (message, ct) => channel.Writer.WriteAsync(message, ct), token).ConfigureAwait(false); }
        catch (OperationCanceledException) when (token.IsCancellationRequested) { }
        catch (Exception ex) { channel.Writer.TryWrite(new ActivityStreamMessage("disconnected", 0, Message: ex.Message)); }
        finally { channel.Writer.TryComplete(); }
    }

    private void Pulse(object? sender, EventArgs e)
    {
        if (!_ready) return;
        var changed = false;
        for (var count = 0; count < 100 && _channel?.Reader.TryRead(out var message) == true; count++)
        {
            switch (message.Type)
            {
                case "activity" when message.Event is not null:
                    changed |= _timeline.Apply(message.Event);
                    if (message.Event.Kind.StartsWith("task.", StringComparison.Ordinal) || message.Event.Kind.StartsWith("thread.", StringComparison.Ordinal) || message.Event.Kind.StartsWith("step.", StringComparison.Ordinal) || message.Event.Kind.StartsWith("review.", StringComparison.Ordinal)) _needsRefresh = true;
                    break;
                case "cursor": _timeline.Advance(message.Sequence); break;
                case "gap": _timeline.Advance(message.Sequence); JournalWarning.Text = ActivityText.Get("Gap"); break;
                case "reset": _timeline.Reset(message.Sequence); JournalWarning.Text = ActivityText.Get("Reset"); changed = true; break;
                case "warning": JournalWarning.Text = ActivityText.Get("Warning") + ": " + message.Message; break;
                case "connected": ConnectionStatus.Text = ActivityText.Get("Live"); break;
                case "disconnected": ConnectionStatus.Text = message.Message; break;
            }
        }
        if (changed)
        {
            ApplyLiveObservations();
            EmptyTimeline.Visibility = CollectionViewSource.GetDefaultView(_timeline.Rows).IsEmpty ? Visibility.Visible : Visibility.Collapsed;
            if (_timeline.RemovedRowCount > 0) JournalWarning.Text = ActivityText.Get("RecentOnly");
            if (FollowLatest.IsChecked == true && _timeline.Rows.Count > 0) TimelineList.ScrollIntoView(_timeline.Rows[^1]);
            UpdateActions();
        }
        if (_liveTrusted && DateTimeOffset.UtcNow - _liveReceivedAt > TimeSpan.FromSeconds(12)) InvalidateLiveState();
        if (!_controlBusy && DateTimeOffset.UtcNow - _lastLivePoll > TimeSpan.FromSeconds(3)) _ = RefreshLiveAsync(false);
        if (!_refreshing && !_controlBusy && DateTimeOffset.UtcNow - _lastRefresh > TimeSpan.FromSeconds(_needsRefresh ? 2 : 8)) _ = RefreshTasksAsync();
    }

    private void StopStream()
    {
        _streamCancellation?.Cancel(); _streamCancellation?.Dispose(); _streamCancellation = null; _channel = null;
    }

    private void UpdateActions()
    {
        ApplyLiveObservations();
        WorkspaceButton.IsEnabled = _liveTrusted && _live is { WorkspaceStatus: "bound", WorkspaceRuntime: "windows" };
        WorkspaceButton.ToolTip = WorkspaceButton.IsEnabled ? _live?.WorkspacePath : WorkspaceInfo.Text;
        ContinueButton.IsEnabled = _task is not null && (_task.Status != "completed" || _task.Outcome == "cancelled");
        ContinueButton.Content = ActivityText.Get(_task?.Outcome == "cancelled" ? "Retry" : "Continue");
        ResumeTaskButton.Visibility = _task?.Status == "blocked" ? Visibility.Visible : Visibility.Collapsed;
        ResumeTaskButton.IsEnabled = !_controlBusy;
        LiveSessionsList.IsEnabled = !_controlBusy && _liveTrusted;
        ThreadActionsMenu.IsEnabled = !_controlBusy && _task is { Status: not "completed" } && ThreadSelector.SelectedItem is ActivityThread;
        if (ThreadSelector.SelectedItem is ActivityThread branch)
            foreach (var item in ThreadActionsMenu.Items.OfType<MenuItem>())
                item.IsEnabled = (item.Tag as string) switch {
                    "thread_resume" => branch.Status == "blocked",
                    "thread_block" or "thread_close" => branch.Status == "open",
                    "thread_switch" => branch.Status != "closed" && branch.EffectiveId != _task?.ActiveThreadId,
                    _ => branch.Status != "closed" };
        TaskActionsMenu.IsEnabled = !_controlBusy;
        foreach (var item in TaskActionsMenu.Items.OfType<MenuItem>())
        {
            item.IsEnabled = (item.Tag as string) switch
            {
                "cleanup" => true,
                "cancel" => _task is { Status: "active" or "blocked" },
                "archive" => _task is { Status: "completed", ArchivedAt: null },
                "unarchive" => _task?.ArchivedAt is not null,
                _ => _task is not null
            };
        }
    }

    private async void Control_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement { Tag: string action } || _controlBusy) return;
        if (action != "cleanup" && string.IsNullOrEmpty(_selectedTaskId)) return;
        var body = new Dictionary<string, object?> { ["action"] = action };
        if (action == "cleanup")
        {
            if (!Confirm(ActivityText.Get("ConfirmCleanup"))) return;
            body["before"] = DateTimeOffset.UtcNow.AddDays(-30);
        }
        else
        {
            body["task_id"] = _selectedTaskId;
            if (_selectedThreadId.Length > 0) body["thread_id"] = _selectedThreadId;
            if (action is "thread_create" or "thread_fork")
            {
                var title = Prompt(ActivityText.Get("Name")); if (title is null) return; body["title"] = title;
            }
            else if (action is "thread_block" or "cancel")
            {
                var reason = Prompt(ActivityText.Get("Reason")); if (reason is null) return; body["summary"] = reason;
                if (action == "cancel")
                {
                    var taskId = _selectedTaskId!;
                    try
                    {
                        var live = await _client.LiveAsync(taskId, "", _lifetime.Token);
                        if (!_ready || taskId != _selectedTaskId) return;
                        if (!Confirm(string.Format(ActivityText.Get("CancelConfirmation"), live.Sessions.Count(item => item.Status == "running")))) return;
                    }
                    catch (OperationCanceledException) when (_lifetime.IsCancellationRequested) { return; }
                    catch (Exception ex) { ShowError(ex); return; }
                }
            }
            else if (action is "thread_close" or "archive" or "unarchive")
            {
                if (!Confirm(ActivityText.Get("ConfirmAction"))) return;
            }
        }
        await RunControlAsync(body, action is "thread_create" or "thread_fork");
        if (action is "resume" or "thread_resume") JournalWarning.Text = ActivityText.Get("ResumeHint");
    }

    private async Task RunControlAsync(object request, bool selectCreated = false)
    {
        _controlBusy = true; UpdateActions();
        try
        {
            var result = await _client.ControlAsync(request, _lifetime.Token);
            if (selectCreated && result.TryGetProperty("thread", out var thread) && thread.TryGetProperty("id", out var id)) _preferredThreadId = id.GetString() ?? "";
            if (result.TryGetProperty("activity_warning", out var warning)) JournalWarning.Text = warning.GetString();
            _needsRefresh = true;
            await RefreshTasksAsync();
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested) { }
        catch (Exception ex) { if (_ready) ShowError(ex); }
        finally { _controlBusy = false; if (_ready) UpdateActions(); }
    }

    private async void Stop_Click(object sender, RoutedEventArgs e)
    {
        if (_controlBusy || EventRow(sender) is not ActivityRow row || !row.CanStop || !Confirm(ActivityText.Get("ConfirmStop"))) return;
        await RunControlAsync(new { action = "stop", task_id = row.Latest.TaskId, thread_id = row.Latest.ThreadId, session_id = row.SessionId });
    }

    private void Copy_Click(object sender, RoutedEventArgs e)
    {
        try { if (EventRow(sender) is ActivityRow row && row.Command.Length > 0) System.Windows.Clipboard.SetText(row.Command); }
        catch (Exception ex) { ShowError(ex); }
    }
    private void Directory_Click(object sender, RoutedEventArgs e) => OpenRecordedPath(sender, directory: true);
    private void File_Click(object sender, RoutedEventArgs e) => OpenRecordedPath(sender, directory: false);

    private void OpenRecordedPath(object sender, bool directory)
    {
        try
        {
            if (EventRow(sender) is not ActivityRow row) return;
            var path = directory ? row.Workdir : row.FilePath;
            if (row.Runtime == "wsl" || path.Contains("[REDACTED]", StringComparison.Ordinal) || path.StartsWith(@"\\", StringComparison.Ordinal) ||
                !Path.IsPathFullyQualified(path) || (directory ? !Directory.Exists(path) : !File.Exists(path)))
                throw new InvalidOperationException(ActivityText.Get("InvalidPath"));
            var start = new ProcessStartInfo("explorer.exe") { UseShellExecute = false, CreateNoWindow = true };
            if (!directory) start.ArgumentList.Add("/select,");
            start.ArgumentList.Add(Path.GetFullPath(path));
            Process.Start(start)?.Dispose();
        }
        catch (Exception ex) { ShowError(ex); }
    }

    private async void Diff_Click(object sender, RoutedEventArgs e)
    {
        if (_controlBusy || EventRow(sender) is not ActivityRow row || !row.CanDiff) return;
        _controlBusy = true; UpdateActions();
        try
        {
            var response = await _client.DiffAsync(row.Latest.TaskId, row.Latest.ThreadId, row.Latest.Seq, _lifetime.Token);
            var diff = response.TryGetProperty("diff", out var value) ? value.GetString() : "";
            var text = new System.Windows.Controls.TextBox { Text = string.IsNullOrEmpty(diff) ? ActivityText.Get("DiffEmpty") : diff, IsReadOnly = true, FontFamily = new System.Windows.Media.FontFamily("Consolas"), FontSize = 13, TextWrapping = TextWrapping.NoWrap, VerticalScrollBarVisibility = ScrollBarVisibility.Auto, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, Margin = new Thickness(12) };
            new Window { Owner = this, Title = ActivityText.Get("DiffTitle"), Width = 920, Height = 650, MinWidth = 500, MinHeight = 300, Content = text, WindowStartupLocation = WindowStartupLocation.CenterOwner }.ShowDialog();
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested) { }
        catch (Exception ex) { if (_ready) ShowError(ex); }
        finally { _controlBusy = false; if (_ready) UpdateActions(); }
    }

    private bool Confirm(string text) => MessageBox.Show(this, text, ActivityText.Get("Confirm"), MessageBoxButton.OKCancel, MessageBoxImage.Warning) == MessageBoxResult.OK;
    private void ShowError(Exception ex) => MessageBox.Show(this, ex.Message, ActivityText.Get("OperationFailed"), MessageBoxButton.OK, MessageBoxImage.Error);

    private string? Prompt(string title)
    {
        var panel = new System.Windows.Controls.StackPanel { Margin = new Thickness(18) };
        panel.Children.Add(new TextBlock { Text = title, Margin = new Thickness(0, 0, 0, 10) });
        var input = new System.Windows.Controls.TextBox { MaxLength = 512, MinWidth = 380 };
        panel.Children.Add(input);
        var hint = new TextBlock { Text = ActivityText.Get("Required"), Margin = new Thickness(0, 10, 0, 10), TextWrapping = TextWrapping.Wrap };
        panel.Children.Add(hint);
        var actions = new System.Windows.Controls.WrapPanel();
        var ok = new System.Windows.Controls.Button { Content = ActivityText.Get("OK"), IsDefault = true };
        var cancel = new System.Windows.Controls.Button { Content = ActivityText.Get("Dismiss"), IsCancel = true };
        actions.Children.Add(ok); actions.Children.Add(cancel); panel.Children.Add(actions);
        var dialog = new Window { Owner = this, Title = title, Content = panel, Width = 470, SizeToContent = SizeToContent.Height, ResizeMode = ResizeMode.NoResize, WindowStartupLocation = WindowStartupLocation.CenterOwner };
        ok.Click += (_, _) => { if (input.Text.Trim().Length > 0) dialog.DialogResult = true; };
        dialog.Loaded += (_, _) => input.Focus();
        return dialog.ShowDialog() == true ? input.Text.Trim() : null;
    }

    private async void TaskList_SelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        if (_ready && !_suppressSelection && TaskList.SelectedItem is ActivityTask task && task.Id != _selectedTaskId) await SelectTaskAsync(task.Id);
    }
    private void ThreadSelector_SelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        if (!_ready || _suppressSelection || ThreadSelector.SelectedItem is not ActivityThread thread || string.IsNullOrEmpty(_selectedTaskId)) return;
        ShowThreadDetails(thread);
        if (_selectedThreadId != thread.EffectiveId) { _selectedThreadId = thread.EffectiveId; StartStream(_selectedTaskId, thread.EffectiveId); }
        InvalidateLiveState();
        _ = RefreshLiveAsync(true);
        UpdateActions();
    }
    private void TaskSearch_TextChanged(object sender, TextChangedEventArgs e)
    {
        if (!_ready) return;
        var view = CollectionViewSource.GetDefaultView(_tasks); view.Refresh();
        TaskCountNotice.Text = view.IsEmpty ? ActivityText.Get("NoResults") : _tasks.Count > 200 ? ActivityText.Get("MoreTasks") : "";
    }
    private async void StateFilter_SelectionChanged(object sender, SelectionChangedEventArgs e) { if (_ready && !_suppressSelection) await RefreshTasksAsync(); }
    private async void ArchiveFilter_Changed(object sender, RoutedEventArgs e) { if (_ready) await RefreshTasksAsync(); }
    private async void Refresh_Click(object sender, RoutedEventArgs e) => await RefreshTasksAsync();
    private void TimelineList_SelectionChanged(object sender, SelectionChangedEventArgs e) { if (_ready) UpdateActions(); }

    private void Window_Closed(object? sender, EventArgs e)
    {
        _ready = false; _pulse.Stop(); _lifetime.Cancel();
        StopStream(); _detailCancellation?.Cancel(); _detailCancellation?.Dispose();
        _client.Dispose(); _lifetime.Dispose();
    }
}
