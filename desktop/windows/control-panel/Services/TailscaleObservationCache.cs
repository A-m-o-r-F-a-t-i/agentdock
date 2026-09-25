using System.IO;
using System.Text.Json;

namespace AgentDock.ControlPanel;

internal sealed record TailscaleObservationIdentity(string Provider, string Mode, string PublicOrigin, string LocalOrigin, string Revision)
{
    internal bool Active => Provider == "tailscale" && Mode == "funnel";
    internal static bool SameOrigin(string left, string right) =>
        Uri.TryCreate(left, UriKind.Absolute, out var a) && Uri.TryCreate(right, UriKind.Absolute, out var b) &&
        a.UserInfo.Length == 0 && a.Query.Length == 0 && a.Fragment.Length == 0 && a.AbsolutePath == "/" && a == b;
    internal bool Matches(NativeTunnelStatus value) => value.Provider == "tailscale" && value.Mode == "funnel" &&
        (string.IsNullOrEmpty(value.LocalOrigin) || SameOrigin(value.LocalOrigin, LocalOrigin)) &&
        (!Active || string.IsNullOrEmpty(value.PublicUrl) || SameOrigin(value.PublicUrl, PublicOrigin)) &&
        (!value.Ready || Active && value.Running && value.LocalReady && value.VerifiedAt is not null &&
            SameOrigin(value.PublicUrl, PublicOrigin) && SameOrigin(value.LocalOrigin, LocalOrigin));
}

// The only Tailscale display cache. Values are observations of an immutable
// configuration, not facts transferable to another origin or generation.
internal sealed class TailscaleObservationCache
{
    internal sealed record Entry(NativeTunnelStatus Value, DateTimeOffset CheckedAt, long Order);
    private readonly object _gate = new();
    private TailscaleObservationIdentity? _identity;
    private long _generation, _order;
    private Entry? _entry;
    internal long Bind(TailscaleObservationIdentity identity)
    {
        lock (_gate)
        {
            if (_identity != identity) { _identity = identity; _entry = null; ++_generation; }
            return _generation;
        }
    }
    internal void Invalidate()
    { lock (_gate) { _identity = null; _entry = null; ++_generation; } }
    internal long NextOrder() => Interlocked.Increment(ref _order);
    internal Entry? Read(TailscaleObservationIdentity identity, long generation)
    { lock (_gate) return _identity == identity && _generation == generation ? _entry : null; }
    internal bool Publish(TailscaleObservationIdentity identity, long generation, long order, NativeTunnelStatus value, DateTimeOffset at)
    {
        lock (_gate)
        {
            if (_identity != identity || _generation != generation || !identity.Matches(value) ||
                _entry is not null && order <= _entry.Order) return false;
            _entry = new(value, at, order);
            return true;
        }
    }
    internal static NativeTunnelStatus Parse(string output)
    {
        if (output.Length > 65536) throw new InvalidDataException("Tailscale status exceeds the size limit.");
        using var document = JsonDocument.Parse(output, new JsonDocumentOptions { MaxDepth = 16 });
        var root = document.RootElement;
        if (root.ValueKind != JsonValueKind.Object ||
            root.EnumerateObject().Select(p => p.Name).Distinct(StringComparer.OrdinalIgnoreCase).Count() != root.EnumerateObject().Count())
            throw new InvalidDataException("Tailscale status must be an object with unique fields.");
        foreach (var key in new[] { "running", "ready" })
            if (!root.TryGetProperty(key, out var field) || field.ValueKind is not (JsonValueKind.True or JsonValueKind.False))
                throw new InvalidDataException("Tailscale status is missing boolean " + key + ".");
        var value = root.Deserialize<NativeTunnelStatus>() ?? throw new InvalidDataException("Tailscale status is empty.");
        if (value.Provider != "tailscale" || value.Mode != "funnel" ||
            value.Ready && (!value.Running || !value.LocalReady || value.VerifiedAt is null))
            throw new InvalidDataException("Invalid Tailscale readiness observation.");
        return value;
    }
}
