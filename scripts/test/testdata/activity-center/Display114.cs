using System.IO;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private static void AssertDisplay114(ExecutionWindow window, string root)
    {
        var now = new DateTimeOffset(2026, 9, 21, 14, 0, 0, TimeSpan.Zero);
        Require(ConversationActivityClock.IsRecent(now, now, false), "A current request was not active.");
        Require(ConversationActivityClock.IsRecent(now, now.AddMilliseconds(29900), false), "Activity ended before 30 seconds.");
        Require(!ConversationActivityClock.IsRecent(now, now.AddSeconds(30), false), "Activity included the 30-second endpoint.");
        Require(!ConversationActivityClock.IsRecent(now, now.AddTicks(-1), false), "Future timestamps invented activity.");
        Require(!ConversationActivityClock.IsRecent(now, now, true), "Terminated conversations remained active.");
        Require(!ConversationActivityClock.IsRecent(null, now, false), "Missing timestamps became zero-age activity.");
        var legacy = new ExecutionCallRow(JsonSerializer.SerializeToElement(new { call_id = "call_legacy", tool_name = "read_file", status = "succeeded" }));
        Require(legacy.Duration == "未记录" && legacy.ExecutionDuration == "未记录", "Legacy missing timing was fabricated as zero.");
        var measured = new ExecutionCallRow(JsonSerializer.SerializeToElement(new
        {
            call_id = "call_file", tool_name = "file_edit", status = "succeeded", rpc_elapsed_ms = 0,
            execution_elapsed_ms = 0, wait_elapsed_ms = 0, process_elapsed_ms = 9000,
            file_edit = new { action = "patch", dry_run = true, executed = true, changed = false, affected_count = 2,
                affected_files = new[] { new { path = "a.txt" }, new { path = "b.txt" } } }
        }));
        Require(measured.Duration == "0.000 s" && measured.ActualTool.Contains("EDIT_FILE"), "Measured zero or canonical edit alias was lost.");
        Require(measured.TimingDetails.Contains("9.000 s") && measured.FileEditDetails.Contains("未写入") && measured.FileEditDetails.Contains("b.txt"), "Process lifetime or multi-file preview was lost.");
        Require(Descendants((ListBox)window.FindName("ObjectsList")).OfType<TextBlock>().Count(text => text.Text == "对话") >= 2, "Conversation labels were not rendered.");
        Require(Descendants((FrameworkElement)window.FindName("ConversationProgressCard")).OfType<TextBlock>().Any(text => text.Text == "任务"), "Task semantic label was not rendered.");

        var calls = (ListBox)window.FindName("CallsList");
        var before = window.Calls.Select(call => call.Id).ToArray();
        var detailed = (RadioButton)window.FindName("DetailedCallsChoice");
        detailed.IsChecked = true;
        window.UpdateLayout();
        Require(((FrameworkElement)window.FindName("DetailedCallsHeader")).Visibility == Visibility.Visible, "Detailed column header did not appear.");
        Require(window.Calls.Select(call => call.Id).SequenceEqual(before), "Presentation switching changed calls or their identities.");
        using (var preferences = JsonDocument.Parse(File.ReadAllText(Path.Combine(root, "execution-center-settings.json"))))
            Require(preferences.RootElement.GetProperty("detailed_calls").GetBoolean(), "Detailed preference did not persist.");
        Require(Descendants(calls).OfType<TextBlock>().Any(text => text.Text == "read_file"), "Detailed rows conceal the actual tool name.");
        CaptureExecution(window, Path.Combine(root, "execution-detailed-1280x800.png"), 1280, 800, 1);
        ((RadioButton)window.FindName("CompactCallsChoice")).IsChecked = true;
        Require(((FrameworkElement)window.FindName("DetailedCallsHeader")).Visibility == Visibility.Collapsed, "Compact view retained the extra header.");
        var button = new Button { Content = "默认按钮" };
        button.SetResourceReference(FrameworkElement.StyleProperty, typeof(Button));
        button.ApplyTemplate();
        Require(button.BorderThickness.Left == 1 && button.BorderBrush is SolidColorBrush border && border.Color.A > 0, "Default button boundary is invisible.");

        var selected = new MenuItem { Header = "当前对话", IsCheckable = true, IsChecked = true };
        selected.SetResourceReference(FrameworkElement.StyleProperty, typeof(MenuItem));
        selected.ApplyTemplate();
        Require(selected.Template.FindName("Check", selected) is TextBlock { Visibility: Visibility.Visible }, "Checked menu state depends on hover.");

        DesktopTheme.Save("dark");
        Require(window.Background is SolidColorBrush dark && dark.Color == ((SolidColorBrush)window.FindResource("AppBackground")).Color, "Execution window did not follow the global theme.");
        using var runtime = new RuntimeService(root);
        var main = new MainWindow(runtime);
        Require(main.Background is SolidColorBrush mainBackground && mainBackground.Color == ((SolidColorBrush)window.FindResource("AppBackground")).Color, "Main window and execution window use different themes.");
        main.Close();
        DesktopTheme.Save("light");
        File.WriteAllText(Path.Combine(root, "display-114-results.json"), JsonSerializer.Serialize(new { passed = true, checks = new[] { "nullable_timing", "rpc_process_split", "edit_preview", "activity_half_open_window", "labels", "detailed_mode_persistence", "checked_menu", "default_border", "shared_theme" } }));
    }
}
