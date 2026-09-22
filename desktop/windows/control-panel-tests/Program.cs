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
Console.WriteLine($"Desktop pure-policy regression passed: {assertions} assertions. No UI or installer was launched.");
internal sealed record Row(string Id, DateTimeOffset At, bool Pinned = false);
