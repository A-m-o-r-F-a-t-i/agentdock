using System.Windows;

namespace AgentDock.ControlPanel;

public partial class App
{
    private ExecutionWindow? _activityWindow;
    private CompletionNotificationService? _completionNotifications;
    private void StartCompletionNotifications() => _completionNotifications ??= new(Runtime, async notification =>
    {
        ShowActivityCenter();
        if (_activityWindow is { } window) await window.NavigateCompletionAsync(notification);
    });

    // The dedicated monitor entrypoint does not acquire the tray singleton, start services,
    // run upgrade handoffs, or modify runtime settings. It can observe an isolated runtime.
    private bool TryStartActivityWindow(string[] arguments)
    {
        var legacy = arguments.Contains("--legacy-activity", StringComparer.OrdinalIgnoreCase);
        if (!arguments.Contains("--activity", StringComparer.OrdinalIgnoreCase) && !legacy) return false;
        var index = Array.FindIndex(arguments, argument => string.Equals(argument, "--runtime-root", StringComparison.OrdinalIgnoreCase));
        if (index >= 0 && (index + 1 == arguments.Length || arguments[index + 1].StartsWith("--", StringComparison.Ordinal)))
        {
            System.Windows.MessageBox.Show("--runtime-root requires an explicit directory.", "AgentDock", MessageBoxButton.OK, MessageBoxImage.Error);
            Shutdown(2);
            return true;
        }
        Runtime = new RuntimeService(index >= 0 ? arguments[index + 1] : null);
        ShutdownMode = ShutdownMode.OnMainWindowClose;
        if (legacy) { MainWindow = new ActivityWindow(Runtime); MainWindow.Show(); return true; }
        _activityWindow = new ExecutionWindow(Runtime);
        MainWindow = _activityWindow;
        _activityWindow.Closed += (_, _) => _activityWindow = null;
        _activityWindow.Show();
        StartCompletionNotifications();
        return true;
    }

    public void ShowActivityCenter()
    {
        Dispatcher.Invoke(() =>
        {
            if (_activityWindow is null)
            {
                _activityWindow = new ExecutionWindow(Runtime);
                _activityWindow.Closed += (_, _) => _activityWindow = null;
            }
            if (!_activityWindow.IsVisible) _activityWindow.Show();
            if (_activityWindow.WindowState == WindowState.Minimized) _activityWindow.WindowState = WindowState.Normal;
            _activityWindow.Activate();
        });
    }

    private void ReloadActivityWindowLanguage()
    {
        if (_activityWindow is null) return;
        var previous = _activityWindow;
        var left = previous.Left; var top = previous.Top;
        var width = previous.Width; var height = previous.Height;
        previous.Close();
        ShowActivityCenter();
        if (_activityWindow is not null)
        {
            _activityWindow.Left = left; _activityWindow.Top = top;
            _activityWindow.Width = width; _activityWindow.Height = height;
        }
    }
}
