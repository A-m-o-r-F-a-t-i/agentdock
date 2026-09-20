using System.Diagnostics;
using System.IO;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Data;

namespace AgentDock.ControlPanel;

public partial class ActivityWindow
{
    private ActivityLive? _live;
    private bool _liveRefreshing, _liveTrusted;
    private DateTimeOffset _lastLivePoll, _liveReceivedAt, _lastSuccessfulRefresh;

    private string LastSuccessfulCheck() => _lastSuccessfulRefresh == default ? "" :
        $" · {ActivityText.Get("LastChecked")}: {_lastSuccessfulRefresh:HH:mm:ss}";

    private void ClearLiveState()
    {
        _live = null; _liveTrusted = false; _lastLivePoll = default;
        LiveSessionsList.ItemsSource = null;
        LiveSessionsList.Visibility = Visibility.Collapsed;
        WorkspaceInfo.Text = ActivityText.Get("NoWorkspace");
        LiveStatusText.Text = ActivityText.Get("LiveUnavailable");
        WorkspaceButton.IsEnabled = false;
    }

    private void InvalidateLiveState()
    {
        _liveTrusted = false;
        LiveStatusText.Text = ActivityText.Get("LiveUnavailable") +
            (_live is null ? "" : $" · {ActivityText.Get("LastChecked")}: {_live.ObservedAt.ToLocalTime():HH:mm:ss}");
        LiveSessionsList.IsEnabled = false;
        WorkspaceButton.IsEnabled = false;
        ApplyLiveObservations();
    }

    private async Task RefreshLiveAsync(bool force)
    {
        if (!_ready || _liveRefreshing || _selectedTaskId is null) return;
        if (!force && DateTimeOffset.UtcNow - _lastLivePoll < TimeSpan.FromSeconds(3)) return;
        _lastLivePoll = DateTimeOffset.UtcNow;
        _liveRefreshing = true;
        var taskId = _selectedTaskId;
        var threadId = _selectedThreadId;
        var generation = _selectionGeneration;
        try
        {
            var live = await _client.LiveAsync(taskId, threadId, _lifetime.Token);
            if (!_ready || generation != _selectionGeneration || taskId != _selectedTaskId || threadId != _selectedThreadId) return;
            if (live.TaskId != taskId || (threadId.Length > 0 && live.ThreadId != threadId) ||
                live.Sessions.Count > 128 || live.Sessions.Any(item => taskId.Length > 0 && item.TaskId != taskId))
                throw new IOException("Live activity binding mismatch.");
            _live = live; _liveTrusted = true; _liveReceivedAt = DateTimeOffset.UtcNow;
            WorkspaceInfo.Text = live.WorkspaceStatus switch {
                "bound" => ActivityText.Get("Workspace") + ": " + live.WorkspacePath,
                "missing" => ActivityText.Get("WorkspaceMissing"), _ => ActivityText.Get("NoWorkspace") };
            var running = live.Sessions.Where(item => item.Status == "running").ToList();
            foreach (var session in running)
            {
                var row = _timeline.Rows.FirstOrDefault(item => SameSession(item, session));
                session.DisplayTitle = row?.Heading ?? ActivityText.Get("UnknownCommand");
            }
            LiveSessionsList.ItemsSource = running;
            LiveSessionsList.Visibility = running.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
            var status = running.Count == 0 ? ActivityText.Get("NoRunning")
                : _task?.Status == "completed" ? ActivityText.Get("RunningAfterCancel") : ActivityText.Get("LiveCommands");
            LiveStatusText.Text = status + $" · {ActivityText.Get("LastChecked")}: {live.ObservedAt.ToLocalTime():HH:mm:ss}";
            UpdateActions();
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested) { }
        catch (Exception ex) when (ex is IOException or System.Net.Http.HttpRequestException or JsonException or OperationCanceledException)
        {
            if (_ready && generation == _selectionGeneration && taskId == _selectedTaskId && threadId == _selectedThreadId)
                InvalidateLiveState();
        }
        finally { _liveRefreshing = false; }
    }

    private static bool SameSession(ActivityRow row, ActivitySession session) => row.SessionId == session.SessionId &&
        row.Latest.TaskId == session.TaskId && row.Latest.ThreadId == session.ThreadId;

    private void ApplyLiveObservations()
    {
        var sessions = _liveTrusted ? _live?.Sessions : null;
        foreach (var row in _timeline.Rows)
            row.Observe(sessions?.FirstOrDefault(item => SameSession(row, item)), !_controlBusy);
    }

    private ActivityRow? EventRow(object sender) => (sender as FrameworkElement)?.DataContext as ActivityRow
        ?? TimelineList.SelectedItem as ActivityRow;

    private async void StopLive_Click(object sender, RoutedEventArgs e)
    {
        if (_controlBusy || !_liveTrusted || sender is not FrameworkElement { DataContext: ActivitySession session }) return;
        if (_live?.Sessions.Any(item => item.SessionId == session.SessionId && item.TaskId == session.TaskId &&
            item.ThreadId == session.ThreadId && item.Status == "running") != true) return;
        if (!Confirm(ActivityText.Get("ConfirmStop"))) return;
        await RunControlAsync(new { action = "stop", task_id = session.TaskId, thread_id = session.ThreadId, session_id = session.SessionId });
        await RefreshLiveAsync(true);
    }

    private void Workspace_Click(object sender, RoutedEventArgs e)
    {
        try
        {
            if (!_liveTrusted || _live is not { WorkspaceStatus: "bound", WorkspaceRuntime: "windows" }) return;
            var path = _live.WorkspacePath;
            if (!Path.IsPathFullyQualified(path) || path.StartsWith(@"\\", StringComparison.Ordinal) ||
                path.Contains("[REDACTED]", StringComparison.Ordinal) || !Directory.Exists(path))
                throw new InvalidOperationException(ActivityText.Get("InvalidPath"));
            var start = new ProcessStartInfo("explorer.exe") { UseShellExecute = false, CreateNoWindow = true };
            start.ArgumentList.Add(Path.GetFullPath(path));
            Process.Start(start)?.Dispose();
        }
        catch (Exception ex) { ShowError(ex); }
    }

    private void Continue_Click(object sender, RoutedEventArgs e)
    {
        try
        {
            if (_task is null) return;
            System.Windows.Clipboard.SetText(ActivityPresentation.ContinueInstruction(_task, ThreadSelector.SelectedItem as ActivityThread));
            JournalWarning.Text = ActivityText.Get("ClipboardReady");
        }
        catch (Exception ex) { ShowError(ex); }
    }

    private void CategoryFilter_Changed(object sender, SelectionChangedEventArgs e)
    {
        if (!_ready) return;
        var view = CollectionViewSource.GetDefaultView(_timeline.Rows);
        view.Refresh();
        EmptyTimeline.Visibility = view.IsEmpty ? Visibility.Visible : Visibility.Collapsed;
    }

    private void Timeline_ScrollChanged(object sender, ScrollChangedEventArgs e)
    {
        if (_ready && e.VerticalChange < 0 && e.ExtentHeightChange == 0 && e.ViewportHeightChange == 0)
            FollowLatest.IsChecked = false;
    }

    private void ReturnLatest_Click(object sender, RoutedEventArgs e)
    {
        FollowLatest.IsChecked = true;
        if (TimelineList.Items.OfType<ActivityRow>().LastOrDefault() is { } row) TimelineList.ScrollIntoView(row);
    }
}
