using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Reflection;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using System.Windows.Threading;
using System.Xml.Linq;
using System.Windows.Markup;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private static void TestRendering(string root)
    {
        Directory.CreateDirectory(root);
        using var fixture = new LocalFixture(root);
        fixture.WriteRuntime(root);
        var app = new Application { ShutdownMode = ShutdownMode.OnExplicitShutdown };
        using (var styles = typeof(Program).Assembly.GetManifestResourceStream("ActualAppStyles.xaml")!)
        {
            var source = XDocument.Load(styles).Root!;
            XNamespace presentation = "http://schemas.microsoft.com/winfx/2006/xaml/presentation";
            var dictionary = new XElement(presentation + "ResourceDictionary", source.Attributes().Where(attribute => attribute.IsNamespaceDeclaration), source.Element(presentation + "Application.Resources")!.Elements());
            app.Resources = (ResourceDictionary)XamlReader.Parse(dictionary.ToString());
        }
        var trace = new BindingErrors();
        PresentationTraceSources.DataBindingSource.Listeners.Add(trace);
        PresentationTraceSources.DataBindingSource.Switch.Level = SourceLevels.Error;
        using var runtime = new RuntimeService(root);
        CultureInfo.CurrentUICulture = CultureInfo.GetCultureInfo("zh-CN");
        CultureInfo.DefaultThreadCurrentUICulture = CultureInfo.CurrentUICulture;
        var window = new ActivityWindow(runtime) { ShowActivated = false, ShowInTaskbar = false, WindowStartupLocation = WindowStartupLocation.Manual, Left = -12000, Top = -12000 };
        window.Show();
        var timeline = (ActivityTimeline)typeof(ActivityWindow).GetField("_timeline", BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(window)!;
        var threads = (ComboBox)window.FindName("ThreadSelector");
        try { PumpUntil(() => threads.Items.Count == 2 && timeline.Rows.Count > 0, TimeSpan.FromSeconds(10)); }
        catch (Exception ex)
        {
            var state = $"threads={threads.Items.Count}; rows={timeline.Rows.Count}; requests={fixture.RequestCount}; streams={fixture.ActiveStreams}; status={((TextBlock)window.FindName("ConnectionStatus")).Text}; title={((TextBlock)window.FindName("TaskTitle")).Text}; bindings={string.Join(" | ", trace.Messages)}";
            window.Close();
            throw new InvalidOperationException(state, ex);
        }
        Require(((TextBlock)window.FindName("TaskTitle")).Text.Contains("1.1.0"), "Window did not load the selected task.");
        Require(((TextBox)window.FindName("ThreadInfo")).Text.Contains("下一动作"), "Chinese checkpoint labels were not rendered.");
        var streamCancellation = (CancellationTokenSource)typeof(ActivityWindow).GetField("_streamCancellation", BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(window)!;
        var firstStreamToken = streamCancellation.Token;
        threads.SelectedIndex = 1;
        Require(firstStreamToken.IsCancellationRequested && timeline.Rows.Count == 0, "Thread switch did not cancel the previous subscription and clear rows.");
        threads.SelectedIndex = 0;
        PopulateTimeline(timeline, root);
        var list = (ListBox)window.FindName("TimelineList");
        list.SelectedIndex = 0;
        window.UpdateLayout();
        Capture(window, root, "activity-zh-1180x800-100.png", 1180, 800, 1.0);
        Capture(window, root, "activity-zh-840x560-125.png", 840, 560, 1.25);
        var timer = Stopwatch.StartNew();
        for (ulong sequence = 200; sequence < 4200; sequence++) timeline.Apply(Sample(sequence, "tool.completed", ""));
        window.UpdateLayout();
        Require(timeline.Rows.Count == ActivityTimeline.MaxRows, "Rendered timeline exceeded its row bound.");
        var realized = Descendants(list).OfType<ListBoxItem>().Count();
        Require(realized < 100, $"Timeline virtualization failed: {realized} realized containers.");
        File.WriteAllText(Path.Combine(root, "layout-metrics.json"), System.Text.Json.JsonSerializer.Serialize(new { visible_rows = timeline.Rows.Count, realized_containers = realized, burst_model_layout_ms = timer.ElapsedMilliseconds }));
        var cancellation = ((CancellationTokenSource)typeof(ActivityWindow).GetField("_streamCancellation", BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(window)!).Token;
        window.Close();
        Require(cancellation.IsCancellationRequested, "Closing the activity window retained its subscription.");
        PumpUntil(() => fixture.ActiveStreams == 0, TimeSpan.FromSeconds(4));

        CultureInfo.CurrentUICulture = CultureInfo.GetCultureInfo("en-US");
        CultureInfo.DefaultThreadCurrentUICulture = CultureInfo.CurrentUICulture;
        var english = new ActivityWindow(runtime) { ShowActivated = false, ShowInTaskbar = false, WindowStartupLocation = WindowStartupLocation.Manual, Left = -12000, Top = -12000 };
        english.Show();
        var englishTimeline = (ActivityTimeline)typeof(ActivityWindow).GetField("_timeline", BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(english)!;
        PumpUntil(() => ((ComboBox)english.FindName("ThreadSelector")).Items.Count == 2 && englishTimeline.Rows.Count > 0, TimeSpan.FromSeconds(10));
        Require(english.Title.Contains("Task activity", StringComparison.Ordinal), "English window title was not localized.");
        PopulateTimeline(englishTimeline, root);
        Capture(english, root, "activity-en-1000x700-150.png", 1000, 700, 1.5);
        english.Close();
        PumpUntil(() => fixture.ActiveStreams == 0, TimeSpan.FromSeconds(4));
        Require(trace.Messages.Count == 0, "WPF binding errors: " + string.Join("\n", trace.Messages));
        PresentationTraceSources.DataBindingSource.Listeners.Remove(trace);
    }

    private static void PopulateTimeline(ActivityTimeline timeline, string root)
    {
        timeline.Reset();
        var started = Sample(100, "command.started"); started.Workdir = root;
        timeline.Apply(started);
        var output = Sample(101); output.Workdir = root; output.OutputPreview = "ok  internal/activity\nok  internal/taskstate\nok  internal/httpx\n"; output.ElapsedMs = 850;
        timeline.Apply(output);
        var completed = Sample(102, "command.completed"); completed.Workdir = root; completed.Status = "success"; completed.ExitCode = 0; completed.CommandOk = true; completed.ElapsedMs = 1250;
        timeline.Apply(completed);
        timeline.Rows[0].IsExpanded = true;
        var file = Sample(103, "file.changed", ""); file.Title = "更新任务活动窗口 / Update ActivityWindow"; file.DisplayCommand = ""; file.Status = "success";
        file.ResolvedPath = Path.Combine(root, "ActivityWindow.fixture.txt"); file.Insertions = 18; file.Deletions = 4; file.ChangeStatsKnown = true; file.Summary = "隔离测试数据，用于验证路径和差异摘要的展示。";
        File.WriteAllText(file.ResolvedPath, "isolated rendering fixture\n"); timeline.Apply(file);
        var checkpoint = Sample(104, "step.summary", ""); checkpoint.Title = "阶段检查点 / Checkpoint"; checkpoint.DisplayCommand = ""; checkpoint.Status = "open";
        checkpoint.Summary = "后端测试通过，正在核对窗口布局、重连和输出截断。"; timeline.Apply(checkpoint);
        var running = Sample(105, "command.started", "session_running"); running.Title = "长命令 / Long-running command"; running.DisplayCommand = "go test ./..."; timeline.Apply(running);
    }

    private static void Capture(ActivityWindow window, string root, string name, double width, double height, double scale)
    {
        // Hosted desktops can clamp HWNDs to a small virtual screen. Detach the real
        // content only for synchronous offscreen layout; keep its margins and bindings.
        // The live-window checks still exercise subscriptions and the full lifetime.
        var content = (FrameworkElement)window.Content;
        var controls = new[] { "TaskList", "ThreadSelector", "TimelineList", "StopButton", "DiffButton", "ConnectionStatus" }
            .ToDictionary(controlName => controlName, controlName => (FrameworkElement)window.FindName(controlName));
        window.Content = null;
        var surface = new Border { Background = window.Background, Child = content };
        try
        {
            surface.Measure(new Size(width, height));
            surface.Arrange(new Rect(0, 0, width, height));
            surface.UpdateLayout();
            Require(Math.Abs(surface.ActualWidth - width) < 2 && Math.Abs(surface.ActualHeight - height) < 2,
                $"WPF surface did not adopt the requested size: {surface.ActualWidth}x{surface.ActualHeight}.");
            foreach (var (controlName, element) in controls)
            {
                var bounds = element.TransformToAncestor(surface).TransformBounds(new Rect(0, 0, element.ActualWidth, element.ActualHeight));
                Require(bounds.Width > 0 && bounds.Height > 0 && bounds.Left >= 0 && bounds.Top >= 0 && bounds.Right <= width + 1 && bounds.Bottom <= height + 1,
                    $"Control {controlName} is clipped at {width}x{height}: {bounds}.");
            }
            var image = new RenderTargetBitmap((int)Math.Ceiling(width * scale), (int)Math.Ceiling(height * scale), 96 * scale, 96 * scale, PixelFormats.Pbgra32);
            image.Render(surface);
            var encoder = new PngBitmapEncoder(); encoder.Frames.Add(BitmapFrame.Create(image));
            using var output = File.Create(Path.Combine(root, name)); encoder.Save(output);
        }
        finally
        {
            surface.Child = null;
            window.Content = content;
            window.UpdateLayout();
        }
    }

    private static IEnumerable<DependencyObject> Descendants(DependencyObject root)
    {
        for (var index = 0; index < VisualTreeHelper.GetChildrenCount(root); index++)
        {
            var child = VisualTreeHelper.GetChild(root, index); yield return child;
            foreach (var descendant in Descendants(child)) yield return descendant;
        }
    }

    private static void PumpUntil(Func<bool> condition, TimeSpan timeout)
    {
        if (condition()) return;
        var frame = new DispatcherFrame();
        var started = Stopwatch.StartNew();
        var timer = new DispatcherTimer { Interval = TimeSpan.FromMilliseconds(20) };
        timer.Tick += (_, _) => { if (condition() || started.Elapsed > timeout) { timer.Stop(); frame.Continue = false; } };
        timer.Start(); Dispatcher.PushFrame(frame); timer.Stop();
        Require(condition(), "Timed out waiting for isolated WPF activity state.");
    }

    private sealed class BindingErrors : TraceListener
    {
        public List<string> Messages { get; } = [];
        public override void Write(string? message) { if (!string.IsNullOrWhiteSpace(message) && Messages.Count < 10) Messages.Add(message); }
        public override void WriteLine(string? message) => Write(message);
    }
}
