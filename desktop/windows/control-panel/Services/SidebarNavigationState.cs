namespace AgentDock.ControlPanel;

public enum ProjectNavigationMode { Auto, Collapsed, History }

public static class ExecutionLayout
{
    public const double ConversationRowHeight = 34;
    public const double MoreRowHeight = ConversationRowHeight / 2;
}

public sealed class ProjectNavigationState
{
    public ProjectNavigationMode Mode { get; private set; }
    public int HistoryLimit { get; private set; }
    public string Cursor { get; private set; } = "";
    public string ProtocolMode => Mode switch { ProjectNavigationMode.Collapsed => "collapsed", ProjectNavigationMode.History => "history", _ => "auto" };
    public ProjectNavigationState(bool collapsed = false) => Mode = collapsed ? ProjectNavigationMode.Collapsed : ProjectNavigationMode.Auto;
    public bool Expanded(int activeCount) => Mode == ProjectNavigationMode.History || Mode == ProjectNavigationMode.Auto && activeCount > 0;
    public void Expand() { Mode = ProjectNavigationMode.History; HistoryLimit = 5; Cursor = ""; }
    public void Collapse() { Mode = ProjectNavigationMode.Collapsed; HistoryLimit = 0; Cursor = ""; }
    public void More()
    {
        Mode = ProjectNavigationMode.History;
        HistoryLimit = HistoryLimit < 20 ? 20 : checked(HistoryLimit + 20);
    }
    public void AcceptHistory(string cursor, int limit)
    {
        if (Mode == ProjectNavigationMode.Collapsed) return;
        Cursor = cursor;
        if (limit > 0) HistoryLimit = limit;
    }
}

// Navigation is per window. Search owns a separate instance; leaving search
// restores the original instance, including any deliberate all-project collapse.
public sealed class SidebarNavigationState(bool initiallyCollapsed = false)
{
    private readonly Dictionary<string, ProjectNavigationState> _projects = new(StringComparer.Ordinal);
    public bool DefaultCollapsed { get; private set; } = initiallyCollapsed;
    public ProjectNavigationState For(string id)
    {
        if (!_projects.TryGetValue(id, out var state)) _projects[id] = state = new(DefaultCollapsed);
        return state;
    }
    public Dictionary<string, string> Modes => _projects.ToDictionary(pair => pair.Key, pair => pair.Value.ProtocolMode, StringComparer.Ordinal);
    public Dictionary<string, int> Limits => _projects.Where(pair => pair.Value.HistoryLimit > 0).ToDictionary(pair => pair.Key, pair => pair.Value.HistoryLimit, StringComparer.Ordinal);
    public Dictionary<string, string> Cursors => _projects.Where(pair => pair.Value.Cursor.Length > 0).ToDictionary(pair => pair.Key, pair => pair.Value.Cursor, StringComparer.Ordinal);
    public void CollapseAll()
    {
        DefaultCollapsed = true;
        foreach (var state in _projects.Values) state.Collapse();
    }
}
