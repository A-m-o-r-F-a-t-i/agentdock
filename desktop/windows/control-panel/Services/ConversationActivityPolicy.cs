namespace AgentDock.ControlPanel;

internal static class ConversationActivityPolicy
{
    internal static readonly TimeSpan ActivityWindow = TimeSpan.FromSeconds(120);
    internal static readonly TimeSpan InsertionWindow = TimeSpan.FromSeconds(180);
    internal static readonly TimeSpan UnclaimedInsertionExpiry = TimeSpan.FromSeconds(300);
    internal static readonly TimeSpan ReceiptWait = TimeSpan.FromSeconds(30);
    internal static bool IsRecent(DateTimeOffset? last, DateTimeOffset now, bool terminated) =>
        !terminated && last is not null && now >= last && now - last < ActivityWindow;
    internal static bool IsRecent(DateTimeOffset? last, DateTimeOffset? expires, DateTimeOffset now, bool terminated) =>
        !terminated && last is not null && now >= last && (expires is not null ? now < expires : now - last < ActivityWindow);
    internal static bool CanInsert(DateTimeOffset? last, DateTimeOffset now, bool terminated) =>
        !terminated && last is not null && now >= last && now - last < InsertionWindow;
}
