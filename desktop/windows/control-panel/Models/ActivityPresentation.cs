using System.Text;

namespace AgentDock.ControlPanel;

public sealed class ActivityLive
{
    public string TaskId { get; set; } = "";
    public string ThreadId { get; set; } = "";
    public string WorkspaceId { get; set; } = "";
    public string WorkspacePath { get; set; } = "";
    public string WorkspaceRuntime { get; set; } = "";
    public string WorkspaceStatus { get; set; } = "unbound";
    public DateTimeOffset ObservedAt { get; set; }
    public List<ActivitySession> Sessions { get; set; } = [];
}

public sealed class ActivitySession
{
    public string SessionId { get; set; } = "";
    public string TaskId { get; set; } = "";
    public string ThreadId { get; set; } = "";
    public string Status { get; set; } = "";
    public string Workdir { get; set; } = "";
    public string Runtime { get; set; } = "";
    public long ElapsedMs { get; set; }
    public string DisplayTitle { get; set; } = "";
    public string Detail => $"{ActivityText.Get("RunningNow")} · {ActivityText.Get("Thread")}: {ThreadId} · {ElapsedMs / 1000d:0.0} s";
}

internal static class ActivityPresentation
{
    internal static string TaskState(ActivityTask task) => ActivityText.State(task.Outcome == "cancelled" ? "cancelled" : task.Status)
        + (task.ArchivedAt is null ? "" : " · " + ActivityText.State("archived"));

    internal static IReadOnlyList<ActivityStep> Steps(ActivityTask task, ActivityThread? thread) =>
        thread?.Steps.Count > 0 ? thread.Steps : task.Steps;

    internal static string ThreadSummary(ActivityTask task, ActivityThread? thread)
    {
        var text = new StringBuilder();
        var steps = Steps(task, thread);
        var step = steps.FirstOrDefault(item => item.Id == thread?.CurrentStepId);
        text.AppendLine(ActivityText.Get("Step") + ": " + (step?.Title ?? ActivityText.Get("NoStep")));
        if (task.Outcome == "cancelled")
            text.AppendLine(ActivityText.Get("CancelReason") + ": " + Missing(task.CancelReason, "NoReason"));
        if (task.Status == "blocked")
            text.AppendLine(ActivityText.Get("TaskBlockReason") + ": " + Missing(task.Blocker, "NoReason"));
        if (thread?.Status == "blocked")
            text.AppendLine(ActivityText.Get("ThreadBlockReason") + ": " + Missing(thread.BlockReason, "NoReason"));
        if (thread is not null && thread.Status != "open")
            text.AppendLine(ActivityText.Get("Thread") + ": " + ActivityText.State(thread.Status));
        text.AppendLine(ActivityText.Get("Next") + ": " + Missing(thread?.NextAction, "NoNext"));
        var summary = thread?.Summary.Length > 0 ? thread.Summary : task.Summary;
        if (summary.Length > 0) text.AppendLine(ActivityText.Get("Summary") + ": " + summary);
        return text.ToString().TrimEnd();
    }

    internal static string Missing(string? value, string key) => string.IsNullOrWhiteSpace(value) ? ActivityText.Get(key) : value;

    internal static string EventHeading(ActivityEvent value, string title)
    {
        var label = value.Kind == "task.completed" && value.Status == "cancelled"
            ? ActivityText.Get("TaskCancelledEvent") : ActivityText.Kind(value.Kind);
        // Stored event names are not user titles. Always preserve the outcome label,
        // including when an older producer persisted task.completed as the title.
        if (string.IsNullOrWhiteSpace(title) || title == value.Kind) return label;
        return value.Kind.StartsWith("command.", StringComparison.Ordinal) ? title : label + " · " + title;
    }

    internal static string EventCategory(string kind) => kind.StartsWith("command.", StringComparison.Ordinal) ? "command"
        : kind.StartsWith("file.", StringComparison.Ordinal) ? "file"
        : kind.StartsWith("step.", StringComparison.Ordinal) || kind.StartsWith("review.", StringComparison.Ordinal) ? "checkpoint" : "lifecycle";

    internal static string ContinueInstruction(ActivityTask task, ActivityThread? thread) =>
        task.Outcome == "cancelled"
            ? ActivityText.Get("RetryInstruction") + $"\nTask: {task.Id}\n" + task.Goal
            : ActivityText.Get("ContinueInstruction") + $"\nTask: {task.Id}\nThread: {thread?.EffectiveId ?? task.ActiveThreadId}\n" +
              (thread?.WorkspaceId.Length > 0 ? $"Workspace: {thread.WorkspaceId}" : task.WorkspaceId.Length > 0 ? $"Workspace: {task.WorkspaceId}" : ActivityText.Get("NoWorkspace"));
}
