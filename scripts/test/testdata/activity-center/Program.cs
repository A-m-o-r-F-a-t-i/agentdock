using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.Json;
using AgentDock.ControlPanel;
using ActivityEvent = AgentDock.ControlPanel.ActivityEvent;

internal static partial class Program
{
    [STAThread]
    private static int Main(string[] args)
    {
        if (args.Length != 1 || Directory.Exists(args[0])) throw new ArgumentException("Provide a fresh isolated test directory.");
        Directory.CreateDirectory(args[0]);
        var started = Stopwatch.StartNew();
        try
        {
            TestTimeline();
            TestPresentation();
            TestPublicDiscoveryAsync().GetAwaiter().GetResult();
            TestParserAsync().GetAwaiter().GetResult();
            TestNetworkAsync(Path.Combine(args[0], "network")).GetAwaiter().GetResult();
            TestRendering(Path.Combine(args[0], "render"));
            TestExecutionParserAsync().GetAwaiter().GetResult();
            TestExecutionRendering(Path.Combine(args[0], "execution-render"));
            var executionScale = TestExecutionLargeLists(Path.Combine(args[0], "execution-scale"));
            File.WriteAllText(Path.Combine(args[0], "result.json"), JsonSerializer.Serialize(new
            {
                passed = true, elapsed_ms = started.ElapsedMilliseconds,
                execution_large_list_fixture = executionScale,
                execution_single_line_rows_and_no_task_calls = true, execution_right_click_targets_pointer = true,
                execution_branch_view_does_not_change_continuation = true, conversation_compact_task_progress_and_detail_next_action = true, no_task_hides_progress_only = true, task_milestones_are_separate = true,
                execution_projection_duplicate_replay_and_output_preferences = true,
                execution_scaled_rendering = new[] { "1280x800@100% light/dark", "840x640@125%", "1000x720@150%", "1280x800@200%" },
                access_modes_12_cross_transitions_and_4_reapplies = true, access_draft_failure_and_refresh_protection = true,
                task_cancel_archive_and_reason_projection = true, live_session_stop_guard = true,
                anonymous_discovery_challenge_and_metadata_validation = true,
                timeline_deduplicates_and_merges_sessions = true, output_and_row_memory_bounded = true,
                fragmented_sse_and_large_ids = true, bounded_sse_lines = true,
                authenticated_loopback_client = true, redirect_refused = true,
                reconnect_last_event_id = true, cross_thread_events_refused = true,
                cancellation_releases_connection = true, window_close_cancels_observer = true,
                offscreen_dpi_scaled_rendering = new[] { "1180x800@100%", "840x560@125%", "1000x700@150%", "1180x800@200%" },
                test_source = "isolated HTTP fixtures and WPF window; production tray and runtime unchanged"
            }, new JsonSerializerOptions { WriteIndented = true }));
            Console.WriteLine("Activity center regressions passed.");
            return 0;
        }
        catch (Exception error) { Console.Error.WriteLine(error); return 1; }
    }

    private static void Require(bool condition, string error) { if (!condition) throw new InvalidOperationException(error); }

    private static ActivityEvent Sample(ulong sequence, string kind = "command.output", string session = "session_fixture") => new()
    {
        SchemaVersion = 1, Seq = sequence, EventId = $"evt_{sequence}", CreatedAt = DateTimeOffset.UtcNow,
        TaskId = LocalFixture.TaskId, ThreadId = "main", StepId = "verify", SessionId = session,
        Kind = kind, Status = "running", ToolName = "exec_command", Runtime = "windows",
        Title = "验证命令输出 / Verify command output", DisplayCommand = "go test ./internal/activity ./internal/taskstate",
        Workdir = @"E:\PROJECT\AgentDock", WorkspaceId = "wsp_fixture"
    };

