using System.Windows;
using System.Windows.Controls;
using ComboBox = System.Windows.Controls.ComboBox;

namespace AgentDock.ControlPanel;

public partial class MainWindow
{
    private bool _themeSelectionUpdating;
    private CancellationTokenSource? _displayRequest;
    private McpUiPreference? _mcpUiPreference;
    private bool _displaySaving;

    private async void DisplaySettings_Loaded(object sender, RoutedEventArgs e)
    {
        DesktopTheme.Changed -= MainTheme_Changed;
        DesktopTheme.Changed += MainTheme_Changed;
        _themeSelectionUpdating = true;
        try { ThemePreferenceCombo.SelectedValue = DesktopTheme.Preference; ThemeSaveStatus.Text = DesktopTheme.LoadWarning; }
        finally { _themeSelectionUpdating = false; }
        await LoadMcpUiPreferenceAsync();
    }

    private void DisplaySettings_Unloaded(object sender, RoutedEventArgs e)
    {
        DesktopTheme.Changed -= MainTheme_Changed;
        _displayRequest?.Cancel();
    }

    private void MainTheme_Changed(object? sender, EventArgs e)
    {
        _themeSelectionUpdating = true;
        try { ThemePreferenceCombo.SelectedValue = DesktopTheme.Preference; }
        finally { _themeSelectionUpdating = false; }
    }

    private async Task LoadMcpUiPreferenceAsync()
    {
        if (_displaySaving) return;
        _displayRequest?.Cancel();
        using var request = new CancellationTokenSource(TimeSpan.FromSeconds(12));
        _displayRequest = request;
        McpUiEnabledChoice.IsEnabled = false;
        try
        {
            var value = await new DisplayPreferenceService(_runtime).ReadAsync(request.Token);
            _mcpUiPreference = value;
            McpUiEnabledChoice.IsChecked = value.Enabled;
            McpUiSaveStatus.Text = value.Warning;
            McpUiEnabledChoice.IsEnabled = value.Warning.Length == 0;
        }
        catch (OperationCanceledException) { McpUiSaveStatus.Text = UiText.Get("DisplayReadCancelled"); }
        catch (Exception error) when (error is System.IO.IOException or System.Net.Http.HttpRequestException or System.Text.Json.JsonException or InvalidOperationException)
        { McpUiSaveStatus.Text = UiText.Format("DisplayUnavailable", error.Message); }
        finally { if (ReferenceEquals(_displayRequest, request)) _displayRequest = null; }
    }

    private async void McpUiPreference_Click(object sender, RoutedEventArgs e)
    {
        if (_mcpUiPreference is null || _displaySaving) return;
        var previous = _mcpUiPreference;
        var enabled = McpUiEnabledChoice.IsChecked == true;
        _displaySaving = true;
        McpUiEnabledChoice.IsEnabled = false;
        using var request = new CancellationTokenSource(TimeSpan.FromSeconds(12));
        _displayRequest = request;
        try
        {
            var saved = await new DisplayPreferenceService(_runtime).SaveAsync(enabled, previous.Revision, request.Token);
            _mcpUiPreference = saved;
            McpUiEnabledChoice.IsChecked = saved.Enabled;
            McpUiSaveStatus.Text = UiText.Get("DisplaySaved") + saved.RefreshHint;
        }
        catch (Exception error) when (error is OperationCanceledException or System.IO.IOException or System.Net.Http.HttpRequestException or System.Text.Json.JsonException or InvalidOperationException)
        {
            McpUiEnabledChoice.IsChecked = previous.Enabled;
            McpUiSaveStatus.Text = UiText.Format("DisplaySaveUnconfirmed", error.Message);
            try
            {
                using var confirm = new CancellationTokenSource(TimeSpan.FromSeconds(8));
                var current = await new DisplayPreferenceService(_runtime).ReadAsync(confirm.Token);
                _mcpUiPreference = current;
                McpUiEnabledChoice.IsChecked = current.Enabled;
                McpUiSaveStatus.Text = UiText.Get("DisplayReloaded") + current.RefreshHint;
            }
            catch (Exception readError) when (readError is OperationCanceledException or System.IO.IOException or System.Net.Http.HttpRequestException or System.Text.Json.JsonException or InvalidOperationException)
            { McpUiSaveStatus.Text = UiText.Get("DisplayConfirmUnavailable"); }
        }
        finally
        {
            _displaySaving = false;
            if (ReferenceEquals(_displayRequest, request)) _displayRequest = null;
            McpUiEnabledChoice.IsEnabled = _mcpUiPreference?.Warning.Length == 0;
        }
    }

    private void ThemePreference_Changed(object sender, SelectionChangedEventArgs e)
    {
        if (_themeSelectionUpdating || sender is not ComboBox { IsLoaded: true } combo || combo.SelectedValue is not string choice) return;
        try { DesktopTheme.Save(choice); ThemeSaveStatus.Text = UiText.Get("SettingsSaved"); }
        catch (Exception error) when (error is System.IO.IOException or UnauthorizedAccessException or System.Text.Json.JsonException or InvalidOperationException)
        {
            ThemeSaveStatus.Text = UiText.Format("ThemeSaveFailed", error.Message);
            _themeSelectionUpdating = true;
            try { combo.SelectedValue = DesktopTheme.Preference; }
            finally { _themeSelectionUpdating = false; }
        }
    }
}
