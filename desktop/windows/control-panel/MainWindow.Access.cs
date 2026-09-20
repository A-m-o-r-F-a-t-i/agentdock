using System.Windows;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    private long _accessRevision;
    private bool _namedDraftDirty;
    private bool _accessApplyFailed;
    private DateTimeOffset? _activitySummaryAt;

    private void InvalidateAccessChecks()
    {
        _accessRevision++;
        _lastAutoTestOrigin = "";
        if (PublicTestStatusText is not null) PublicTestStatusText.Text = UiText.Get("NotChecked");
        if (TailscaleDiagnosticText is not null) TailscaleDiagnosticText.Text = UiText.Get("NotChecked");
    }

    private void NamedDraft_Changed(object sender, RoutedEventArgs e)
    {
        if (_updatingUi) return;
        _namedDraftDirty = true;
        _accessApplyFailed = false;
        InvalidateAccessChecks();
        UpdateTunnelModeUi();
    }

    // Only success commits the selected draft. Applying another provider preserves
    // the hidden named-domain draft, including an unsubmitted replacement credential.
    internal void FinishAccessApply(bool success, string mode)
    {
        _accessApplyFailed = !success;
        if (!success) return;
        _tunnelSelectionDirty = false;
        if (mode == "named")
        {
            var updating = _updatingUi;
            _updatingUi = true;
            try { TunnelTokenPasswordBox.Clear(); _passwordHistory.Remove(TunnelTokenPasswordBox); }
            finally { _updatingUi = updating; }
            _namedDraftDirty = false;
        }
    }

    private void OpenActivityCenter_Click(object sender, RoutedEventArgs e)
    {
        if (System.Windows.Application.Current is App app) app.ShowActivityCenter();
    }

    private async Task RefreshActivitySummaryAsync()
    {
        try
        {
            using var client = new ActivityClient(_runtime);
            var tasks = await client.TasksAsync("", false, CancellationToken.None);
            _activitySummaryAt = DateTimeOffset.Now;
            ActivitySummaryText.Text = UiText.Format("ActivityCounts", tasks.Tasks.Count(item => item.Status == "active"),
                tasks.Tasks.Count(item => item.Status == "blocked"), tasks.Tasks.Count >= 200 ? "+" : "", _activitySummaryAt);
        }
        catch (Exception ex) when (ex is System.Net.Http.HttpRequestException or System.IO.IOException or System.Text.Json.JsonException or OperationCanceledException or InvalidOperationException)
        {
            ActivitySummaryText.Text = UiText.Get("StatusUnavailable") + (_activitySummaryAt is { } at ? " · " + UiText.Format("LastRefresh", at) : "");
        }
    }
}
