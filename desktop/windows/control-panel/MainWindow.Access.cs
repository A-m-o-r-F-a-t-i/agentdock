using System.Windows;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    private long _accessRevision;
    private bool _namedDraftDirty;
    private bool _accessApplyFailed;
    private ExecutionSummaryObserver? _activitySummary;
    private ActivityClient? _activitySummaryClient;

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

    private void InitializeActivitySummary()
    {
        _activitySummaryClient = new ActivityClient(_runtime);
        _activitySummary = new ExecutionSummaryObserver(
            async token => (await _activitySummaryClient.ReadExecutionOverviewAsync(token)).Summary,
            view =>
            {
                if (!IsVisible) return;
                var text = view.Snapshot is { } snapshot
                    ? UiText.Format("ActivitySummaryCounts", snapshot.Running, snapshot.Pending, snapshot.Unknown) + " · " +
                      (snapshot.LastToolCallAt is { } last ? UiText.Format("ActivitySummaryLastRequest", last.ToLocalTime()) : UiText.Get("ActivitySummaryNoRequest"))
                    : UiText.Get("StatusUnavailable");
                if (view.Stale) text += " · " + UiText.Get("ActivitySummaryStale");
                ActivitySummaryText.Text = text;
                ActivitySummaryText.ToolTip = view.LastSuccess is { } at ? UiText.Format("LastRefresh", at) : UiText.Get("NotChecked");
            });
        Loaded += (_, _) => _activitySummary.SetVisible(IsVisible);
        IsVisibleChanged += (_, _) => _activitySummary.SetVisible(IsLoaded && IsVisible);
        Closed += (_, _) => { _activitySummary.Dispose(); _activitySummaryClient.Dispose(); };
    }
}
