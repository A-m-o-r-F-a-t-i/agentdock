using AgentDock.ControlPanel;

var assertions = 0;
void Check(bool condition, string name) { if (!condition) throw new InvalidOperationException(name); assertions++; }
var now = DateTimeOffset.Parse("2026-09-22T12:00:00Z");
Check(ConversationActivityPolicy.ActivityWindow.TotalMilliseconds == 120000 &&
      ConversationActivityPolicy.InsertionWindow.TotalMilliseconds == 180000 &&
      ConversationActivityPolicy.UnclaimedInsertionExpiry.TotalMilliseconds == 300000 &&
      ConversationActivityPolicy.ReceiptWait.TotalMilliseconds == 30000,
    "shared timing constants diverged");
foreach (var test in new[] { (119999d, true), (120000d, false), (120001d, false) })
    Check(ConversationActivityPolicy.IsRecent(now.AddMilliseconds(-test.Item1), now, false) == test.Item2, "activity boundary " + test.Item1);
var interactionStart = now.AddMinutes(-10);
var interactionExpiry = interactionStart.AddSeconds(120);
Check(ConversationActivityPolicy.IsRecent(interactionStart, interactionExpiry, interactionExpiry.AddMilliseconds(-1), false), "explicit server expiry remains half-open before deadline");
Check(!ConversationActivityPolicy.IsRecent(interactionStart, interactionExpiry, interactionExpiry, false), "explicit server expiry closes exactly at deadline");
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
var daemonTiming = new ExecutionCallRow(System.Text.Json.JsonDocument.Parse("{\"status\":\"succeeded\",\"rpc_elapsed_ms\":232,\"process_elapsed_ms\":1200005}").RootElement);
Check(daemonTiming.Duration == "0.232 s" && daemonTiming.ProcessDuration == "1200.005 s", "RPC and daemon process timing stay separate");
Check(daemonTiming.TotalTimingDetails.Contains("RPC 耗时") && daemonTiming.ProcessTimingDetails.Contains("后台命令进程"), "timing labels identify separate sources");
var liveDaemonTiming = new ExecutionCallRow(System.Text.Json.JsonDocument.Parse("{\"status\":\"running\",\"rpc_elapsed_ms\":232}").RootElement);
Check(liveDaemonTiming.ProcessDuration == "仍在运行" && liveDaemonTiming.ProcessTimingDetails.Contains("RPC 已返回"), "live background process is not reported as RPC duration");
var navigation = new SidebarNavigationState();
var projectA = navigation.For("A"); var projectB = navigation.For("B");
Check(!projectA.Expanded(0) && projectA.Expanded(12), "auto navigation respects actual activity without a five-row cap");
projectA.Expand(); Check(projectA.HistoryLimit == 5, "history starts with five");
foreach (var expected in new[] {20,40,60,80}) { projectA.More(); Check(projectA.HistoryLimit == expected, "history cumulative limit " + expected); }
projectA.AcceptHistory("cursor-a", 80); Check(projectA.Cursor == "cursor-a", "retain stable history cursor");
projectA.Collapse(); Check(projectA.Cursor.Length == 0 && projectA.HistoryLimit == 0 && !projectA.Expanded(20), "manual collapse survives activity and resets history");
projectA.Expand(); Check(projectA.HistoryLimit == 5 && projectA.Expanded(0), "reopen restarts at five; history survives inactivity");
Check(projectB.Mode == ProjectNavigationMode.Auto, "project states are independent");
navigation.CollapseAll(); Check(!projectA.Expanded(30) && !projectB.Expanded(30) && !navigation.For("new").Expanded(10), "collapse all also covers new project activity");
projectB.Expand(); Check(projectB.HistoryLimit == 5 && !projectA.Expanded(10), "one project reopens after collapse all");
var searchNavigation = new SidebarNavigationState(); searchNavigation.For("A").Expand();
Check(!navigation.For("A").Expanded(10), "temporary search does not modify prior navigation state");
Check(new SidebarNavigationState().For("A").Expanded(1), "new window restores automatic mode");
Check(ExecutionLayout.MoreRowHeight * 2 == ExecutionLayout.ConversationRowHeight, "ellipsis always half conversation row height");

