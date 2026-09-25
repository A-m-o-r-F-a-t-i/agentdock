using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace AgentDock.ControlPanel;

public sealed class OwnedTextDescriptor
{
    [JsonPropertyName("schema_version")] public int SchemaVersion { get; set; }
    [JsonPropertyName("code")] public string Code { get; set; } = "";
    [JsonPropertyName("args")] public string[]? Args { get; set; }
    [JsonPropertyName("text_hash")] public string TextHash { get; set; } = "";
}

// Metadata accompanies an unchanged stored original. Unknown versions/codes,
// malformed arguments, user labels and stale hashes always show that original.
internal static class OwnedText
{
    private static readonly HashSet<string> Tools = new(StringComparer.Ordinal) {
        "agentdock_context", "workspace_context", "read_file", "list_dir", "search_text", "exec_command", "file_edit", "task_manage", "workspace_manage",
        "mcp_tool_search", "mcp_tool_list", "mcp_tool_inspect", "plugin_load", "session_observe", "session_act"
    };
    internal static string Render(JsonElement descriptor, string original, string tool, string labelSource, string status)
    {
        if (descriptor.ValueKind != JsonValueKind.Object) return original;
        try { return Render(descriptor.Deserialize<OwnedTextDescriptor>(), original, tool, labelSource, status); }
        catch (JsonException) { return original; }
    }
    internal static string Render(OwnedTextDescriptor? descriptor, string original, string tool, string labelSource, string status)
    {
        if (descriptor is not { SchemaVersion: 1 }) return original;
        if (descriptor.Code == "permission.update" && descriptor.Args is null)
            descriptor = new OwnedTextDescriptor { SchemaVersion = descriptor.SchemaVersion, Code = descriptor.Code, TextHash = descriptor.TextHash, Args = [] };
        if (descriptor.Args is null || descriptor.Args.Length > 6 ||
            descriptor.Args.Any(value => value is null || Encoding.UTF8.GetByteCount(value) > 1024) || labelSource == "user" ||
            !string.Equals(descriptor.TextHash, Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(original))), StringComparison.OrdinalIgnoreCase)) return original;
        string key;
        if (descriptor.Code == "tool." + tool && Tools.Contains(tool) && labelSource == "tool" && descriptor.Args.Length == 1)
            key = "OwnedTool_" + tool;
        else if (tool == "permission.update" && descriptor.Code == "permission.update" && descriptor.Args.Length == 0)
            key = "OwnedPermissionUpdate";
        else if (tool == "permission.update" && descriptor.Code == "permission.updated" && descriptor.Args.Length == 4 && status == "succeeded")
            key = "OwnedPermissionUpdated";
        else return original;
        var format = UiText.Get(key);
        if (format == key) return original;
        try { return string.Format(System.Globalization.CultureInfo.CurrentCulture, format, descriptor.Args.Cast<object?>().ToArray()); }
        catch (FormatException) { return original; }
    }
}
