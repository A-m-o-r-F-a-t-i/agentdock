using System.Text.Json;

namespace AgentDock.ControlPanel;

internal sealed record McpUiPreference(bool Enabled, long Revision, string Warning, string RefreshHint, ToolOutputSettings ToolOutput);

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

    internal async Task<McpUiPreference> SaveOutputAsync(ToolOutputSettings output, long revision, CancellationToken token)
    {
        if (!output.Valid) throw new ArgumentOutOfRangeException(nameof(output));
        using var client = new ActivityClient(runtime);
        return Parse(await client.ExecutionPostAsync("/internal/runtime/execution/display", new { tool_output = output, expected_revision = revision }, token).ConfigureAwait(false));
    }

    private static McpUiPreference Parse(JsonElement value)
    {
        var enabled = value.Field("chatgpt_mcp_ui_enabled");
        var revision = value.OptionalNumber("revision");
        if (enabled.ValueKind is not (JsonValueKind.True or JsonValueKind.False) || revision is not > 0)
            throw new JsonException("服务未返回有效的显示偏好。");
        var output = value.Field("tool_output");
        var outputEnabled = output.Field("enabled"); var maxChars = output.OptionalNumber("max_chars");
        if (outputEnabled.ValueKind is not (JsonValueKind.True or JsonValueKind.False) || maxChars is not (>= ToolOutputSettings.Minimum and <= ToolOutputSettings.Maximum))
            throw new JsonException("服务未返回有效的工具输出配置。");
        return new(enabled.GetBoolean(), revision.Value, value.Text("warning"), value.Text("refresh_hint"), new(outputEnabled.GetBoolean(), (int)maxChars.Value));
    }
}
