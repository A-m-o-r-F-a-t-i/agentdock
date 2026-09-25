using System.Text.Json;
using AgentDock.ControlPanel;

internal static class TailscaleObservationRegression
{
    internal static void Run()
    {
        var count = 0;
        void Check(bool value, string message) { ++count; if (!value) throw new InvalidOperationException(message); }
        var now = DateTimeOffset.Parse("2026-09-25T00:00:00Z");
        var identity = new TailscaleObservationIdentity("tailscale", "funnel", "https://old.example.ts.net", "http://127.0.0.1:8765", "generation-a");
        NativeTunnelStatus Ready(TailscaleObservationIdentity binding) => new()
        { Provider = "tailscale", Mode = "funnel", Ready = true, Running = true, LocalReady = true,
          VerifiedAt = now, PublicUrl = binding.PublicOrigin, LocalOrigin = binding.LocalOrigin };
        var cache = new TailscaleObservationCache();
        var epoch = cache.Bind(identity);
        Check(cache.Publish(identity, epoch, cache.NextOrder(), Ready(identity), now), "initial observation accepted");
        Check(cache.Read(identity, epoch)?.Value.Ready == true, "same binding uses known result");
        foreach (var changed in new[] {
            identity with { PublicOrigin = "https://new.example.ts.net" },
            identity with { LocalOrigin = "http://127.0.0.1:9999" },
            identity with { Revision = "generation-b" },
            identity with { Provider = "cloudflare", Mode = "named" } })
        {
            var next = cache.Bind(changed);
            Check(cache.Read(changed, next) is null, "configuration change clears previous observation");
            Check(!cache.Publish(identity, epoch, cache.NextOrder(), Ready(identity), now), "late old response rejected");
            if (changed.Active) Check(cache.Publish(changed, next, cache.NextOrder(), Ready(changed), now), "new binding can recover");
        }
        var returned = cache.Bind(identity);
        Check(returned != epoch && !cache.Publish(identity, epoch, cache.NextOrder(), Ready(identity), now), "A-B-A cannot revive old generation");
        var oldOrder = cache.NextOrder(); var newOrder = cache.NextOrder();
        var unknown = new NativeTunnelStatus { Provider = "tailscale", Mode = "funnel", DiagnosticCode = "probe_failed" };
        Check(cache.Publish(identity, returned, newOrder, unknown, now), "latest failure accepted as unknown");
        Check(!cache.Publish(identity, returned, oldOrder, Ready(identity), now), "late local result cannot replace latest failure");
        Check(cache.Read(identity, returned)?.Value.DiagnosticCode == "probe_failed", "failure retained");
        var foreign = Ready(identity); foreign.PublicUrl = "https://foreign.example.ts.net";
        Check(!cache.Publish(identity, returned, cache.NextOrder(), foreign, now), "wrong-origin readiness rejected at cache boundary");
        cache.Invalidate();
        Check(!cache.Publish(identity, returned, cache.NextOrder(), Ready(identity), now), "explicit cancellation invalidates observations");
        var fresh = cache.Bind(identity);
        Check(cache.Publish(identity, fresh, cache.NextOrder(), Ready(identity), now), "fresh observation after cancellation accepted");
        var json = JsonSerializer.Serialize(Ready(identity));
        Check(TailscaleObservationCache.Parse(json).Ready, "complete native status accepted");
        foreach (var bad in new[] { "{}", "null", "[]", json + "{}",
            json.Replace("\"running\":true", "\"running\":null"),
            json.Replace("\"running\":true,", ""),
            json.Replace("\"ready\":true", "\"ready\":false,\"ready\":true"),
            json.Replace("\"local_ready\":true", "\"local_ready\":false"),
            json.Replace("tailscale", "cloudflare"), new string(' ', 65537) })
        {
            var rejected = false;
            try { TailscaleObservationCache.Parse(bad); }
            catch (Exception e) when (e is JsonException or InvalidDataException) { rejected = true; }
            Check(rejected, "invalid/missing/duplicate status fields rejected");
        }
        Console.WriteLine($"Tailscale observation identity/order: {count} assertions passed. No network or runtime started.");
    }
}