    private static void TestTimeline()
    {
        var timeline = new ActivityTimeline();
        Require(timeline.Apply(Sample(1, "command.started")), "Initial command was rejected.");
        var output = Sample(2); output.OutputPreview = "第一行 / first line\n";
        timeline.Apply(output);
        var done = Sample(3, "command.completed"); done.Status = "failed"; done.ExitCode = 7; done.CommandOk = false; done.ElapsedMs = 1234;
        timeline.Apply(done);
        Require(timeline.Rows.Count == 1 && timeline.Rows[0].Stdout.Contains("第一行") && !timeline.Rows[0].CanStop && timeline.Rows[0].Latest.ExitCode == 7, "Command events were not merged truthfully.");
        Require(!timeline.Apply(output), "Duplicate replay was applied.");
        var other = Sample(4); other.ThreadId = "thr_1234567890abcdef";
        timeline.Apply(other);
        Require(timeline.Rows.Count == 2, "Identical session text crossed task/thread boundaries.");
        for (ulong sequence = 5; sequence < 100; sequence++)
        {
            var value = Sample(sequence); value.OutputPreview = new string('中', 1000) + "🙂";
            timeline.Apply(value);
        }
        var row = timeline.Rows[0];
        Require(row.Truncated && row.Stdout.Length <= ActivityRow.MaxOutputCharacters && !char.IsLowSurrogate(row.Stdout[0]), "Output tail limit corrupted text or did not hold.");
        for (ulong sequence = 100; sequence < 2200; sequence++) timeline.Apply(Sample(sequence, "tool.completed", ""));
        Require(timeline.Rows.Count == ActivityTimeline.MaxRows && timeline.RemovedRowCount > 1000, "Timeline memory is unbounded.");
        timeline.Advance(3000);
        Require(!timeline.Apply(Sample(2500)), "Filtered stream cursor was ignored.");
        timeline.Reset();
        Require(timeline.Rows.Count == 0 && timeline.LastSequence == 0, "Thread switch retained prior rows.");
    }

    private static async Task TestParserAsync()
    {
        const ulong precise = 9007199254740993;
        var value = Sample(precise, "command.completed"); value.OutputPreview = "中文🙂\nsecond line";
        var json = JsonSerializer.Serialize(value, ActivityClient.JsonOptions);
        var wire = $"retry: 1000\n\nid: {precise}\nevent: activity\ndata: {json}\n\n: heartbeat\n\nid: 8\nevent: reset\ndata: {{\"latest_seq\":8}}\n\n";
        var parser = new ActivitySseReader(new FragmentedReader(wire));
        Require((await parser.ReadEventAsync(CancellationToken.None))?.Type == "heartbeat", "SSE preamble was not handled.");
        var decoded = await parser.ReadEventAsync(CancellationToken.None);
        Require(decoded?.Sequence == precise && decoded.Event?.OutputPreview == value.OutputPreview, "Fragmented SSE lost UTF-16 text or integer precision.");
        Require((await parser.ReadEventAsync(CancellationToken.None))?.Type == "heartbeat", "SSE heartbeat was not handled.");
        Require((await parser.ReadEventAsync(CancellationToken.None))?.Type == "reset", "SSE reset was not handled.");
        foreach (var malformed in new[]
        {
            new string('x', ActivitySseReader.MaximumEventCharacters + 1) + "\n",
            "id: invalid\nevent: activity\ndata: {}\n\n",
            "id: 7\nevent: activity\ndata: {\"schema_version\":1,\"seq\":8}\n\n"
        })
        {
            try { await new ActivitySseReader(new StringReader(malformed)).ReadEventAsync(CancellationToken.None); throw new InvalidOperationException("Malformed/oversized SSE was accepted."); }
            catch (IOException) { }
        }
        using var cancelled = new CancellationTokenSource(); cancelled.Cancel();
        try { await new ActivitySseReader(new BlockingReader()).ReadEventAsync(cancelled.Token); throw new InvalidOperationException("Parser ignored cancellation."); }
        catch (OperationCanceledException) { }
    }

