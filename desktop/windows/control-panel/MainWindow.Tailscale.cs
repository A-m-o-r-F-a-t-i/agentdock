using System.Diagnostics;
using System.Windows;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    private bool _tailscalePanelRefreshing;
    private bool _tunnelChangeInProgress;
    private bool _tunnelSelectionDirty;

    private void ApplyTailscaleStatus(NativeTunnelStatus status)
    {
        TailscaleClientText.Text = status.Installed ? status.BinaryPath : UiText.Get("TailscaleNotInstalled");
        TailscaleConnectionText.Text = string.IsNullOrEmpty(status.BackendState) ? UiText.Get("Unknown") : status.BackendState;
        TailscaleDeviceText.Text = status.DeviceName;
        TailscaleDomainTextBox.Text = status.DnsName;
        TailscaleMcpTextBox.Text = string.IsNullOrEmpty(status.PublicUrl) ? "" : status.PublicUrl.TrimEnd('/') + "/mcp";
        TailscaleTargetText.Text = status.LocalOrigin;
        TailscaleFunnelText.Text = status.Phase switch
        {
            "CheckingLocal" => UiText.Get("TunnelPhaseCheckingLocal"), "NeedsApproval" => UiText.Get("TunnelPhaseNeedsApproval"),
            "LocalReady" => UiText.Get("TunnelPhaseLocalReady"), "VerifyingPublic" => UiText.Get("TunnelPhaseVerifyingPublic"),
            "Degraded" => UiText.Get("TunnelPhaseDegraded"), "Failed" => UiText.Get("TunnelPhaseFailed"),
            "Ready" => UiText.Get("TunnelPhaseReady"),
            _ => status.Ready ? UiText.Get("TunnelPhaseReady") : status.Running ? UiText.Get("TailscalePending") : UiText.Get("Disabled")
        };
        TailscaleKeyExpiryText.Text = status.KeyExpiry is { Year: > 1 } expiry
            ? expiry.ToLocalTime().ToString("yyyy-MM-dd HH:mm") : UiText.Get("TailscaleNoKeyExpiry");
        TailscaleDiagnosticText.Text = status.Diagnostic;
        TailscaleAuthorizeButton.IsEnabled = status.DiagnosticCode == "funnel_permission_required";
    }

    private async Task RefreshTailscalePanelAsync(bool force)
    {
        if (_tailscalePanelRefreshing) return;
        var revision = _accessRevision;
        _tailscalePanelRefreshing = true;
        TailscaleDetectButton.IsEnabled = false;
        try
        {
            var status = await _runtime.ReadTailscaleStatusAsync(force);
            if (revision == _accessRevision && SelectedTunnelMode() == "funnel") ApplyTailscaleStatus(status);
        }
        catch (Exception ex)
        {
            if (revision == _accessRevision && SelectedTunnelMode() == "funnel") TailscaleDiagnosticText.Text = ex.Message;
        }
        finally
        {
            _tailscalePanelRefreshing = false;
            UpdateTunnelModeUi();
        }
    }

    private static string PublicAccessName(string mode) => mode switch
    {
        "funnel" => "Tailscale Funnel",
        "quick" => UiText.Get("TemporaryAddress"),
        "named" => UiText.Get("CustomDomain"),
        _ => UiText.Get("LocalOnly")
    };

    private async Task ApplySelectedPublicAccessAsync()
    {
        if (_tunnelChangeInProgress) return;
        var mode = SelectedTunnelMode();
        var previous = _snapshot?.TunnelMode ?? "none";
        if (previous != mode && (previous == "funnel" || mode == "funnel"))
        {
            var confirmation = System.Windows.MessageBox.Show(this,
                UiText.Format("PublicAccessSwitchWarning", PublicAccessName(previous), PublicAccessName(mode)),
                "AgentDock", MessageBoxButton.YesNo, MessageBoxImage.Warning);
            if (confirmation != MessageBoxResult.Yes) return;
        }
        _tunnelChangeInProgress = true;
        InvalidateAccessChecks();
        UpdateTunnelModeUi();
        try
        {
            if (mode == "quick")
            {
                PublicMcpTextBox.Text = "";
                PublicTestStatusText.Text = UiText.Get("GeneratingTemporaryAddress");
                _lastAutoTestOrigin = "";
            }
            var success = await ExecuteActionAsync(UiText.Get("SwitchingPublicAccess"),
                () => _runtime.SetTunnelModeAsync(mode,
                    mode == "named" ? ServerUrlTextBox.Text.Trim() : "",
                    mode == "named" ? TunnelTokenPasswordBox.Password : ""), TunnelActionStatusText);
            FinishAccessApply(success, mode);
            if (success && mode == "funnel") TunnelActionStatusText.Text = UiText.Get("TunnelPhaseCommitted");
        }
        finally
        {
            _tunnelChangeInProgress = false;
            UpdateTunnelModeUi();
        }
        await RefreshAsync();
    }

    private async void TailscaleDetectButton_Click(object sender, RoutedEventArgs e) => await RefreshTailscalePanelAsync(true);

    private async void TailscaleStopButton_Click(object sender, RoutedEventArgs e)
    {
        if (_tunnelChangeInProgress || _snapshot?.TunnelMode != "funnel") return;
        _tunnelChangeInProgress = true;
        InvalidateAccessChecks();
        UpdateTunnelModeUi();
        try
        {
            await ExecuteActionAsync(UiText.Get("Stopping"), () => _runtime.RunTunnelActionAsync("stop"), TunnelActionStatusText);
            await RefreshTailscalePanelAsync(true);
        }
        finally
        {
            _tunnelChangeInProgress = false;
            UpdateTunnelModeUi();
        }
    }

    private void TailscaleAuthorizeButton_Click(object sender, RoutedEventArgs e)
    {
        try
        {
            Process.Start(new ProcessStartInfo("https://login.tailscale.com/admin/acls") { UseShellExecute = true });
        }
        catch (Exception ex) { TailscaleDiagnosticText.Text = ex.Message; }
    }
}
