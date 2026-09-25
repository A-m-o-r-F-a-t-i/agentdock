using System.IO;
using System.Net.Http;
using System.Text.Json;

namespace AgentDock.ControlPanel;

// Window-owned observer, not another execution engine. Calls stay serialized
// across hide/show, including a reader which finishes late after cancellation.
internal sealed class ExecutionSummaryObserver : IDisposable
{
    private readonly Func<CancellationToken, Task<ExecutionSummarySnapshot>> _read;
    private readonly Action<ExecutionSummaryView> _present;
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    private readonly Func<DateTimeOffset> _now;
    private readonly SemaphoreSlim _singleRead = new(1, 1);
    private CancellationTokenSource? _visible;
    private bool _disposed;
    private ExecutionSummarySnapshot? _last;
    private DateTimeOffset? _lastSuccess;
    internal Task Completion { get; private set; } = Task.CompletedTask;

    internal ExecutionSummaryObserver(Func<CancellationToken, Task<ExecutionSummarySnapshot>> read,
        Action<ExecutionSummaryView> present, Func<TimeSpan, CancellationToken, Task>? delay = null,
        Func<DateTimeOffset>? now = null)
    { _read = read; _present = present; _delay = delay ?? Task.Delay; _now = now ?? (() => DateTimeOffset.Now); }

    internal void SetVisible(bool visible)
    {
        if (_disposed || visible == (_visible is not null)) return;
        var old = _visible; _visible = null; old?.Cancel();
        if (!visible) return;
        var current = new CancellationTokenSource(); _visible = current;
        _present(new(_last, _lastSuccess, true));
        Completion = ObserveAsync(current);
    }
    private bool Current(CancellationTokenSource source) => !_disposed && ReferenceEquals(_visible, source) && !source.IsCancellationRequested;
    private async Task ObserveAsync(CancellationTokenSource source)
    {
        try
        {
            while (Current(source))
            {
                await _singleRead.WaitAsync(source.Token);
                try
                {
                    if (!Current(source)) return;
                    using var deadline = CancellationTokenSource.CreateLinkedTokenSource(source.Token);
                    deadline.CancelAfter(TimeSpan.FromSeconds(4));
                    try
                    {
                        var value = await _read(deadline.Token);
                        if (!Current(source)) return;
                        _last = value; _lastSuccess = _now();
                        _present(new(value, _lastSuccess, false));
                    }
                    catch (OperationCanceledException) when (source.IsCancellationRequested) { return; }
                    catch (Exception ex) when (ex is IOException or HttpRequestException or JsonException or OperationCanceledException or InvalidOperationException or UnauthorizedAccessException)
                    {
                        if (Current(source)) _present(new(_last, _lastSuccess, true));
                    }
                }
                finally { _singleRead.Release(); }
                await _delay(TimeSpan.FromSeconds(3), source.Token);
            }
        }
        catch (OperationCanceledException) when (source.IsCancellationRequested) { }
        finally { source.Dispose(); }
    }
    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true; var source = _visible; _visible = null; source?.Cancel();
    }
}
