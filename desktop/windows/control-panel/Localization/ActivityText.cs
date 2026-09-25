using System.Windows.Markup;

namespace AgentDock.ControlPanel;

internal static class ActivityText
{
    // Keep the established ActivityText/XAML API while using the same explicit
    // ResourceManager locale as the rest of the Windows product.
    internal static string Get(string key)
    {
        var resourceKey = "Activity" + key;
        var value = UiText.Get(resourceKey);
        return value == resourceKey ? key : value;
    }

    internal static string State(string value) => value switch
    {
        "active" => UiText.Get("ActivityLabelActive"), "blocked" => UiText.Get("ActivityLabelBlocked"),
        "completed" => UiText.Get("ActivityLabelCompleted"), "cancelled" => UiText.Get("ActivityLabelCancelled"),
        "archived" => UiText.Get("ActivityLabelArchived"), "open" => UiText.Get("ActivityLabelOpen"),
        "closed" => UiText.Get("ActivityLabelClosed"), "success" or "pass" => UiText.Get("ActivityLabelSucceeded"),
        "failed" => UiText.Get("ActivityLabelFailed"), "running" => UiText.Get("ActivityLabelRunningLastObserved"),
        "timeout" => UiText.Get("ActivityLabelTimedOut"), "killed" => UiText.Get("ActivityLabelStopped"),
        "pending" => UiText.Get("ActivityLabelPending"), "in_progress" => UiText.Get("ActivityLabelInProgress"),
        "partial" => UiText.Get("ActivityLabelPartial"), "exited" => UiText.Get("ActivityLabelExited"), _ => value
    };

    internal static string Kind(string value) => value switch
    {
        "command.started" or "command.output" or "command.completed" => UiText.Get("ActivityLabelCommand"),
        "task.created" => UiText.Get("ActivityLabelTaskCreated"),
        "task.completed" => UiText.Get("ActivityLabelTaskCompleted"),
        "task.blocked" => UiText.Get("ActivityLabelTaskBlocked"),
        "task.resumed" => UiText.Get("ActivityLabelTaskUnblocked"),
        "task.archived" => UiText.Get("ActivityLabelTaskArchived"),
        "task.unarchived" => UiText.Get("ActivityLabelTaskRestoredFromArchive"),
        "thread.created" => UiText.Get("ActivityLabelBranchCreated"),
        "thread.switched" => UiText.Get("ActivityLabelContinuationBranchChanged"),
        "thread.blocked" => UiText.Get("ActivityLabelBranchBlocked"),
        "thread.resumed" => UiText.Get("ActivityLabelBranchUnblocked"),
        "thread.closed" => UiText.Get("ActivityLabelBranchClosed"),
        "activity.cleaned" => UiText.Get("ActivityLabelHistoryCleaned"),
        "file.changed" => UiText.Get("ActivityLabelFileChanged"),
        "step.started" => UiText.Get("ActivityLabelStepStarted"), "step.summary" => UiText.Get("ActivityLabelCheckpoint"),
        "tool.started" or "tool.completed" => UiText.Get("ActivityLabelToolCall"),
        "review.completed" => UiText.Get("ActivityLabelFinalReview"),
        _ => value
    };

}

[MarkupExtensionReturnType(typeof(string))]
internal sealed class ActivityLocExtension(string key) : MarkupExtension
{
    [ConstructorArgument("key")]
    public string Key { get; set; } = key;
    public override object ProvideValue(IServiceProvider serviceProvider) => ActivityText.Get(Key);
}
