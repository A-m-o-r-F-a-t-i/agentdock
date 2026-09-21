using System.Diagnostics;
using System.IO;
using System.Reflection;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using System.Windows.Threading;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private static async Task TestExecutionParserAsync()
    {
        var json = "{\"call_id\":\"call_a\",\"updated_seq\":7,\"title\":\"中文🙂\"}";
        var reader = new ExecutionSseReader(new StringReader("id: 7\nevent: call\ndata: " + json + "\n\n"));
        var parsed = await reader.ReadEventAsync(CancellationToken.None);
        Require(parsed is not null && parsed.Seq == 7 && parsed.Value.Text("title") == "中文🙂", "Execution SSE parser lost Unicode or cursor.");
        using var first = JsonDocument.Parse("{\"call_id\":\"call_a\",\"created_seq\":1,\"updated_seq\":2,\"status\":\"running\"}");
        using var finished = JsonDocument.Parse("{\"call_id\":\"call_a\",\"created_seq\":1,\"updated_seq\":3,\"status\":\"succeeded\",\"output_preview\":\"保留末尾\",\"stdout_truncated\":true}");
        var row = new ExecutionCallRow(first.RootElement) { IsExpanded = true, FollowOutput = false };
        row.ApplyDetail(finished.RootElement); row.Apply(first.RootElement);
        Require(row.Status == "succeeded" && row.IsExpanded && !row.FollowOutput && row.Output.Contains("截断", StringComparison.Ordinal), "Execution projection regressed state or lost view preferences.");
        foreach (var state in new[] { "loading", "resolved", "unavailable", "deleted", "error" }) { row.SetSource("样例来源", state); Require(row.SourceState == state, "Source state collapsed."); }
        var unknown = new ExecutionCallRow(JsonSerializer.SerializeToElement(new { call_id = "call_unknown", status = "unknown" }));
        Require(!unknown.IsExpanded && !unknown.CanRetry, "Unknown results expanded or became retryable.");
        var deleted = new ExecutionCallRow(JsonSerializer.SerializeToElement(new { call_id = "call_deleted", deleted_at = DateTimeOffset.UtcNow }));
        Require(!deleted.VisibleIn("all"), "Deleted tombstone is visible.");
        var tooLarge = new ExecutionSseReader(new StringReader("data: " + new string('x', ExecutionSseReader.MaximumEventCharacters + 1)));
        try { await tooLarge.ReadEventAsync(CancellationToken.None); throw new InvalidOperationException("Oversized execution event accepted."); } catch (IOException) { }
    }
    private static object? InvokeExecution(ExecutionWindow window, string method, params object?[] args) => typeof(ExecutionWindow).GetMethod(method, BindingFlags.NonPublic | BindingFlags.Instance)!.Invoke(window, args);
    private static void TestExecutionRendering(string root)
    {
        SynchronizationContext.SetSynchronizationContext(new DispatcherSynchronizationContext(Dispatcher.CurrentDispatcher));
        Directory.CreateDirectory(root);
        using var fixture = new LocalFixture(root) { ExecutionMode = true }; fixture.WriteRuntime(root);
        using var runtime = new RuntimeService(root);
        var window = new ExecutionWindow(runtime) { ShowActivated = false, ShowInTaskbar = false, WindowStartupLocation = WindowStartupLocation.Manual, Left = -12000, Top = -12000 };
        var trace = new BindingErrors(); PresentationTraceSources.DataBindingSource.Listeners.Add(trace);
        window.Show();
        try
        {
            PumpUntil(() => window.Objects.Count == 2 && window.Calls.Count == 2 && window.Calls.Any(call => call.UpdatedSeq == 4), TimeSpan.FromSeconds(15));
            ((DispatcherTimer)typeof(ExecutionWindow).GetField("_pulse", BindingFlags.NonPublic | BindingFlags.Instance)!.GetValue(window)!).Stop();
            Require(window.Calls.Select(call => call.Id).Distinct().Count() == 2, "Duplicate SSE updates duplicated execution rows.");
            var reading = window.Calls.Single(call => call.Id == LocalFixture.ReadCall);
            Require(reading.TaskId == "" && reading.Status == "succeeded", "No-task execution vanished or received a fabricated task.");
            Require(((FrameworkElement)window.FindName("ConversationProgressCard")).Visibility == Visibility.Visible, "Current-task progress is missing.");
            Require(((TextBlock)window.FindName("CurrentTaskStatus")).Text == "1/2", "Task progress did not use the selected task state.");
            Require(((TextBlock)window.FindName("CurrentTaskNext")).Text == "验证任务活动窗口", "The compact bar must show the current step, not next-action prose.");
            Require(((FrameworkElement)window.FindName("DetailsPanel")).Visibility == Visibility.Collapsed, "Details opened without a selection.");
            Require(fixture.Cursors.Contains("3"), "Execution stream did not resume from projection cursor.");
            var objects = (ListBox)window.FindName("ObjectsList"); var calls = (ListBox)window.FindName("CallsList");
            Require(window.Objects.All(item => item.Kind == "conversation"), "Tasks became peers of conversations in navigation.");
            var sideText = Descendants(objects).OfType<TextBlock>().Select(item => item.Text).ToArray();
            Require(!sideText.Any(value => value.Contains('●') || value.Contains('□')), "Decorative focus markers returned to sidebar.");
            Require(!Descendants(window).OfType<TextBlock>().Any(item => item.Text == "执行中心"), "Redundant standalone title returned.");
            // Select the actual row; details must be fetched only on demand.
            calls.SelectedItem = reading;
            PumpUntil(() => reading.DetailLoaded && ((FrameworkElement)window.FindName("DetailsPanel")).Visibility == Visibility.Visible, TimeSpan.FromSeconds(5));
            Require(reading.Output.Contains("隔离测试输出", StringComparison.Ordinal), "Call detail output did not load.");
            var tabs = (TabControl)window.FindName("CallDetailsTabs"); tabs.SelectedIndex = 1;
            PumpUntil(() => reading.SourceState == "resolved", TimeSpan.FromSeconds(5));
            Require(!((TextBox)window.FindName("SourceDetailsText")).Text.Contains(window.Objects[0].Title, StringComparison.Ordinal), "Known conversation title repeated in source details.");
            InvokeExecution(window, "CloseDetails");
            // Real WPF row layout is measured with 32 immutable synthetic calls.
            for (var i = 0; i < 30; i++)
            {
                var value = JsonSerializer.SerializeToElement(new { call_id = "call_" + (1000+i).ToString("x32"), conversation_id = LocalFixture.ConversationA, tool_name = "read_file", title = new[] { "读取工作区配置", "检查变更文件", "验证执行结果", "读取任务检查点", "核对构建产物" }[i%5], created_seq = i+10, updated_seq = i+10, status = "succeeded", created_at = "2026-09-21T04:51:00Z", elapsed_ms = 102 });
                InvokeExecution(window, "UpsertCall", value);
            }
            InvokeExecution(window, "ApplyTheme", "system"); window.UpdateLayout();
            InvokeExecution(window, "ApplyTheme", "light");
            CaptureExecution(window, Path.Combine(root, "execution-light-1280x800.png"), 1280, 800, 1);
            Require(((SolidColorBrush)window.FindResource("SidebarBackground")).Color == Color.FromRgb(242,243,244), "Light mode retained a dark sidebar.");
            var viewport = Descendants(calls).OfType<ScrollContentPresenter>().First();
            var fullRows = Descendants(calls).OfType<ListBoxItem>().Count(item => { var rect = item.TransformToAncestor(viewport).TransformBounds(new Rect(new Size(item.ActualWidth, item.ActualHeight))); return rect.Top >= -0.1 && rect.Bottom <= viewport.ActualHeight+0.1; });
            Require(fullRows >= 12, $"1280×800 showed only {fullRows} full rows.");
            Require(Descendants(calls).OfType<ListBoxItem>().All(item => Math.Abs(item.ActualHeight-36)<0.1), "Calls are not one 36-DIP row.");
            InvokeExecution(window, "ApplyTheme", "dark");
            CaptureExecution(window, Path.Combine(root, "execution-dark-1280x800.png"), 1280, 800, 1);
            Require(((SolidColorBrush)window.FindResource("SidebarBackground")).Color == Color.FromRgb(25,26,28), "Dark theme was not applied.");
            foreach (var label in Descendants(objects).OfType<TextBlock>().Where(item => window.Objects.Any(row => item.Text == row.Title || item.Text == row.WorkspaceKey.Title)))
                Require(label.Foreground is SolidColorBrush brush && brush.Color == Color.FromRgb(241,242,243), "Sidebar inherited black system text in dark mode.");
            CaptureExecution(window, Path.Combine(root, "execution-dark-840x640-125.png"), 840, 640, 1.25);
            Require(!window.ShowTimestamps, "Timestamp did not collapse at narrow width.");
            InvokeExecution(window, "ApplyTheme", "light");
            CaptureExecution(window, Path.Combine(root, "execution-light-1000x720-150.png"), 1000, 720, 1.5);
            CaptureExecution(window, Path.Combine(root, "execution-light-1280x800-200.png"), 1280, 800, 2);
            var taskButton = Descendants(window).OfType<Button>().Single(button => button.Content?.ToString() == "任务详情");
            taskButton.RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
            PumpUntil(() => ((ComboBox)window.FindName("BranchCombo")).Items.Count == 2 && ((TextBlock)window.FindName("MilestonesText")).Text.Contains("任务检查点", StringComparison.Ordinal), TimeSpan.FromSeconds(8));
            Require(((TextBlock)window.FindName("TaskAcceptanceText")).Text.Contains("输出与退出码", StringComparison.Ordinal), "Task acceptance did not load from persisted conditions.");
            var branches = (ComboBox)window.FindName("BranchCombo"); var beforeControl = fixture.ControlCount; branches.SelectedIndex = 1;
            PumpUntil(() => fixture.BranchReads > 0 && ((TextBlock)window.FindName("TaskStepsText")).Text == "进度未记录", TimeSpan.FromSeconds(5));
            Require(fixture.ControlCount == beforeControl, "Viewing a branch changed execution continuation.");
            CaptureExecution(window, Path.Combine(root, "execution-task-details.png"), 1280, 800, 1);
            InvokeExecution(window, "CloseDetails");
            objects.SelectedItem = window.Objects[0]; objects.UpdateLayout();
            var second = Descendants(objects).OfType<ListBoxItem>().Single(item => (item.DataContext as ExecutionObject)?.Id == LocalFixture.ConversationB);
            second.RaiseEvent(new MouseButtonEventArgs(Mouse.PrimaryDevice, Environment.TickCount, MouseButton.Right) { RoutedEvent = Mouse.PreviewMouseDownEvent });
            var menuIds = (string[])typeof(ExecutionWindow).GetField("_menuSelection", BindingFlags.NonPublic | BindingFlags.Instance)!.GetValue(window)!;
            Require(menuIds.SequenceEqual([LocalFixture.ConversationB]), "Right-click targeted an unrelated previous selection.");
            if (second.ContextMenu is not null) second.ContextMenu.IsOpen = false;
            PumpUntil(() => ((FrameworkElement)window.FindName("ConversationProgressCard")).Visibility == Visibility.Collapsed && window.Calls.Count == 0, TimeSpan.FromSeconds(5));
            Require(fixture.ControlCount == 0, "Navigation changed execution state.");
            var managerTask = (Task)InvokeExecution(window, "OpenDataManagerAsync", false)!;
            PumpUntil(() => managerTask.IsCompleted, TimeSpan.FromSeconds(5)); managerTask.GetAwaiter().GetResult();
            Require(((ListBox)window.FindName("ManagedObjectsList")).Items.Count == 1, "Historical tasks cannot be managed without a conversation binding.");
            Require(trace.Messages.Count == 0, "Execution UI binding failures:\n" + string.Join("\n", trace.Messages));
            File.WriteAllText(Path.Combine(root, "layout-metrics.json"), JsonSerializer.Serialize(new { passed=true, full_rows_at_1280x800=fullRows, call_height_dip=36, themes=new[]{"light","dark","system"}, details_default_closed=true, task_peers_in_sidebar=false, source_states=5, scope="real WPF window, isolated HTTP and rendering fixtures" }, new JsonSerializerOptions { WriteIndented=true }));
        }
        catch (Exception ex) {
            throw new InvalidOperationException($"Execution render failed: objects={window.Objects.Count}, calls={window.Calls.Count}, warning={((TextBlock)window.FindName("WarningText")).Text}, task={((TextBlock)window.FindName("CurrentTaskStatus")).Text}, requests={fixture.RequestCount}; bindings=" + string.Join("\n",trace.Messages), ex);
        }
        finally { window.Close(); PresentationTraceSources.DataBindingSource.Listeners.Remove(trace); }
        PumpUntil(() => fixture.ActiveStreams == 0, TimeSpan.FromSeconds(6));
        Require(fixture.ControlCount == 0, "Closing the observer stopped a task or command.");
    }
    private static void CaptureExecution(ExecutionWindow window, string path, double width, double height, double scale)
    {
        window.Width = width; window.Height = height; window.UpdateLayout();
        var host = (FrameworkElement)window.Content;
        foreach (var name in new[] { "ObjectsList", "CallsList", "ConversationHeader" })
        {
            var element = (FrameworkElement)window.FindName(name); var bounds = element.TransformToAncestor(host).TransformBounds(new Rect(new Size(element.ActualWidth,element.ActualHeight)));
            Require(bounds.Left >= -1 && bounds.Top >= -1 && bounds.Right <= host.ActualWidth+2 && bounds.Bottom <= host.ActualHeight+2, "Execution layout clipped " + name + " at " + width + "×" + height);
        }
        Require(((ListBox)window.FindName("ObjectsList")).ActualHeight >= 90, "Sidebar height became unusable.");
        var image = new RenderTargetBitmap((int)Math.Ceiling(host.ActualWidth*scale), (int)Math.Ceiling(host.ActualHeight*scale), 96*scale, 96*scale, PixelFormats.Pbgra32); image.Render(host);
        var encoder = new PngBitmapEncoder(); encoder.Frames.Add(BitmapFrame.Create(image)); using var file = File.Create(path); encoder.Save(file);
    }
}
