using System.Diagnostics;
using System.IO;
using System.Net;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Threading;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private sealed partial class LocalFixture
    {
        internal bool ExecutionScaleMode { get; set; }
        private const int ScaleObjectCount = 1000;
        private const int ScaleCallCount = 100000;
        private static string ScaleConversation(int index) => "conv_" + (index + 1).ToString("x32");
        private static string ScaleTask(int index) => "tsk_" + (index + 1).ToString("x16");
        private static object ScaleConversationObject(int index) => new { conversation_id = ScaleConversation(index), title = $"对话 {index + 1:D4}", source = "mcp:http", attribution = "host_metadata", state = new { binding_revision = 1 }, updated_at = "2026-09-20T14:00:00Z" };
        private static object ScaleTaskObject(int index) => new { id = ScaleTask(index), title = $"任务 {index + 1:D4}", goal = "大列表分页与搜索夹具", status = "active", summary = "尚未执行的独立任务", steps = Array.Empty<object>(), conditions = new[] { new { text = "分页可用" } }, updated_at = "2026-09-20T14:00:00Z" };
        private static object ScaleCall(int number) => new { call_id = "call_" + number.ToString("x32"), conversation_id = ScaleConversation(0), task_id = ScaleTask(0), thread_id = "main", workspace_id = "wsp_fixture", binding_quality = "host_metadata", created_seq = number, updated_seq = number, created_at = "2026-09-20T14:00:00Z", elapsed_ms = 10, tool_name = "read_file", display_title = $"读取记录 {number:D6}", status = "succeeded", summary = $"记录 {number:D6}", output_preview = "bounded fixture output" };
        private async Task<bool> RespondExecutionScaleAsync(HttpListenerContext context)
        {
            if (!ExecutionScaleMode) return false;
            var path = context.Request.Url!.AbsolutePath;
            var query = context.Request.QueryString;
            var offset = int.TryParse(query["offset"], out var parsedOffset) ? Math.Max(parsedOffset, 0) : 0;
            var limit = int.TryParse(query["limit"], out var parsedLimit) ? Math.Clamp(parsedLimit, 1, 200) : 100;
            var search = query["search"] ?? "";
            if (path is "/internal/runtime/conversations" or "/internal/runtime/execution/tasks")
            {
                var conversation = path.EndsWith("conversations", StringComparison.Ordinal);
                var indexes = Enumerable.Range(0, ScaleObjectCount).Where(index => $"{(conversation ? "对话" : "任务")} {index + 1:D4}".Contains(search, StringComparison.Ordinal)).ToArray();
                var page = indexes.Skip(offset).Take(limit).Select(index => conversation ? ScaleConversationObject(index) : ScaleTaskObject(index)).ToArray();
                var next = Math.Min(indexes.Length, offset + page.Length);
                var response = new Dictionary<string, object> { [conversation ? "conversations" : "tasks"] = page, ["total"] = indexes.Length, ["next_offset"] = next, ["has_more"] = next < indexes.Length, ["selected_ids"] = indexes.Select(index => conversation ? ScaleConversation(index) : ScaleTask(index)).ToArray() };
                await JsonAsync(context, response); return true;
            }
            if (path.StartsWith("/internal/runtime/conversations/conv_", StringComparison.Ordinal))
            {
                var index = Math.Clamp(Convert.ToInt32(path.Split('_')[^1][^4..], 16) - 1, 0, ScaleObjectCount - 1);
                await JsonAsync(context, new { conversation = ScaleConversationObject(index) }); return true;
            }
            if (path.StartsWith("/internal/runtime/tasks/tsk_", StringComparison.Ordinal))
            {
                if (path.EndsWith("/threads", StringComparison.Ordinal)) { await JsonAsync(context, new { threads = Array.Empty<object>() }); return true; }
                if (path.EndsWith("/activity", StringComparison.Ordinal)) { await JsonAsync(context, new { events = Array.Empty<object>(), next_seq = 0, latest_seq = 0, has_more = false, gap = false }); return true; }
                var index = Math.Clamp(Convert.ToInt32(path.Split('/')[^1][^4..], 16) - 1, 0, ScaleObjectCount - 1);
                await JsonAsync(context, new { task = ScaleTaskObject(index) }); return true;
            }
            if (path == "/internal/runtime/calls")
            {
                var before = int.TryParse(query["before"], out var parsedBefore) ? parsedBefore : ScaleCallCount + 1;
                var numbers = Enumerable.Range(1, Math.Clamp(before - 1, 0, ScaleCallCount)).Reverse().Where(number => $"记录 {number:D6}".Contains(search, StringComparison.Ordinal)).Take(limit + 1).ToArray();
                var page = numbers.Take(limit).Select(ScaleCall).ToArray();
                await JsonAsync(context, new { calls = page, latest_seq = ScaleCallCount, next_seq = ScaleCallCount, next_before = numbers.Length == 0 ? 0 : numbers[Math.Min(limit, numbers.Length) - 1], has_more = numbers.Length > limit, gap = false }); return true;
            }
            return false;
        }
    }
    private static object TestExecutionLargeLists(string root)
    {
        SynchronizationContext.SetSynchronizationContext(new DispatcherSynchronizationContext(Dispatcher.CurrentDispatcher));
        Directory.CreateDirectory(root);
        using var fixture = new LocalFixture(root) { ExecutionMode = true, ExecutionScaleMode = true }; fixture.WriteRuntime(root);
        using var runtime = new RuntimeService(root);
        var window = new ExecutionWindow(runtime) { ShowActivated = false, ShowInTaskbar = false, WindowStartupLocation = WindowStartupLocation.Manual, Left = -12000, Top = -12000 };
        var watch = Stopwatch.StartNew(); window.Show();
        long firstScreen; int realizedObjects, realizedCalls;
        try
        {
            PumpUntil(() => window.Objects.Count == 200 && window.Calls.Count == 100 && ((Button)window.FindName("MoreObjectsButton")).IsEnabled, TimeSpan.FromSeconds(15)); firstScreen = watch.ElapsedMilliseconds;
            var objects = (ListBox)window.FindName("ObjectsList"); var calls = (ListBox)window.FindName("CallsList");
            objects.UpdateLayout(); calls.UpdateLayout();
            realizedObjects = Descendants(objects).OfType<ListBoxItem>().Count(); realizedCalls = Descendants(calls).OfType<ListBoxItem>().Count();
            Require(VirtualizingPanel.GetIsVirtualizing(objects) && VirtualizingPanel.GetIsVirtualizing(calls), "Large lists disabled virtualization.");
            Require(realizedObjects < 100 && realizedCalls < 100, "All loaded rows were materialized instead of virtualized.");
            ((Button)window.FindName("MoreObjectsButton")).RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
            PumpUntil(() => window.Objects.Count == 400, TimeSpan.FromSeconds(8));
            var older = Descendants(window).OfType<Button>().Single(button => button.Content?.ToString() == "更早"); older.RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
            PumpUntil(() => window.Calls.Count == 200, TimeSpan.FromSeconds(8));
            Require(window.Calls.Select(call => call.Id).Distinct().Count() == 200, "Call pages duplicate or lose identities.");
            ((TextBox)window.FindName("CallSearchBox")).Text = "099999";
            PumpUntil(() => window.Calls.Count == 1 && window.Calls[0].Title.Contains("099999", StringComparison.Ordinal), TimeSpan.FromSeconds(8));
            ((TextBox)window.FindName("SearchBox")).Text = "1000";
            PumpUntil(() => window.Objects.Count == 1 && window.Objects[0].Title.Contains("1000", StringComparison.Ordinal), TimeSpan.FromSeconds(8));
            objects.UnselectAll(); objects.SelectedIndex = 0;
            Require(typeof(ExecutionWindow).GetField("_frozenSelection", System.Reflection.BindingFlags.NonPublic | System.Reflection.BindingFlags.Instance)!.GetValue(window) is null, "Changing selection retained a stale batch-delete snapshot.");
            var managerTask = (Task)InvokeExecution(window, "OpenDataManagerAsync", false)!;
            PumpUntil(() => managerTask.IsCompleted, TimeSpan.FromSeconds(8)); managerTask.GetAwaiter().GetResult();
            var managed = (ListBox)window.FindName("ManagedObjectsList");
            Require(managed.Items.Count == 200, "Historical task management did not page independently from conversations.");
            ((Button)window.FindName("MoreDataButton")).RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
            PumpUntil(() => managed.Items.Count == 400, TimeSpan.FromSeconds(8));
            Require(window.Objects.All(item => item.Kind == "conversation"), "Opening historical task management polluted conversation navigation.");
            Require(fixture.ControlCount == 0, "Large list navigation modified execution state.");
        }
        finally { window.Close(); }
        PumpUntil(() => fixture.ActiveStreams == 0, TimeSpan.FromSeconds(6));
        return new { passed = true, conversations = 1000, tasks = 1000, calls = 100000, conversation_page_size = 200, call_page_size = 100, loaded_task_pages = 400, loaded_call_pages = 200, first_screen_ms = firstScreen, realized_object_rows = realizedObjects, realized_call_rows = realizedCalls, scope = "Actual WPF window with paginated HTTP fixture; excludes real journal cold-query latency" };
    }
}
