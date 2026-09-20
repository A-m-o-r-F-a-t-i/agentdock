using System.Diagnostics;
using System.Globalization;
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
        var tooLarge = new ExecutionSseReader(new StringReader("data: " + new string('x', ExecutionSseReader.MaximumEventCharacters + 1)));
        try { await tooLarge.ReadEventAsync(CancellationToken.None); throw new InvalidOperationException("Oversized execution event accepted."); } catch (IOException) { }
    }
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
            PumpUntil(() => window.Objects.Count == 2 && window.Calls.Count == 2 && window.Calls.Any(call => call.UpdatedSeq == 4), TimeSpan.FromSeconds(12));
            Require(window.Calls.Select(call => call.Id).Distinct().Count() == 2, "Duplicate SSE updates duplicated execution cards.");
            var reading = window.Calls.Single(call => call.Id == LocalFixture.ReadCall);
            Require(reading.TaskId == "" && reading.Status == "succeeded", "No-task execution vanished or received a fabricated task.");
            Require(((FrameworkElement)window.FindName("ConversationProgressCard")).Visibility == Visibility.Visible, "Conversation current-task progress card is missing.");
            Require(((TextBlock)window.FindName("CurrentTaskStatus")).Text.Contains("1/2",StringComparison.Ordinal), "Conversation task progress count was not read from task state.");
            Require(((TextBlock)window.FindName("CurrentTaskNext")).Text.Contains("核对中文",StringComparison.Ordinal), "Conversation next action is missing.");
            Require(((TextBlock)window.FindName("MilestonesText")).Text.Contains("任务检查点",StringComparison.Ordinal), "Task milestone was not separately presented.");
            Require(fixture.Cursors.Contains("3"), "Execution stream did not resume after the initial projection cursor.");
            CaptureExecution(window, Path.Combine(root, "execution-1220x840-100.png"), 1220, 840, 1);
            CaptureExecution(window, Path.Combine(root, "execution-840x640-125.png"), 840, 640, 1.25);
            CaptureExecution(window, Path.Combine(root, "execution-1000x720-150.png"), 1000, 720, 1.5);
            CaptureExecution(window, Path.Combine(root, "execution-1220x840-200.png"), 1220, 840, 2);
            var objects = (ListBox)window.FindName("ObjectsList");
            objects.SelectedItem = window.Objects[0]; objects.UpdateLayout();
            var second = (ListBoxItem)objects.ItemContainerGenerator.ContainerFromIndex(1);
            Require(second is not null, "Second conversation was not rendered.");
            second!.RaiseEvent(new MouseButtonEventArgs(Mouse.PrimaryDevice, Environment.TickCount, MouseButton.Right) { RoutedEvent = Mouse.PreviewMouseDownEvent });
            var menuIds = (string[])typeof(ExecutionWindow).GetField("_menuSelection", BindingFlags.NonPublic | BindingFlags.Instance)!.GetValue(window)!;
            Require(menuIds.SequenceEqual([LocalFixture.ConversationB]), "Right click targeted the previously selected conversation.");
            PumpUntil(() => ((FrameworkElement)window.FindName("ConversationProgressCard")).Visibility == Visibility.Collapsed && window.Calls.Count == 0, TimeSpan.FromSeconds(5));
            Require(fixture.ControlCount == 0, "Conversation selection silently changed execution continuation.");
            var taskNav = Descendants(window).OfType<Button>().Single(button => button.Tag?.ToString() == "task" && button.Content?.ToString()?.Contains("任务", StringComparison.Ordinal) == true);
            taskNav.RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
            PumpUntil(() => window.Objects.Count == 1 && window.Objects[0].Kind == "task" && ((ComboBox)window.FindName("BranchCombo")).Items.Count==3, TimeSpan.FromSeconds(10));
            var acceptance=((TextBlock)window.FindName("TaskAcceptanceText")).Text;
            Require(!string.IsNullOrWhiteSpace(acceptance) && !acceptance.StartsWith("任务尚未",StringComparison.Ordinal),"Task acceptance did not load: "+acceptance+" Warning: "+((TextBlock)window.FindName("WarningText")).Text);
            var branches = (ComboBox)window.FindName("BranchCombo"); Require(branches.Items.Count == 3, "Task branches are not independently selectable.");
            var beforeControl = fixture.ControlCount; branches.SelectedIndex = 2;
            PumpUntil(() => fixture.BranchReads>0 && ((TextBlock)window.FindName("TaskStepsText")).Text.StartsWith("尚未设置",StringComparison.Ordinal), TimeSpan.FromSeconds(5));
            Require(fixture.ControlCount == beforeControl, "Viewing a branch changed the actual continuation branch.");
            CaptureExecution(window, Path.Combine(root, "execution-task-1220x840.png"), 1220, 840, 1);
            Require(trace.Messages.Count == 0, "Execution UI binding failures:\n" + string.Join("\n", trace.Messages));
        }
        finally { window.Close(); PresentationTraceSources.DataBindingSource.Listeners.Remove(trace); }
        PumpUntil(() => fixture.ActiveStreams == 0, TimeSpan.FromSeconds(6));
        Require(fixture.ControlCount == 0, "Closing the execution center issued a process stop or task-control request.");
    }
    private static void CaptureExecution(ExecutionWindow window, string path, double width, double height, double scale)
    {
        window.Width = width; window.Height = height; window.UpdateLayout();
        var content = window.Content; window.Content = null;
        var host = new Border { Resources=window.Resources,Background = window.Background, DataContext = window.DataContext, Child = (UIElement)content, Width = width - 18, Height = height - 42 };
        System.Windows.Documents.TextElement.SetFontFamily(host,window.FontFamily);
        System.Windows.Documents.TextElement.SetFontSize(host,window.FontSize);
        System.Windows.Documents.TextElement.SetForeground(host,window.Foreground);
        try
        {
            host.Measure(new Size(width - 18, height - 42)); host.Arrange(new Rect(0, 0, width - 18, height - 42)); host.UpdateLayout();
            foreach (var name in new[] { "ObjectsList", "CallsList", "OverviewText", "ConnectionText" })
            {
                var element = (FrameworkElement)window.FindName(name); var bounds = element.TransformToAncestor(host).TransformBounds(new Rect(new Size(element.ActualWidth, element.ActualHeight)));
                Require(bounds.Left >= -1 && bounds.Top >= -1 && bounds.Right <= host.ActualWidth + 2 && bounds.Bottom <= host.ActualHeight + 2, "Execution layout clipped " + name + " at " + width + "×" + height);
            }
            Require(((ListBox)window.FindName("ObjectsList")).ActualHeight>=90,"Narrow layout left less than 90px for the object list.");
            var image = new RenderTargetBitmap((int)Math.Ceiling(host.ActualWidth * scale), (int)Math.Ceiling(host.ActualHeight * scale), 96 * scale, 96 * scale, PixelFormats.Pbgra32); image.Render(host);
            var encoder = new PngBitmapEncoder(); encoder.Frames.Add(BitmapFrame.Create(image)); using var file = File.Create(path); encoder.Save(file);
        }
        finally { host.Child = null; window.Content = content; window.UpdateLayout(); }
    }
}
