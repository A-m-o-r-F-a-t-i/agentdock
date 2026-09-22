using System.Text.Json;

namespace AgentDock.ControlPanel;

internal sealed record McpUiPreference(bool Enabled, long Revision, string Warning, string RefreshHint);

internal sealed class DisplayPreferenceService(RuntimeService runtime)
{
    internal async Task<McpUiPreference> ReadAsync(CancellationToken token)
    {
        using var client = new ActivityClient(runtime);
        return Parse(await client.ExecutionGetAsync("/internal/runtime/execution/display", token).ConfigureAwait(false));
    }

    internal async Task<McpUiPreference> SaveAsync(bool enabled, long revision, CancellationToken token)
    {
        using var client = new ActivityClient(runtime);
        return Parse(await client.ExecutionPostAsync("/internal/runtime/execution/display", new { chatgpt_mcp_ui_enabled = enabled, expected_revision = revision }, token).ConfigureAwait(false));
    }

    private static McpUiPreference Parse(JsonElement value)
    {
        var enabled = value.Field("chatgpt_mcp_ui_enabled");
        var revision = value.OptionalNumber("revision");
        if (enabled.ValueKind is not (JsonValueKind.True or JsonValueKind.False) || revision is not > 0)
            throw new JsonException("服务未返回有效的显示偏好。");
        return new(enabled.GetBoolean(), revision.Value, value.Text("warning"), value.Text("refresh_hint"));
    }
}