var transactional = new SidebarNavigationState();
transactional.For("A").Expand(); transactional.For("A").AcceptHistory("old", 5);
var revision = transactional.Revision;
var candidate = transactional.Copy(); candidate.For("A").More();
Check(transactional.For("A").HistoryLimit == 5 && transactional.For("A").Cursor == "old", "candidate cannot change successful state");
candidate.For("A").AcceptHistory("new", 20);
Check(transactional.TryCommit(candidate, revision), "successful candidate commits");
Check(transactional.For("A").HistoryLimit == 20 && transactional.For("A").Cursor == "new", "cursor and limit commit together");
Check(!transactional.TryCommit(candidate, revision), "duplicate response rejected");
revision = transactional.Revision; candidate = transactional.Copy(); candidate.For("A").More();
transactional.For("A").Collapse();
Check(!transactional.TryCommit(candidate, revision) && transactional.For("A").HistoryLimit == 0, "collapse wins over in-flight response");
for (var cycle = 0; cycle < 100; cycle++)
{
    transactional.For("A").Expand();
    revision = transactional.Revision; candidate = transactional.Copy(); candidate.For("A").More();
    Check(transactional.TryCommit(candidate, revision) && transactional.For("A").HistoryLimit == 20, "pagination cycle " + cycle);
    transactional.For("A").Collapse();
}
var footer = new ExecutionObject { IsGroupFooter = true, HasMore = true };
Check(footer.CanLoadMore, "footer enabled"); footer.IsPaging = true;
Check(!footer.CanLoadMore, "in-flight footer disabled"); footer.IsPaging = false;
Check(footer.CanLoadMore, "failure releases footer");

System.Text.Json.JsonElement Json(string text) { using var parsed = System.Text.Json.JsonDocument.Parse(text); return parsed.RootElement.Clone(); }
var separatedActivity = ExecutionObject.From(Json("""
{"conversation_id":"conv_daemon","in_flight":true,"recently_active":false,
 "last_interaction_at":"2026-09-22T11:40:00Z","interaction_expires_at":"2026-09-22T11:42:00Z",
 "statistics":{"last_tool_call_at":"2026-09-22T11:40:00Z","last_interaction_at":"2026-09-22T11:40:00Z",
 "last_activity_at":"2026-09-22T12:00:00Z","pending":1,"running":1}}
"""), "conversation");
Check(!separatedActivity.RecentlyActive && separatedActivity.InFlight && separatedActivity.VisibleInAuto,
    "expired interaction and live execution were collapsed into one state");
Check(separatedActivity.LastInteractionAt == DateTimeOffset.Parse("2026-09-22T11:40:00Z") && separatedActivity.LastActivityAt == now,
    "interaction time was overwritten by asynchronous execution activity");
Check(separatedActivity.ExecutionStateText == "待审批 1" && separatedActivity.ExecutionStateHint.Contains("独立过期"),
    "live approval state is not separately visible");
var activeGroup = new WorkspaceGroupKey("wsp_a", "A");
activeGroup.Apply(Json("{\"recent_count\":0,\"execution_count\":1,\"mode\":\"auto\"}"));
Check(activeGroup.RecentCount == 0 && activeGroup.ExecutionCount == 1 && activeGroup.VisibleActivityCount == 1,
    "project execution liveness was folded into recent interaction count");
SidebarProtocolException SidebarFailure(string text)
{
    try { SidebarResponseValidation.Parse(Json(text)); }
    catch (SidebarProtocolException error) { assertions++; return error; }
    throw new InvalidOperationException("Expected a typed sidebar protocol failure.");
}
var legalSidebar = SidebarResponseValidation.Parse(Json("""
{"latest_seq":17,"groups":[
  {"workspace_id":"wsp_a","history_limit":5,"conversations":[{"conversation_id":"conv_a","title":"A","state":{"workspace_id":"wsp_a"}}]},
  {"workspace_id":"unattributed","history_limit":5,"conversations":[{"conversation_id":"","is_unattributed":true,"title":"未归属调用"}]}
]}
"""));
Check(legalSidebar.Groups["unattributed"].Single().SelectionKey == "unattributed", "legal unattributed navigation row was rejected or assigned a fake ID");
Check(legalSidebar.Groups["wsp_a"].Single().SelectionKey == "conv_a", "ordinary navigation identity changed");
var isolatedSidebar = SidebarResponseValidation.Parse(Json("""
{"latest_seq":23,"groups":[
  {"workspace_id":"bad","history_limit":5,"conversations":[{"conversation_id":"","title":"missing"}]},
  {"workspace_id":"good","history_limit":5,"conversations":[{"conversation_id":"conv_good","state":{"workspace_id":"good"}}]}
]}
"""));
Check(isolatedSidebar.GroupFailures["bad"].Code == "SIDEBAR_CONVERSATION_ID_MISSING" && isolatedSidebar.GroupFailures["bad"].ResponseGeneration == 23,
    "group-local failure lost its stable code or response generation");
