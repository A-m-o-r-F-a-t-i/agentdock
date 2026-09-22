using System.Diagnostics;
using System.IO;
using System.Net.Http;
using System.Text.Json;
using System.Windows;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

internal sealed record TaskNotification(string Id, string TaskId, string Title, string ConversationId, string ThreadId)
{
    internal static TaskNotification From(JsonElement value) => new(value.Text("id"), value.Text("task_id"), value.Text("title"), value.Text("conversation_id"), value.Text("thread_id"));
}

// Owned by the application/tray, not by the selected conversation or a window.
// The server claims each task-success identity once across observer processes.
internal sealed class CompletionNotificationService : IDisposable
{
    private readonly ActivityClient _client;
    private readonly Func<TaskNotification, Task> _navigate;
    private readonly CancellationTokenSource _lifetime = new();
    private readonly DispatcherTimer _timer = new(DispatcherPriority.Background) { Interval = TimeSpan.FromSeconds(3) };
    private readonly List<TaskNotificationWindow> _visible = [];
    private bool _fetching, _disposed;

    internal CompletionNotificationService(RuntimeService runtime, Func<TaskNotification, Task> navigate)
    {
        _client = new ActivityClient(runtime); _navigate = navigate;
        _timer.Tick += async (_, _) => await PollAsync();
        _timer.Start();
    }
    private async Task PollAsync()
    {
        if (_fetching || _disposed || _visible.Count >= 3) return;
        _fetching = true;
        try
        {
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
            timeout.CancelAfter(TimeSpan.FromSeconds(8));
            var response = await _client.ExecutionPostAsync("/internal/runtime/execution/notifications", new { limit = 3 - _visible.Count }, timeout.Token);
            if (_disposed) return;
            _timer.Interval = TimeSpan.FromSeconds(3);
            foreach (var value in response.Array("notifications").Take(3 - _visible.Count))
            {
                var notification = TaskNotification.From(value);
                if (notification.Id.Length == 0 || notification.TaskId.Length == 0) continue;
                var window = new TaskNotificationWindow(notification, _navigate);
                _visible.Add(window);
                window.Closed += (_, _) => { _visible.Remove(window); Reposition(); };
                Reposition(); window.Show();
            }
        }
        catch (Exception ex) when (ex is HttpRequestException or IOException or JsonException or OperationCanceledException or InvalidOperationException)
        {
            // Reconnection must not create its own user-facing notification flood.
            Debug.WriteLine("Task completion notifications: " + ex.Message);
            if (!_disposed) _timer.Interval = TimeSpan.FromSeconds(15);
        }
        finally { _fetching = false; }
    }
    private void Reposition()
    {
        var area = SystemParameters.WorkArea;
        for (var index = 0; index < _visible.Count; index++)
        {
            var window = _visible[index];
            window.Left = Math.Max(area.Left, area.Right - window.Width - 16);
            window.Top = Math.Max(area.Top, area.Bottom - (window.Height + 12) * (index + 1) - 4);
        }
    }
    public void Dispose()
    {
        if (_disposed) return; _disposed = true;
        _timer.Stop(); _lifetime.Cancel();
        foreach (var window in _visible.ToArray()) window.Close();
        _client.Dispose(); _lifetime.Dispose();
    }
}
