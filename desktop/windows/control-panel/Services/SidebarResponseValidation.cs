using System.Text.Json;

namespace AgentDock.ControlPanel;

internal sealed class SidebarProtocolException : JsonException
{
    internal string Code { get; }
    internal string Scope { get; }
    internal string RowType { get; }
    internal long ResponseGeneration { get; }
    // Generation remains reportable evidence, but is excluded from the
    // diagnostic key so an unchanged malformed stream cannot bypass log
    // throttling merely by incrementing latest_seq.
    internal string Fingerprint => $"{Code}|{Scope}|{RowType}";
    internal bool IsPageWide => Scope == "page";
    internal string UserMessage => IsPageWide
        ? $"对话列表响应无效，已保留上次可信列表（{Code}，响应代次 {ResponseGeneration}）。"
        : $"项目对话响应无效，已保留该项目上次可信内容（{Code}，响应代次 {ResponseGeneration}）。";

    internal SidebarProtocolException(string code, string scope, string rowType, long responseGeneration)
        : base(code)
    {
        Code = code;
        Scope = scope;
        RowType = rowType;
        ResponseGeneration = responseGeneration;
    }
}

internal sealed class SidebarParseResult
{
    internal Dictionary<string, List<ExecutionObject>> Groups { get; } = new(StringComparer.Ordinal);
    internal Dictionary<string, SidebarProtocolException> GroupFailures { get; } = new(StringComparer.Ordinal);
    internal bool IsPartial => GroupFailures.Count > 0;
}

// Sidebar rows use protocol identities, not display identities. The one legal
// empty conversation_id is the explicitly typed unattributed navigation row.
// No row is assigned a generated key and no duplicate is silently discarded.
internal static class SidebarResponseValidation
{
    internal const string UnattributedNavigationKey = "unattributed";
    internal const string FooterPrefix = "footer:";

