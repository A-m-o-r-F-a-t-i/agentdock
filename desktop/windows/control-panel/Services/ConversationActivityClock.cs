using System.Diagnostics;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

// One deadline scheduler serves the loaded conversation rows. It never polls
// history and never treats reconnect, render or task state as a tool request.
internal sealed class ConversationActivityClock : IDisposable
{
    internal static readonly TimeSpan ActivityWindow = ConversationActivityPolicy.ActivityWindow;
    internal static readonly TimeSpan InsertionWindow = ConversationActivityPolicy.InsertionWindow;
    internal event EventHandler? Changed;
    internal DateTimeOffset? ServerNow => _serverAnchor is { } anchor ? anchor + Stopwatch.GetElapsedTime(_anchorTimestamp) : null;
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
        ConversationActivityPolicy.IsRecent(last, now, terminated);

    internal static bool CanInsert(DateTimeOffset? last, DateTimeOffset now, bool terminated) =>
        ConversationActivityPolicy.CanInsert(last, now, terminated);

    internal void Refresh()
    {
        if (_disposed) return;
        _expiry.Stop();
        if (_serverAnchor is null) return;
        var now = _serverAnchor.Value + Stopwatch.GetElapsedTime(_anchorTimestamp);
        TimeSpan? next = null;
        foreach (var item in _items())
        {
            item.RecentlyActive = !item.IsUnknown && !item.IsOrphan && !item.IsGroupFooter && IsRecent(item.LastActivityAt, now, item.Terminated);
            item.InsertionEligible = !item.IsUnknown && !item.IsOrphan && !item.IsGroupFooter && !item.Trashed && CanInsert(item.LastToolCallAt, now, item.Terminated);
            if (item.RecentlyActive && item.LastActivityAt is { } activity)
            {
                var remaining = activity + ActivityWindow - now;
                if (next is null || remaining < next) next = remaining;
            }
            if (item.InsertionEligible && item.LastToolCallAt is { } request)
            {
                var remaining = request + InsertionWindow - now;
                if (next is null || remaining < next) next = remaining;
            }
        }
        Changed?.Invoke(this, EventArgs.Empty);
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
