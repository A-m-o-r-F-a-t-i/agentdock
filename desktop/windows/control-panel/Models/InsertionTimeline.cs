using System.Text.Json;

namespace AgentDock.ControlPanel;

// Mix presentation rows only; call IDs, query cursors, counts and lifecycle
// remain owned by the existing execution service. Each supplement has one row.
public static class InsertionTimeline
{
    public const double MessageRowHeight = 62;
    public static (string Id, double WithinRow) Anchor(IReadOnlyList<ExecutionCallRow> rows, double offset, double callHeight)
    {
        var position = 0.0;
        for (var index = 0; index < rows.Count; index++)
        {
            var height = rows[index].IsInsertion ? MessageRowHeight : callHeight;
            if (offset < position + height || index == rows.Count - 1) return (rows[index].Id, Math.Clamp(offset - position, 0, height));
            position += height;
        }
        return ("", 0);
    }

    public static double? OffsetOf(IEnumerable<ExecutionCallRow> rows, string id, double callHeight)
    {
        var position = 0.0;
        foreach (var row in rows)
        {
            if (row.Id == id) return position;
            position += row.IsInsertion ? MessageRowHeight : callHeight;
        }
        return null;
    }

    public static List<ExecutionCallRow> Merge(IEnumerable<ExecutionCallRow> current, IEnumerable<JsonElement> messages, string conversation, string task, string search, string status, string view, DateTimeOffset? now)
    {
        var rows = current.ToArray();
        var existing = rows.Where(row => row.IsInsertion).ToDictionary(row => row.Id, StringComparer.Ordinal);
        var additions = new List<ExecutionCallRow>();
        var seen = new HashSet<string>(StringComparer.Ordinal);
        if (view is "active" or "all")
            foreach (var item in messages.Take(512))
            {
                var id = item.Text("insertion_id");
                if (id.Length == 0 || item.Text("conversation_id") != conversation || !seen.Add(id) || task.Length > 0 && item.Text("task_id") != task) continue;
                if (status.Length > 0 && !(status == "unknown" && (InsertionPresentation.Unconfirmed(item.Text("status")) || item.Text("status") == "attached"))) continue;
                if (search.Length > 0 && !item.Text("text").Contains(search, StringComparison.OrdinalIgnoreCase) && !InsertionPresentation.State(item).Contains(search, StringComparison.OrdinalIgnoreCase)) continue;
                if (existing.TryGetValue(id, out var row)) row.ApplyInsertion(item, now); else row = ExecutionCallRow.FromInsertion(item, now);
                additions.Add(row);
            }
        additions = additions.OrderBy(row => row.TimelineAt).ThenBy(row => row.Id, StringComparer.Ordinal).ToList();
        var result = new List<ExecutionCallRow>(rows.Length + additions.Count);
        var index = 0;
        foreach (var call in rows.Where(row => !row.IsInsertion))
        {
            while (index < additions.Count && additions[index].TimelineAt <= call.TimelineAt) result.Add(additions[index++]);
            result.Add(call);
        }
        while (index < additions.Count) result.Add(additions[index++]);
        return result;
    }
}
