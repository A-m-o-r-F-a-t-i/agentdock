namespace AgentDock.ControlPanel;

// Presentation only: public transport readiness must never rewrite local
// Core liveness or /healthz. Both the full panel and live tray use this policy.
internal sealed record RuntimeDisplayStatus(string HeaderKey, string ServiceKey, string HealthKey, string BrushKey)
{
    internal static RuntimeDisplayStatus From(RuntimeSnapshot? value)
    {
        if (value is null) return new("StatusUnavailable", "Unknown", "Unknown", "WarningBrush");
        bool? publicReady = value.TunnelMode.ToLowerInvariant() switch
        {
            "quick" or "named" => value.CloudflareReady,
            "funnel" => value.Tailscale is null || value.Tailscale.DiagnosticCode == "probe_failed" ||
                value.Tailscale.Phase == "CheckingLocal" ? null : value.Tailscale.Ready,
            "none" => true,
            _ => null
        };
        var running = value.Healthy ? true : value.CoreRunning;
        var header = value.Healthy
            ? publicReady switch { true => "RunningNormally", false => "LocalHealthyPublicUnavailable", null => "LocalHealthyPublicUnknown" }
            : running switch { true => "RunningHealthFailed", false => "Stopped", null => "StatusUnavailable" };
        return new(header, running switch { true => "Running", false => "Stopped", null => "Unknown" },
            value.Healthy ? "Healthy" : "Unavailable",
            value.Healthy && publicReady == true ? "SuccessBrush" : running == false ? "SecondaryText" : "WarningBrush");
    }
}

internal sealed class RuntimeObservationOrder
{
    private DateTimeOffset _latest = DateTimeOffset.MinValue;
    internal bool Accept(DateTimeOffset startedAt)
    {
        if (startedAt < _latest) return false;
        _latest = startedAt;
        return true;
    }
}
