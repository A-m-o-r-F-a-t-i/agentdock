using System.IO;
using System.Text.Json;

namespace AgentDock.ControlPanel;

// Boundary for the existing service/tunnel status contract. In particular, missing
// booleans must not deserialize to false and masquerade as an observed stop.
internal static class NativeStatusReader
{
    internal static NativeServiceStatus ParseService(string output)
    {
        using var document = Read(output);
        var root = document.RootElement;
        return new NativeServiceStatus
        {
            Running = Boolean(root, "running"),
            NexusConnected = Boolean(root, "nexus_connected")
        };
    }

    internal static NativeTunnelStatus ParseCloudflare(string output, string mode, string origin)
    {
        using var document = Read(output);
        var root = document.RootElement;
        var running = Boolean(root, "running");
        var ready = Boolean(root, "ready");
        if (mode is not ("quick" or "named") || Text(root, "mode") != mode || Text(root, "provider") != "cloudflare")
            throw new InvalidDataException("Native tunnel status belongs to another provider or mode.");
        var publicUrl = root.TryGetProperty("public_url", out var url) ? url.GetString() ?? "" : "";
        if (ready && (!running || publicUrl.Length == 0) ||
            publicUrl.Length > 0 && !SameOrigin(publicUrl, origin))
            throw new InvalidDataException("Native tunnel readiness does not match the observed origin.");
        return new NativeTunnelStatus { Provider = "cloudflare", Mode = mode, Running = running, Ready = ready, PublicUrl = publicUrl };
    }

    private static bool SameOrigin(string left, string right) =>
        Uri.TryCreate(left, UriKind.Absolute, out var a) && Uri.TryCreate(right, UriKind.Absolute, out var b) &&
        a.UserInfo.Length == 0 && a.Query.Length == 0 && a.Fragment.Length == 0 && a.AbsolutePath == "/" &&
        (a.Scheme == Uri.UriSchemeHttps || a.Scheme == Uri.UriSchemeHttp && a.IsLoopback) && a == b;

    private static bool Boolean(JsonElement value, string name) =>
        value.TryGetProperty(name, out var field) && field.ValueKind is JsonValueKind.True or JsonValueKind.False
            ? field.GetBoolean() : throw new InvalidDataException("Native status is missing boolean " + name + ".");
    private static string? Text(JsonElement value, string name) =>
        value.TryGetProperty(name, out var field) && field.ValueKind == JsonValueKind.String ? field.GetString() : null;
    private static JsonDocument Read(string text)
    {
        if (text.Length > 65536) throw new InvalidDataException("Native status exceeds the size limit.");
        var document = JsonDocument.Parse(text, new JsonDocumentOptions { MaxDepth = 16 });
        if (document.RootElement.ValueKind == JsonValueKind.Object &&
            document.RootElement.EnumerateObject().Select(p => p.Name).Distinct(StringComparer.Ordinal).Count() ==
            document.RootElement.EnumerateObject().Count()) return document;
        document.Dispose();
        throw new InvalidDataException("Native status must be an object with unique fields.");
    }
}