Check(isolatedSidebar.Groups["good"].Single().Id == "conv_good", "group-local isolation discarded a healthy project");
var duplicateSidebar = SidebarFailure("""
{"latest_seq":31,"groups":[
  {"workspace_id":"A","history_limit":5,"conversations":[{"conversation_id":"conv_same"}]},
  {"workspace_id":"B","history_limit":5,"conversations":[{"conversation_id":"conv_same"}]}
]}
""");
Check(duplicateSidebar.Code == "SIDEBAR_CONVERSATION_ID_DUPLICATE" && duplicateSidebar.IsPageWide && duplicateSidebar.RowType == "conversation",
    "duplicate real ID was not rejected page-wide");
var duplicateUnattributed = SidebarFailure("""
{"latest_seq":32,"groups":[{"workspace_id":"unattributed","history_limit":5,"conversations":[
  {"conversation_id":"","is_unattributed":true},{"conversation_id":"","is_unattributed":true}
]}]}
""");
Check(duplicateUnattributed.Code == "SIDEBAR_UNATTRIBUTED_DUPLICATE" && duplicateUnattributed.RowType == "unattributed",
    "duplicate unattributed rows were silently deduplicated");
var malformedUnattributed = SidebarFailure("""
{"latest_seq":33,"groups":[{"workspace_id":"A","history_limit":5,"conversations":[{"conversation_id":"","is_unattributed":true}]}]}
""");
Check(malformedUnattributed.Code == "SIDEBAR_UNATTRIBUTED_IDENTITY_INVALID", "unattributed row outside its typed group was accepted");
var reservedSidebar = SidebarFailure("""
{"latest_seq":34,"groups":[{"workspace_id":"A","history_limit":5,"conversations":[{"conversation_id":"unattributed"}]}]}
""");
Check(reservedSidebar.Code == "SIDEBAR_RESERVED_KEY", "reserved navigation key collision was accepted");
var mismatchedTiming = SidebarFailure("""
{"latest_seq":35,"recent_interaction_window_ms":119999,"groups":[]}
""");
Check(mismatchedTiming.Code == "SIDEBAR_TIMING_CONTRACT_INVALID" && mismatchedTiming.IsPageWide && mismatchedTiming.RowType == "contract",
    "a present but incompatible timing contract was accepted");
