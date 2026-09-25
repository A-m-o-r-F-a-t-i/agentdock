using AgentDock.ControlPanel;
internal static class RuntimeDisplayRegression
{
    internal static void Run()
    {
        var count = 0;
        void Check(bool yes) { count++; if (!yes) throw new InvalidOperationException("Runtime display assertion " + count); }
        var settings = new ControlPanelSettings { Port = 9000, BrowserCdpUrl = "draft", AcpDefaultProfile = "custom-draft" };
        var manifest = new RuntimeManifest { InstallRoot = "fixture", ListenPort = 9000 };
        var now = DateTimeOffset.Now;
        var source = new RuntimeSnapshot(manifest, settings, "1.1.6", true, true, false, "http://127.0.0.1:9000/mcp", "https://kept.example", "https://kept.example/mcp", "https://draft.example", "quick", true, true, false, new(false,"","","",false), false, now, new() { Ready = false }, CloudflareReady: false);
        foreach (var mode in new[] {"quick","named","funnel"}) {
            var status = RuntimeDisplayStatus.From(source with { TunnelMode = mode });
            Check(status.HeaderKey == "LocalHealthyPublicUnavailable");
            Check(status.ServiceKey == "Running" && status.HealthKey == "Healthy" && status.BrushKey == "WarningBrush");
        }
        Check(RuntimeDisplayStatus.From(source with { TunnelMode = "none" }).HeaderKey == "RunningNormally");
        Check(RuntimeDisplayStatus.From(source with { CloudflaredRunning = true, CloudflareReady = true }).HeaderKey == "RunningNormally");
        Check(RuntimeDisplayStatus.From(source with { CloudflaredRunning = true, PublicOrigin = "" }).HeaderKey == "LocalHealthyPublicUnavailable");
        Check(RuntimeDisplayStatus.From(source with { TunnelMode = "funnel", Tailscale = new() { Ready = true } }).HeaderKey == "RunningNormally");
        Check(RuntimeDisplayStatus.From(source with { Healthy = false }).HeaderKey == "RunningHealthFailed");
        Check(RuntimeDisplayStatus.From(source with { Healthy = false, CoreRunning = false }).HeaderKey == "Stopped");
        Check(RuntimeDisplayStatus.From(null).HealthKey == "Unknown" && RuntimeDisplayStatus.From(null).ServiceKey == "Unknown");
        Check(RuntimeDisplayStatus.From(source with { CloudflaredRunning = true }).HeaderKey == "LocalHealthyPublicUnavailable");
        Check(RuntimeDisplayStatus.From(source with { CloudflaredRunning = null, CloudflareReady = null }).HeaderKey == "LocalHealthyPublicUnknown");
        Check(RuntimeDisplayStatus.From(source with { Healthy = false, CoreRunning = null }).ServiceKey == "Unknown");
        Check(RuntimeDisplayStatus.From(source with { Healthy = false, CoreRunning = null }).HeaderKey == "StatusUnavailable");
        Check(RuntimeDisplayStatus.From(source with { Healthy = true, CoreRunning = false }).ServiceKey == "Running");
        Check(RuntimeDisplayStatus.From(source with { TunnelMode = "funnel", Tailscale = null }).HeaderKey == "LocalHealthyPublicUnknown");
        Check(RuntimeDisplayStatus.From(source with { TunnelMode = "funnel", Tailscale = new() { DiagnosticCode = "probe_failed" } }).HeaderKey == "LocalHealthyPublicUnknown");
        Check(RuntimeDisplayStatus.From(source with { TunnelMode = "funnel", Tailscale = new() { Phase = "CheckingLocal" } }).HeaderKey == "LocalHealthyPublicUnknown");
        var order = new RuntimeObservationOrder();
        Check(order.Accept(now)); Check(order.Accept(now.AddSeconds(3))); Check(!order.Accept(now.AddSeconds(1))); Check(order.Accept(now.AddSeconds(4)));
        Check(settings.BrowserCdpUrl == "draft" && settings.AcpDefaultProfile == "custom-draft" && settings.Port == 9000);
        Check(source.SavedNamedOrigin == "https://draft.example" && source.Manifest == manifest && source.Healthy);
        Console.WriteLine($"Runtime local/public display: {count} assertions passed. No network, process, UI or installer started.");
    }
}
