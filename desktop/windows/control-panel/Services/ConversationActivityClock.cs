using System.Diagnostics;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

// One deadline scheduler serves the loaded conversation rows. It never polls
// history and never treats reconnect, render or task state as a tool request.
internal sealed class ConversationActivityClock : IDisposable
{
    private static readonly TimeSpan Window = TimeSpan.FromSeconds(30);
    private readonly Func<IEnumerable<ExecutionObject>> _items;
    private readonly DispatcherTimer _expiry = new(DispatcherPriority.Background);
    private DateTimeOffset? _serverAnchor;
    private long _anchorTimestamp;
    private bool _disposed;

    internal ConversationActivityClock(Func<IEnumerable<ExecutionObject>> items)
    {
        _items = items;
        _expiry.Tick += Expire;
    }

    internal void Synchronize(DateTimeOffset? serverNow)
    {
        if (serverNow is null) return;
        _serverAnchor = serverNow;
        _anchorTimestamp = Stopwatch.GetTimestamp();
        Refresh();
    }

    internal static bool IsRecent(DateTimeOffset? last, DateTimeOffset now, bool terminated) =>
        !terminated && last is not null && now >= last && now - last < Window;

    internal void Refresh()
    {
        if (_disposed) return;
        _expiry.Stop();
        if (_serverAnchor is null) return;
        var now = _serverAnchor.Value + Stopwatch.GetElapsedTime(_anchorTimestamp);
        TimeSpan? next = null;
        foreach (var item in _items())
        {
            item.RecentlyActive = !item.IsUnknown && IsRecent(item.LastToolCallAt, now, item.Terminated);
            if (!item.RecentlyActive || item.LastToolCallAt is null) continue;
            var remaining = item.LastToolCallAt.Value + Window - now;
            if (next is null || remaining < next) next = remaining;
        }
        if (next is null) return;
        _expiry.Interval = next.Value < TimeSpan.FromMilliseconds(1) ? TimeSpan.FromMilliseconds(1) : next.Value;
        _expiry.Start();
    }

    private void Expire(object? sender, EventArgs args) => Refresh();
    public void Dispose()
    {
        _disposed = true;
        _expiry.Stop();
        _expiry.Tick -= Expire;
    }
}
