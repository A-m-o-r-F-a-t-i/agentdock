using System.Windows;

namespace AgentDock.ControlPanel;

public partial class App
{
    private ActivityWindow? _activityWindow;

    // The dedicated monitor entrypoint does not acquire the tray singleton, start services,
    // run upgrade handoffs, or modify runtime settings. It can observe an isolated runtime.
    private bool TryStartActivityWindow(string[] arguments)
    {
        if (!arguments.Contains("--activity", StringComparer.OrdinalIgnoreCase)) return false;
        var index = Array.FindIndex(arguments, argument => string.Equals(argument, "--runtime-root", StringComparison.OrdinalIgnoreCase));
        if (index >= 0 && (index + 1 == arguments.Length || arguments[index + 1].StartsWith("--", StringComparison.Ordinal)))
        {
            System.Windows.MessageBox.Show("--runtime-root requires an explicit directory.", "AgentDock", MessageBoxButton.OK, MessageBoxImage.Error);
            Shutdown(2);
            return true;
        }
        Runtime = new RuntimeService(index >= 0 ? arguments[index + 1] : null);
        ShutdownMode = ShutdownMode.OnMainWindowClose;
        _activityWindow = new ActivityWindow(Runtime);
        MainWindow = _activityWindow;
        _activityWindow.Closed += (_, _) => _activityWindow = null;
        _activityWindow.Show();
        return true;
    }

    public void ShowActivityCenter()
    {
        Dispatcher.Invoke(() =>
        {
            if (_activityWindow is null)
            {
                _activityWindow = new ActivityWindow(Runtime);
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