var missingOutput = new ExecutionCallRow(Json("{\"tool_name\":\"agentdock_context\",\"display_title\":\"加载上下文\",\"summary\":\"pretend output\"}"));
missingOutput.ApplyDetail(Json("{\"tool_name\":\"agentdock_context\",\"display_title\":\"加载上下文\",\"summary\":\"pretend output\"}"));
Check(missingOutput.Title.Contains("agentdock_context") && missingOutput.Title.Contains("加载上下文"), "friendly label cannot hide registered tool name");
Check(missingOutput.Output.Contains("旧记录未保存输出") && !missingOutput.Output.Contains("pretend output"), "missing output is not fabricated from summary");
var payload = new ExecutionPayloadView("输出");
payload.Describe(Json("{\"state\":\"complete\",\"ref\":\"blob-a\",\"bytes\":10,\"lines\":2,\"preview\":\"12345\"}"));
Check(payload.NeedsLoad && payload.Position.Contains("10"), "bounded preview reports full size");
Check(payload.ApplyPage(Json("{\"payload\":{\"ref\":\"blob-a\"},\"offset\":0,\"next_offset\":5,\"has_more\":true,\"text\":\"12345\"}"), "blob-a", false), "first output page");
Check(payload.RequestedOffset(false) == 5 && payload.CanReadNext && !payload.HasPrevious, "page cursor advances");
Check(payload.ApplyPage(Json("{\"payload\":{\"ref\":\"blob-a\"},\"offset\":5,\"next_offset\":10,\"has_more\":false,\"text\":\"67890\"}"), "blob-a", false), "second output page");
Check(payload.Text == "67890" && payload.HasPrevious && !payload.CanReadNext, "full output remains bounded to current page");
Check(payload.RequestedOffset(true) == 0, "page back retains prior boundary");
payload.Describe(Json("{\"state\":\"complete\",\"ref\":\"blob-a\",\"bytes\":10,\"lines\":2}"));
Check(payload.Text == "67890" && !payload.NeedsLoad, "metadata refresh preserves current reading page");
payload.Describe(Json("{\"state\":\"partial\",\"ref\":\"blob-b\",\"bytes\":4,\"lines\":1}"));
Check(!payload.ApplyPage(Json("{\"payload\":{\"ref\":\"blob-a\"},\"offset\":0,\"next_offset\":5,\"text\":\"stale\"}"), "blob-a", false), "late old blob cannot replace current result");
Check(payload.StateLabel == "部分输出" && !payload.HasPrevious, "partial results and new blob page reset");
payload.Describe(Json("{\"state\":\"not_stored\",\"reason\":\"超过存储上限\"}"));
Check(payload.Text.Contains("超过存储上限") && !payload.NeedsLoad, "explicit storage limit is not empty output");

foreach (var scenario in new[] {
    ("{\"stats_state\":\"known\",\"insertions\":26,\"deletions\":9}", "+26", "−9"),
    ("{\"stats_state\":\"known\",\"insertions\":0,\"deletions\":0}", "+0", "−0"),
    ("{\"stats_state\":\"unknown\"}", "—", ""),
    ("{\"stats_state\":\"preview\",\"dry_run\":true,\"proposed_insertions\":12,\"proposed_deletions\":6}", "预演", "") })
{
    var row = new ExecutionCallRow(Json("{\"tool_name\":\"file_edit\",\"display_title\":\"EDIT_FILE\",\"file_edit\":" + scenario.Item1 + "}"));
    Check(row.AddedLinesText == scenario.Item2 && row.DeletedLinesText == scenario.Item3, "actual/preview/unknown edit counts");
    Check(!row.Title.Contains("EDIT_FILE") && row.Title.Contains("file_edit"), "correct only confirmed historical label");
}
var thirdParty = new ExecutionCallRow(Json("{\"tool_name\":\"third:edit_file\",\"file_edit\":{\"stats_state\":\"known\",\"insertions\":2,\"deletions\":1}}"));
Check(thirdParty.Title == "third:edit_file · 调用扩展工具", "preserve actual third-party registered name");
foreach (var titleCase in new[] {
    ("read_file", "Read file", "", "read_file · 读取文件"),
    ("task_manage", "Manage recoverable tasks", "resume", "task_manage · 恢复任务"),
    ("agentdock_context", "AgentDock context", "", "agentdock_context · 加载上下文"),
    ("read_file", "read_file · read_file · 读取配置", "", "read_file · 读取配置"),
    ("exec_command", "检查 Debian 最小状态\r\n", "", "exec_command · 检查 Debian 最小状态"),
    ("exec_command", "Check server", "", "exec_command · 执行命令"),
    ("unknown", "", "", "unknown · 执行工具") })
    Check(ExecutionTitleFormatter.Format(titleCase.Item1, titleCase.Item2, titleCase.Item3) == titleCase.Item4, "Chinese action " + titleCase.Item1);
var childStats = new ExecutionCallRow(Json("{\"parent_call_id\":\"root\",\"file_edit\":{\"insertions\":2,\"deletions\":1}}"));
Check(!childStats.HasEditStatistics && childStats.AddedLinesText == "", "child span does not duplicate root totals");
await PrivilegeTransitionTests.Run(Check);
OutputPolicyTests.Run(Check);
InsertionTimelineTests.Run(Check);
Console.WriteLine($"Desktop pure-policy regression passed: {assertions} assertions. No UI or installer was launched.");
internal sealed record Row(string Id, DateTimeOffset At, bool Pinned = false);
