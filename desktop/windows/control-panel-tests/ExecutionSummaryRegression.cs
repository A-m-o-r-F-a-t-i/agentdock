using System.Collections.Concurrent;
using System.Text.Json;
using System.Threading.Channels;
using AgentDock.ControlPanel;

internal static class ExecutionSummaryRegression
{
    internal static async Task RunAsync()
    {
        var assertions = 0;
        void Check(bool value, string message) { assertions++; if (!value) throw new InvalidOperationException(message); }
        JsonElement Json(string s) { using var d = JsonDocument.Parse(s); return d.RootElement.Clone(); }
        string Envelope(string stats, int schema = 2) => "{\"schema_version\":" + schema + ",\"server_now\":\"2026-09-24T10:00:00Z\",\"statistics\":" + stats + "}";
        foreach (var invalid in new[] { "{}", "null", "[]", "{\"running\":null,\"pending\":0,\"unknown\":0}", "{\"running\":\"0\",\"pending\":0,\"unknown\":0}", "{\"running\":-1,\"pending\":0,\"unknown\":0}", "{\"running\":1.5,\"pending\":0,\"unknown\":0}", "{\"running\":0,\"pending\":0}", "{\"running\":0,\"pending\":0,\"unknown\":0,\"last_tool_call_at\":false}" })
        {
            try { ExecutionSummarySnapshot.Parse(Json(Envelope(invalid))); throw new InvalidOperationException("invalid overview accepted: " + invalid); }
            catch (JsonException) { assertions++; }
        }
        foreach (var schema in new[] { 0, 1, 3 })
        {
            try { ExecutionSummarySnapshot.Parse(Json(Envelope("{\"running\":0,\"pending\":0,\"unknown\":0}", schema))); throw new InvalidOperationException("future/missing schema accepted"); }
            catch (JsonException) { assertions++; }
        }
        var sampled = DateTimeOffset.Parse("2026-09-24T10:00:00Z");
        var recent = sampled.AddSeconds(-1);
        var snapshots = new[] {
            new ExecutionSummarySnapshot(0,0,0,sampled,null), new(1,0,0,sampled,recent), new(0,0,0,sampled,recent),
            new(0,1,0,sampled,recent), new(0,0,0,sampled,recent), new(0,0,2,sampled,recent) };
        var answers = new ConcurrentQueue<ExecutionSummarySnapshot>(snapshots);
        var periods = Channel.CreateUnbounded<bool>();
        var views = Channel.CreateUnbounded<ExecutionSummaryView>();
        var reads = 0;
        using var observer = new ExecutionSummaryObserver(token => {
            Interlocked.Increment(ref reads);
            if (!answers.TryDequeue(out var answer)) throw new JsonException("invalid fresh sample");
            return Task.FromResult(answer);
        }, view => views.Writer.TryWrite(view), async (delay, token) => {
            Check(delay == TimeSpan.FromSeconds(3), "incorrect polling interval");
            await periods.Reader.ReadAsync(token);
        }, () => sampled);
        async Task<ExecutionSummaryView> NextFresh()
        {
            while (true) { var view = await views.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(3)); if (!view.Stale) return view; }
        }
        observer.SetVisible(false); Check(reads == 0, "hidden window polled");
        observer.SetVisible(true); observer.SetVisible(true);
        for (var index = 0; index < snapshots.Length; index++)
        {
            if (index > 0) periods.Writer.TryWrite(true);
            var view = await NextFresh();
            Check(view.Snapshot == snapshots[index] && view.LastSuccess == sampled, "automatic sample sequence mismatch");
        }
        periods.Writer.TryWrite(true);
        var stale = await views.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(3));
        Check(stale.Stale && stale.Snapshot == snapshots[^1] && stale.LastSuccess == sampled, "invalid response fabricated zero or lost last-good timestamp");
        observer.SetVisible(false); await observer.Completion;
        var before = reads;
        Check(before == snapshots.Length + 1, "overlapping or duplicate refresh");
        answers.Enqueue(snapshots[0]); observer.SetVisible(true);
        Check((await NextFresh()).Snapshot == snapshots[0], "show did not refresh immediately");
        observer.SetVisible(false); await observer.Completion;
        Check(reads == before + 1, "hidden observer kept reading");

        // A reader deliberately ignores cancellation. New visibility waits for
        // the previous read to leave, then discards its old result.
        var old = new TaskCompletionSource<ExecutionSummarySnapshot>(TaskCreationOptions.RunContinuationsAsynchronously);
        var replacements = Channel.CreateUnbounded<ExecutionSummaryView>();
        var entered = 0; var active = 0; var maxActive = 0;
        using var reordered = new ExecutionSummaryObserver(async token => {
            var current = Interlocked.Increment(ref active); maxActive = Math.Max(maxActive, current);
            try { return Interlocked.Increment(ref entered) == 1 ? await old.Task : snapshots[1]; }
            finally { Interlocked.Decrement(ref active); }
        }, view => replacements.Writer.TryWrite(view), (_, token) => Task.Delay(Timeout.Infinite, token));
        reordered.SetVisible(true); reordered.SetVisible(false); reordered.SetVisible(true);
        Check(entered == 1, "show overlapped a cancelled outstanding read");
        old.SetResult(snapshots[5]);
        ExecutionSummaryView latest;
        do { latest = await replacements.Reader.ReadAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(3)); } while (latest.Stale);
        Check(latest.Snapshot == snapshots[1] && maxActive == 1, "old generation overwrote new summary");
        reordered.Dispose(); await reordered.Completion;
        Check(active == 0, "closed observer retained an outstanding read");
        Console.WriteLine($"Execution summary: {assertions} assertions passed; automatic 0/1/0, pending, unknown, invalid/stale, hide/show and late-result cancellation. No product UI or runtime started.");
    }
}