    internal static SidebarParseResult Parse(JsonElement page)
    {
        var generation = page.Number("latest_seq");
        if (page.ValueKind != JsonValueKind.Object || page.Field("groups").ValueKind != JsonValueKind.Array)
            throw Failure("SIDEBAR_RESPONSE_GROUPS_MISSING", "page", "response", generation);
        ValidateOptionalWindow(page, "recent_interaction_window_ms", (long)ConversationActivityPolicy.ActivityWindow.TotalMilliseconds, generation);
        ValidateOptionalWindow(page, "insertion_eligibility_window_ms", (long)ConversationActivityPolicy.InsertionWindow.TotalMilliseconds, generation);
        ValidateOptionalWindow(page, "unclaimed_insertion_expiry_ms", (long)ConversationActivityPolicy.UnclaimedInsertionExpiry.TotalMilliseconds, generation);
        ValidateOptionalWindow(page, "receipt_wait_ms", (long)ConversationActivityPolicy.ReceiptWait.TotalMilliseconds, generation);

        var result = new SidebarParseResult();
        var groupIds = new HashSet<string>(StringComparer.Ordinal);
        var navigationKeys = new HashSet<string>(StringComparer.Ordinal);
        var unattributedSeen = false;
        foreach (var group in page.Array("groups"))
        {
            if (group.ValueKind != JsonValueKind.Object)
                throw Failure("SIDEBAR_GROUP_INVALID", "page", "workspace", generation);
            var groupId = group.Text("workspace_id");
            if (groupId.Length == 0)
                throw Failure("SIDEBAR_GROUP_ID_MISSING", "page", "workspace", generation);
            if (!groupIds.Add(groupId))
                throw Failure("SIDEBAR_GROUP_ID_DUPLICATE", "page", "workspace", generation);
            if (groupId.StartsWith(FooterPrefix, StringComparison.Ordinal))
                throw Failure("SIDEBAR_RESERVED_KEY", "page", "workspace", generation);

            var scope = "workspace:" + groupId;
            SidebarProtocolException? localFailure = null;
            if (group.Field("conversations").ValueKind != JsonValueKind.Array)
                localFailure = Failure("SIDEBAR_GROUP_ROWS_MISSING", scope, "workspace", generation);
            if (group.Number("history_limit") is < 0 or > int.MaxValue)
                localFailure ??= Failure("SIDEBAR_GROUP_LIMIT_INVALID", scope, "workspace", generation);

            var rows = new List<ExecutionObject>();
            if (group.Field("conversations").ValueKind == JsonValueKind.Array)
            {
                foreach (var raw in group.Array("conversations"))
                {
                    if (raw.ValueKind != JsonValueKind.Object)
                    {
                        localFailure ??= Failure("SIDEBAR_CONVERSATION_INVALID", scope, "conversation", generation);
                        continue;
                    }
                    var row = ExecutionObject.From(raw, "conversation");
                    string navigationKey;
                    if (row.IsUnknown)
                    {
                        if (groupId != UnattributedNavigationKey || row.Id.Length != 0)
                            throw Failure("SIDEBAR_UNATTRIBUTED_IDENTITY_INVALID", "page", "unattributed", generation);
                        if (unattributedSeen)
                            throw Failure("SIDEBAR_UNATTRIBUTED_DUPLICATE", "page", "unattributed", generation);
                        unattributedSeen = true;
                        navigationKey = UnattributedNavigationKey;
                    }
                    else
                    {
                        if (row.Id.Length == 0)
                        {
                            localFailure ??= Failure("SIDEBAR_CONVERSATION_ID_MISSING", scope, "conversation", generation);
                            continue;
                        }
                        if (row.Id == UnattributedNavigationKey || row.Id.StartsWith(FooterPrefix, StringComparison.Ordinal))
                            throw Failure("SIDEBAR_RESERVED_KEY", "page", "conversation", generation);
                        if (groupId == UnattributedNavigationKey)
                        {
                            localFailure ??= Failure("SIDEBAR_UNATTRIBUTED_GROUP_INVALID", scope, "conversation", generation);
                            continue;
                        }
                        navigationKey = row.Id;
                    }
                    if (!navigationKeys.Add(navigationKey))
                        throw Failure("SIDEBAR_CONVERSATION_ID_DUPLICATE", "page", row.IsUnknown ? "unattributed" : "conversation", generation);
                    rows.Add(row);
                }
            }

            if (localFailure is not null) result.GroupFailures[groupId] = localFailure;
            else result.Groups[groupId] = rows;
        }
        return result;
    }

    internal static ExecutionObject? ParseSelected(JsonElement selected, long generation)
    {
        if (selected.ValueKind is JsonValueKind.Undefined or JsonValueKind.Null) return null;
        if (selected.ValueKind != JsonValueKind.Object)
            throw Failure("SIDEBAR_SELECTED_INVALID", "page", "selected", generation);
        var row = ExecutionObject.From(selected, "conversation");
        if (row.IsUnknown)
            throw Failure("SIDEBAR_UNATTRIBUTED_SELECTED_INVALID", "page", "selected", generation);
        if (row.Id.Length == 0)
            throw Failure("SIDEBAR_SELECTED_ID_MISSING", "page", "selected", generation);
        if (row.Id == UnattributedNavigationKey || row.Id.StartsWith(FooterPrefix, StringComparison.Ordinal))
            throw Failure("SIDEBAR_RESERVED_KEY", "page", "selected", generation);
        return row;
    }

    internal static SidebarProtocolException TransportFailure(string code, string rowType, long generation = 0) =>
        Failure(code, "page", rowType, generation);

    private static void ValidateOptionalWindow(JsonElement page, string field, long expected, long generation)
    {
        var raw = page.Field(field);
        if (raw.ValueKind is JsonValueKind.Undefined or JsonValueKind.Null) return;
        if (raw.ValueKind != JsonValueKind.Number || !raw.TryGetInt64(out var value) || value != expected)
            throw Failure("SIDEBAR_TIMING_CONTRACT_INVALID", "page", "contract", generation);
    }

    private static SidebarProtocolException Failure(string code, string scope, string rowType, long generation) =>
        new(code, scope, rowType, generation);
}
