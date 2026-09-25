using System.Diagnostics;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Threading;
using Button = System.Windows.Controls.Button;

namespace AgentDock.ControlPanel;

internal sealed class TaskNotificationWindow : Window
{
    private readonly DispatcherTimer _timeout = new() { Interval = TimeSpan.FromSeconds(8) };
    internal TaskNotificationWindow(TaskNotification notification, Func<TaskNotification, Task> navigate)
    {
        Title = "AgentDock"; Width = 360; Height = 116;
        WindowStyle = WindowStyle.None; ResizeMode = ResizeMode.NoResize;
        ShowInTaskbar = false; ShowActivated = false; Topmost = true;
        FontFamily = new System.Windows.Media.FontFamily("Segoe UI, Microsoft YaHei UI"); FontSize = 14;
        SetResourceReference(BackgroundProperty, "PanelBackground");
        SetResourceReference(ForegroundProperty, "PrimaryText");
        var frame = new ThemeBorder { BorderResource = "SeparatorBrush", BackgroundResource = "PanelBackground", BorderThickness = new Thickness(1), Padding = new Thickness(14, 10, 10, 12) };
        var layout = new Grid();
        layout.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        layout.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var heading = new DockPanel();
        var close = new Button { Content = UiText.Get("ExecutionClose"), FontSize = 12, Padding = new Thickness(8, 2, 8, 2), MinHeight = 26 };
        DockPanel.SetDock(close, Dock.Right); close.Click += (_, _) => Close(); heading.Children.Add(close);
        heading.Children.Add(new ThemeTextBlock { Text = UiText.Get("ExecutionCompleted"), ForegroundResource = "SecondaryText", VerticalAlignment = VerticalAlignment.Center });
        layout.Children.Add(heading);
        var open = new Button { Content = new TextBlock { Text = notification.Title, TextTrimming = TextTrimming.CharacterEllipsis, FontWeight = FontWeights.SemiBold },
            HorizontalContentAlignment = System.Windows.HorizontalAlignment.Left, Margin = new Thickness(0, 8, 0, 0), BorderThickness = new Thickness(0), ToolTip = notification.Title };
        Grid.SetRow(open, 1); layout.Children.Add(open);
        open.Click += async (_, _) =>
        {
            Close();
            try { await navigate(notification); }
            catch (Exception ex) { Debug.WriteLine("Task notification navigation: " + ex.Message); }
        };
        frame.Child = layout; Content = frame;
        _timeout.Tick += (_, _) => Close();
        Loaded += (_, _) => _timeout.Start();
        MouseEnter += (_, _) => _timeout.Stop();
        MouseLeave += (_, _) => _timeout.Start();
        Closed += (_, _) => _timeout.Stop();
    }
}
