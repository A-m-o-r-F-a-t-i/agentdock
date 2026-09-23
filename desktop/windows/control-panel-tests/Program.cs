using AgentDock.ControlPanel;

var assertions = 0;
void Check(bool condition, string name) { if (!condition) throw new InvalidOperationException(name); assertions++; }
var now = DateTimeOffset.Parse("2026-09-22T12:00:00Z");
foreach (var test in new[] { (119999d, true), (120000d, false), (120001d, false) })
    Check(ConversationActivityPolicy.IsRecent(now.AddMilliseconds(-test.Item1), now, false) == test.Item2, "activity boundary " + test.Item1);
foreach (var test in new[] { (179999d, true), (180000d, false), (180001d, false) })
    Check(ConversationActivityPolicy.CanInsert(now.AddMilliseconds(-test.Item1), now, false) == test.Item2, "composer boundary " + test.Item1);
Check(!ConversationActivityPolicy.IsRecent(null, now, false), "unknown activity");
Check(!ConversationActivityPolicy.CanInsert(null, now, false), "unknown request");
Check(!ConversationActivityPolicy.IsRecent(now.AddSeconds(1), now, false), "future activity");
Check(!ConversationActivityPolicy.CanInsert(now, now, true), "terminated composer");
Check(!ConversationActivityPolicy.IsRecent(now, now, true), "terminated activity");
var previous = new[] { "A", "B" };
List<Row> Sort(IEnumerable<Row> rows) => SidebarOrdering.Stable(previous, rows, row => row.Id, row => row.At, row => row.Pinned);
for (var second = 0; second < 600; second++)
{
    var a = now.AddSeconds(second); var b = a.AddSeconds(second % 2 == 0 ? 3 : -3);
    var sorted = Sort(new[] { new Row("B", b), new Row("A", a) });
    Check(sorted.Select(row => row.Id).SequenceEqual(previous), "alternating activity changed order");
}
Check(Sort(new[] { new Row("A", now), new Row("B", now.AddSeconds(60)) })[0].Id == "B", "significant newer project not promoted");
Check(Sort(new[] { new Row("A", now), new Row("B", now.AddMilliseconds(59999)) })[0].Id == "A", "threshold not respected");
Check(Sort(new[] { new Row("A", now), new Row("B", now.AddDays(-1), true) })[0].Id == "B", "explicit pin ignored");
Check(Sort(new[] { new Row("B", now) }).Count == 1, "removed row retained");
foreach (var legacy in new long[] { 520989, 355618, 354854, 353098, 96203, 71028 })
{
    using var json = System.Text.Json.JsonDocument.Parse($$"""{"call_id":"legacy-{{legacy}}","tool_name":"agentdock_context","elapsed_ms":{{legacy}}} """);
    var row = new ExecutionCallRow(json.RootElement);
    Check(row.TotalElapsedMs == legacy && row.DurationSource == "legacy", "legacy timing source " + legacy);
    Check(row.TotalTimingDetails.Contains(row.Duration) && row.TotalTimingDetails.Contains("历史总耗时"), "same row/detail duration");
    Check(row.ExecutionDuration == "未记录" && row.WaitDuration == "未记录", "do not fabricate old timing stages");
}
foreach (var test in new[] { ("{\"rpc_elapsed_ms\":0,\"elapsed_ms\":99}", "rpc", (long?)0), ("{}", "unknown", (long?)null), ("{\"operation_elapsed_ms\":123}", "operation", (long?)123), ("{\"rpc_elapsed_ms\":-1,\"elapsed_ms\":15}", "legacy", (long?)15) })
{
    using var json = System.Text.Json.JsonDocument.Parse(test.Item1);
    var row = new ExecutionCallRow(json.RootElement);
    Check(row.DurationSource == test.Item2 && row.TotalElapsedMs == test.Item3, "zero/unknown/source distinction");
}
Console.WriteLine($"Desktop pure-policy regression passed: {assertions} assertions. No UI or installer was launched.");
internal sealed record Row(string Id, DateTimeOffset At, bool Pinned = false);