    private static async Task TestNetworkAsync(string root)
    {
        Directory.CreateDirectory(root);
        using var fixture = new LocalFixture(root);
        fixture.WriteRuntime(root);
        using var runtime = new RuntimeService(root);
        using var client = new ActivityClient(runtime);
        var tasks = await client.TasksAsync("active", false, CancellationToken.None);
        Require(tasks.Tasks.Count == 1 && tasks.Tasks[0].Id == LocalFixture.TaskId && tasks.Tasks[0].ActiveThread?.EffectiveId == "main", "Task list contract changed.");
        var threads = await client.ThreadsAsync(LocalFixture.TaskId, CancellationToken.None);
        Require(threads.Threads.Count == 2, "Thread list was not loaded.");
        var observed = new List<ulong>();
        var completed = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        using (var stop = new CancellationTokenSource())
        {
            var observe = client.ObserveAsync(LocalFixture.TaskId, "main", 0, (message, token) =>
            {
                if (message.Event is { } item)
                {
                    observed.Add(item.Seq);
                    if (item.Kind == "command.completed") completed.TrySetResult();
                }
                return ValueTask.CompletedTask;
            }, stop.Token);
            await completed.Task.WaitAsync(TimeSpan.FromSeconds(10));
            stop.Cancel();
            try { await observe.WaitAsync(TimeSpan.FromSeconds(3)); } catch (OperationCanceledException) { }
        }
        Require(observed.SequenceEqual(new ulong[] { 1, 2, 3 }), "SSE reconnect duplicated or lost execution events.");
        for (var attempt = 0; attempt < 30 && fixture.ActiveStreams > 0; attempt++) await Task.Delay(100);
        Require(fixture.ActiveStreams == 0, "Cancelled observer retained a fixture connection.");
        Require(fixture.Cursors.Take(2).SequenceEqual(new[] { "0", "2" }), "Reconnect did not send Last-Event-ID.");
        Require(fixture.AuthenticatedRequests > 0 && fixture.UnauthorizedRequests == 0, "Local activity request did not use the protected runtime credential.");
        using (var redirectTrap = new LocalFixture(Path.Combine(root, "trap")))
        {
            fixture.RedirectTarget = redirectTrap.Origin;
            try { await client.TasksAsync("", false, CancellationToken.None); throw new InvalidOperationException("Activity client followed a redirect."); }
            catch (System.Net.Http.HttpRequestException ex) { Require((int?)ex.StatusCode == 302, "Redirect refusal returned an unexpected status."); }
            Require(redirectTrap.RequestCount == 0, "Redirected request reached the second endpoint.");
            fixture.RedirectTarget = null;
        }
        fixture.WrongThread = true;
        var refused = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        using (var stop = new CancellationTokenSource())
        {
            var observe = client.ObserveAsync(LocalFixture.TaskId, "main", 0, (message, token) =>
            {
                if (message.Event is not null) throw new InvalidOperationException("Cross-thread event reached the consumer.");
                if (message.Type == "disconnected" && message.Message.Contains("binding mismatch", StringComparison.Ordinal)) refused.TrySetResult();
                return ValueTask.CompletedTask;
            }, stop.Token);
            await refused.Task.WaitAsync(TimeSpan.FromSeconds(5));
            stop.Cancel();
            try { await observe.WaitAsync(TimeSpan.FromSeconds(3)); } catch (OperationCanceledException) { }
        }
        fixture.WrongThread = false;
        await client.ControlAsync(new { action = "thread_switch", task_id = LocalFixture.TaskId, thread_id = LocalFixture.BranchId }, CancellationToken.None);
        Require(fixture.ControlCount == 1, "Thread control was not sent as JSON to the local runtime.");
    }

    private sealed class FragmentedReader(string text) : TextReader
    {
        private int offset;
        public override ValueTask<int> ReadAsync(Memory<char> buffer, CancellationToken cancellationToken = default)
        {
            cancellationToken.ThrowIfCancellationRequested();
            var count = Math.Min(Math.Min(3, buffer.Length), text.Length - offset);
            text.AsMemory(offset, count).CopyTo(buffer); offset += count;
            return ValueTask.FromResult(count);
        }
    }
    private sealed class BlockingReader : TextReader
    {
        public override async ValueTask<int> ReadAsync(Memory<char> buffer, CancellationToken cancellationToken = default) { await Task.Delay(Timeout.Infinite, cancellationToken); return 0; }
    }
}
