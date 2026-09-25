using System.Text.Json;

namespace AgentDock.ControlPanel;

internal sealed record ExecutionSummarySnapshot(long Running, long Pending, long Unknown,
    DateTimeOffset SampledAt, DateTimeOffset? LastToolCallAt)
{
    internal static ExecutionSummarySnapshot Parse(JsonElement value)
    {
        if (value.ValueKind != JsonValueKind.Object || !value.TryGetProperty("schema_version", out var schema) ||
            schema.ValueKind != JsonValueKind.Number || !schema.TryGetInt32(out var version) || version != 2 ||
            !value.TryGetProperty("statistics", out var stats) || stats.ValueKind != JsonValueKind.Object)
            throw new JsonException("Unsupported execution overview envelope.");
        var sampled = RequiredDate(value, "server_now");
        DateTimeOffset? last = null;
        if (stats.TryGetProperty("last_tool_call_at", out var recent) && recent.ValueKind != JsonValueKind.Null)
            last = RequiredDate(stats, "last_tool_call_at");
        return new(Count(stats, "running"), Count(stats, "pending"), Count(stats, "unknown"), sampled, last);
    }
    private static long Count(JsonElement value, string key)
    {
        if (!value.TryGetProperty(key, out var field) || field.ValueKind != JsonValueKind.Number ||
            !field.TryGetInt64(out var count) || count < 0) throw new JsonException("Invalid execution counter: " + key);
        return count;
    }
    private static DateTimeOffset RequiredDate(JsonElement value, string key)
    {
        if (!value.TryGetProperty(key, out var field) || field.ValueKind != JsonValueKind.String ||
            !field.TryGetDateTimeOffset(out var date)) throw new JsonException("Invalid execution timestamp: " + key);
        return date;
    }
}

internal sealed record ExecutionOverview(System.Text.Json.JsonElement Value, ExecutionSummarySnapshot Summary);
internal sealed record ExecutionSummaryView(ExecutionSummarySnapshot? Snapshot, DateTimeOffset? LastSuccess, bool Stale);
