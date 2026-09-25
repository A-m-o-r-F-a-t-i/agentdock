using System.Windows;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    private readonly RuntimeObservationOrder _runtimeObservation = new();
    private bool _statusWindowClosed;

    private void RenderRuntimeStatus(RuntimeSnapshot? snapshot)
    {
        var status = RuntimeDisplayStatus.From(snapshot);
        HeaderStatusText.Text = UiText.Get(status.HeaderKey);
        ServiceStatusText.Text = UiText.Get(status.ServiceKey);
        HealthStatusText.Text = UiText.Get(status.HealthKey);
        StatusDot.SetResourceReference(System.Windows.Shapes.Shape.FillProperty, status.BrushKey);
        if (snapshot is null) return;
        VersionText.Text = string.IsNullOrWhiteSpace(snapshot.Version) ? UiText.Get("Unknown") : snapshot.Version;
        LocalMcpTextBox.Text = snapshot.LocalMcpUrl;
        PublicMcpTextBox.Text = _tunnelChangeInProgress && SelectedTunnelMode() == "quick" ? "" : snapshot.PublicMcpUrl;
    }

    // A live refresh updates observations, not editable controls, credentials,
    // selected access drafts or settings. It never calls the full ApplySnapshot.
    internal void ApplyLiveRuntimeStatus(RuntimeSnapshot snapshot)
    {
        if (_statusWindowClosed || !IsVisible || !_runtimeObservation.Accept(snapshot.CheckedAt)) return;
        if (_snapshot?.PublicOrigin != snapshot.PublicOrigin || _snapshot?.TunnelMode != snapshot.TunnelMode)
            InvalidateAccessChecks();
        _snapshot = snapshot;
        RenderRuntimeStatus(snapshot);
        UpdateTunnelModeUi();
    }

    internal void ApplyRuntimeStatusUnavailable(DateTimeOffset startedAt)
    {
        if (_statusWindowClosed || !IsVisible || !_runtimeObservation.Accept(startedAt)) return;
        RenderRuntimeStatus(null);
    }
}
