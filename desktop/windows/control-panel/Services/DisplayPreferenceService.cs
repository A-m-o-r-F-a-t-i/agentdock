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
            throw new JsonException(UiText.Get("DisplayPreferenceInvalid"));
        var warning = value.Text("warning_code") == "display_preferences_load_failed"
            ? UiText.Format("ThemeLoadWarning", value.Text("warning_detail")) : value.Text("warning");
        var hint = value.Text("refresh_hint_code") == "refresh_chatgpt_connection"
            ? UiText.Get("DisplayRefreshConnectionHint") : value.Text("refresh_hint");
        return new(enabled.GetBoolean(), revision.Value, warning, hint);
    }
}
