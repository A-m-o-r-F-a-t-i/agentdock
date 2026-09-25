namespace AgentDock.ControlPanel;

public enum ProjectNavigationMode { Auto, Collapsed, History }

public static class ExecutionLayout
{
    public const double ConversationRowHeight = 34;
    public const double MoreRowHeight = ConversationRowHeight / 2;
}

public sealed class ProjectNavigationState
{
    private readonly Action? _changed;
    public long IntentRevision { get; private set; }
    public ProjectNavigationMode Mode { get; private set; }
    public int HistoryLimit { get; private set; }
    public string Cursor { get; private set; } = "";
    public string ProtocolMode => Mode switch { ProjectNavigationMode.Collapsed => "collapsed", ProjectNavigationMode.History => "history", _ => "auto" };
    public ProjectNavigationState(bool collapsed = false) : this(collapsed, null) { }
    internal ProjectNavigationState(bool collapsed, Action? changed)
    { Mode = collapsed ? ProjectNavigationMode.Collapsed : ProjectNavigationMode.Auto; _changed = changed; }
    internal void CopyFrom(ProjectNavigationState source)
    { Mode = source.Mode; HistoryLimit = source.HistoryLimit; Cursor = source.Cursor; IntentRevision = source.IntentRevision; }
    public bool Expanded(int activeCount) => Mode == ProjectNavigationMode.History || Mode == ProjectNavigationMode.Auto && activeCount > 0;
    public void Expand() { Mode = ProjectNavigationMode.History; HistoryLimit = 5; Cursor = ""; IntentRevision++; _changed?.Invoke(); }
    public void Collapse() { Mode = ProjectNavigationMode.Collapsed; HistoryLimit = 0; Cursor = ""; IntentRevision++; _changed?.Invoke(); }
    public void More()
    {
        if (HistoryLimit > int.MaxValue - 20) throw new InvalidOperationException("历史对话数量超过分页范围。");
        Mode = ProjectNavigationMode.History;
        HistoryLimit = HistoryLimit < 20 ? 20 : HistoryLimit + 20;
        IntentRevision++; _changed?.Invoke();
    }
    public void AcceptHistory(string cursor, int limit)
    {
        if (Mode == ProjectNavigationMode.Collapsed) return;
        Cursor = cursor;
        if (limit > 0) HistoryLimit = limit;
        _changed?.Invoke();
    }
}

// Navigation is per window. Search owns a separate instance; leaving search
// restores the original instance, including any deliberate all-project collapse.
public sealed class SidebarNavigationState(bool initiallyCollapsed = false)
{
    private readonly Dictionary<string, ProjectNavigationState> _projects = new(StringComparer.Ordinal);
    public bool DefaultCollapsed { get; private set; } = initiallyCollapsed;
    public long Revision { get; private set; }
    public ProjectNavigationState For(string id)
    {
        if (!_projects.TryGetValue(id, out var state)) _projects[id] = state = new(DefaultCollapsed, () => Revision++);
        return state;
    }
    public Dictionary<string, string> Modes => _projects.ToDictionary(pair => pair.Key, pair => pair.Value.ProtocolMode, StringComparer.Ordinal);
    public Dictionary<string, int> Limits => _projects.Where(pair => pair.Value.HistoryLimit > 0).ToDictionary(pair => pair.Key, pair => pair.Value.HistoryLimit, StringComparer.Ordinal);
    public Dictionary<string, string> Cursors => _projects.Where(pair => pair.Value.Cursor.Length > 0).ToDictionary(pair => pair.Key, pair => pair.Value.Cursor, StringComparer.Ordinal);
    public SidebarNavigationState Copy()
    {
        var candidate = new SidebarNavigationState(DefaultCollapsed);
        foreach (var pair in _projects) candidate.For(pair.Key).CopyFrom(pair.Value);
        candidate.Revision = Revision;
        return candidate;
    }
    public bool TryCommit(SidebarNavigationState candidate, long expectedRevision)
    {
        if (Revision != expectedRevision || ReferenceEquals(this, candidate)) return false;
        foreach (var pair in candidate._projects) For(pair.Key).CopyFrom(pair.Value);
        DefaultCollapsed = candidate.DefaultCollapsed;
        Revision++;
        return true;
    }
    public void CollapseAll()
    {
        DefaultCollapsed = true; Revision++;
        foreach (var state in _projects.Values) state.Collapse();
    }
}
