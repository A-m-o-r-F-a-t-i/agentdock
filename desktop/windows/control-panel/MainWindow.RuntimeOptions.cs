using System.Globalization;
using System.IO;
using System.Text.Json;
using System.Windows;
using Button = System.Windows.Controls.Button;
using MessageBox = System.Windows.MessageBox;
using Forms = System.Windows.Forms;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    private bool _runtimeOptionsLoaded;
    private bool _runtimeOptionsBusy;

    private async void RuntimeOptionsPanel_Loaded(object sender, RoutedEventArgs e)
    {
        if (!_runtimeOptionsLoaded) { await LoadRuntimeOptionsAsync(); }
    }

    private async Task LoadRuntimeOptionsAsync()
    {
        if (_runtimeOptionsBusy) { return; }
        SetRuntimeOptionsBusy(true);
        try
        {
            using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(20));
            RuntimeOptionsStatusText.Text = UiText.Get("Refreshing");
            var view = await _runtime.GetRuntimeOptionsAsync(timeout.Token);
            var options = view.Options;
            DefaultWorkspaceTextBox.Text = options.DefaultDir;
            AgentsAutoLoadCheckBox.IsChecked = options.AgentsAutoLoad;
            ExtraInstructionsTextBox.Text = options.InstructionsFile;
            BrowserExecutableTextBox.Text = options.BrowserExecutablePath;
            TrustedProxyTextBox.Text = string.Join(Environment.NewLine, options.TrustedProxyCidrs ?? []);
            CommandEnvReferencesTextBox.Text = JsonSerializer.Serialize(options.CommandEnvFromEnv ?? [],
                new JsonSerializerOptions { WriteIndented = true });
            AcpConcurrencyTextBox.Text = options.AcpMaxConcurrentPrompts.ToString(CultureInfo.InvariantCulture);
            AcpTimeoutTextBox.Text = options.AcpInteractionTimeoutMs.ToString(CultureInfo.InvariantCulture);
            AgentDockHomeTextBox.Text = view.AgentDockHome;
            RuntimeConfigurationPathsTextBox.Text = view.SettingsPath + Environment.NewLine + view.ManifestPath;
            RuntimeOptionsStatusText.Text = UiText.Get("Ready");
            _runtimeOptionsLoaded = true;
        }
        catch (Exception ex)
        {
            RuntimeOptionsStatusText.Text = ex.Message;
        }
        finally { SetRuntimeOptionsBusy(false); }
    }

    private void SetRuntimeOptionsBusy(bool busy)
    {
        _runtimeOptionsBusy = busy;
        SaveRuntimeOptionsButton.IsEnabled = !busy && _runtimeOptionsLoaded;
        ReloadRuntimeOptionsButton.IsEnabled = !busy;
    }

    private async void ReloadRuntimeOptions_Click(object sender, RoutedEventArgs e) => await LoadRuntimeOptionsAsync();

    private async void SaveRuntimeOptions_Click(object sender, RoutedEventArgs e)
    {
        if (_runtimeOptionsBusy || !_runtimeOptionsLoaded) { return; }
        RuntimeOptions options;
        try
        {
            if (!Path.IsPathFullyQualified(DefaultWorkspaceTextBox.Text.Trim()) ||
                !int.TryParse(AcpConcurrencyTextBox.Text, NumberStyles.Integer, CultureInfo.InvariantCulture, out var concurrency) ||
                !int.TryParse(AcpTimeoutTextBox.Text, NumberStyles.Integer, CultureInfo.InvariantCulture, out var timeout) ||
                concurrency is < 1 or > 8 || timeout is < 1000 or > 3600000)
            {
                throw new InvalidOperationException(UiText.Get("RuntimeOptionsInvalid"));
            }
            var references = JsonSerializer.Deserialize<Dictionary<string, string>>(CommandEnvReferencesTextBox.Text);
            if (references is null || references.Any(pair => pair.Value is null))
            {
                throw new InvalidOperationException(UiText.Get("RuntimeOptionsInvalid"));
            }
            options = new RuntimeOptions
            {
                DefaultDir = DefaultWorkspaceTextBox.Text.Trim(),
                AgentsAutoLoad = AgentsAutoLoadCheckBox.IsChecked == true,
                InstructionsFile = ExtraInstructionsTextBox.Text.Trim(),
                BrowserExecutablePath = BrowserExecutableTextBox.Text.Trim(),
                TrustedProxyCidrs = TrustedProxyTextBox.Text.Split(['\r', '\n', ','], StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries).ToList(),
                CommandEnvFromEnv = references,
                AcpMaxConcurrentPrompts = concurrency,
                AcpInteractionTimeoutMs = timeout
            };
        }
        catch (Exception ex) when (ex is JsonException or InvalidOperationException or ArgumentException)
        {
            MessageBox.Show(this, ex.Message, "AgentDock", MessageBoxButton.OK, MessageBoxImage.Warning);
            return;
        }

        SetRuntimeOptionsBusy(true);
        try
        {
            RuntimeOptionsStatusText.Text = UiText.Get("SavingAndRestarting");
            using var timeout = new CancellationTokenSource(TimeSpan.FromMinutes(3));
            await _runtime.SaveRuntimeOptionsAsync(options, timeout.Token);
            await RefreshAsync();
            RuntimeOptionsStatusText.Text = UiText.Get("OperationCompleted");
        }
        catch (Exception ex)
        {
            RuntimeOptionsStatusText.Text = ex.Message;
            MessageBox.Show(this, ex.Message, "AgentDock", MessageBoxButton.OK, MessageBoxImage.Error);
        }
        finally { SetRuntimeOptionsBusy(false); }
    }

    private void DefaultWorkspaceBrowse_Click(object sender, RoutedEventArgs e)
    {
        using var dialog = new Forms.FolderBrowserDialog
        {
            Description = UiText.Get("DefaultGlobalWorkspace"), UseDescriptionForTitle = true,
            ShowNewFolderButton = true,
            SelectedPath = Directory.Exists(DefaultWorkspaceTextBox.Text) ? DefaultWorkspaceTextBox.Text : ""
        };
        if (dialog.ShowDialog() == Forms.DialogResult.OK) { DefaultWorkspaceTextBox.Text = dialog.SelectedPath; }
    }

    private void RuntimeFileBrowse_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not Button button) { return; }
        var instructions = string.Equals(button.Tag as string, "instructions", StringComparison.Ordinal);
        var dialog = new Microsoft.Win32.OpenFileDialog
        {
            CheckFileExists = true, Multiselect = false,
            Filter = instructions ? "Markdown (*.md)|*.md|All files (*.*)|*.*" : "Executable (*.exe)|*.exe|All files (*.*)|*.*"
        };
        if (dialog.ShowDialog(this) == true)
        {
            if (instructions) { ExtraInstructionsTextBox.Text = dialog.FileName; }
            else { BrowserExecutableTextBox.Text = dialog.FileName; }
        }
    }
}
