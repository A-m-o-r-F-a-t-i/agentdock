using System.IO;
using System.Net.Http;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private McpUiPreference? _outputPreference;
    private async Task OpenDisplayPreferencesAsync()
    {
        var service = new DisplayPreferenceService(_runtime);
        var display = await service.ReadAsync(_lifetime.Token);
        _outputPreference = display;
        if (!ExecutionDialogs.Preferences(this, _preferences, display, async output =>
        {
            using var save = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
            save.CancelAfter(TimeSpan.FromSeconds(12));
            _outputPreference = await service.SaveOutputAsync(output, display.Revision, save.Token);
        })) return;
        FontSize = _preferences.FontSize; SavePreferences();
        if (_detailCall is { } row)
        {
            await LoadPayloadPageAsync(row, "request", false, automatic: true);
            await LoadPayloadPageAsync(row, "response", false, automatic: true);
        }
    }

    private async Task LoadPayloadPageAsync(ExecutionCallRow row, string kind, bool previous, bool automatic = false)
    {
        var payload = kind == "request" ? row.RequestPayload : row.ResponsePayload;
        if (payload.Reference.Length == 0 || payload.Busy) return;
        var needsPolicyReload = _outputPreference is not null && payload.NeedsBudgetReload(_outputPreference.ToolOutput.Budget);
        if (automatic && !needsPolicyReload && (!payload.NeedsLoad || payload.HasReadError)) return;
        var reference = payload.Reference;
        var box = kind == "request" ? RequestPayloadText : ResponsePayloadText;
        var scroll = box.VerticalOffset;
        var start = box.SelectionStart; var length = box.SelectionLength;
        var generation = _generation;
        payload.SetBusy(true);
        long policyRevision = 0;
        try
        {
            var display = await new DisplayPreferenceService(_runtime).ReadAsync(SelectionToken);
            if (_outputPreference is null || display.Revision >= _outputPreference.Revision) _outputPreference = display;
            var budget = _outputPreference.ToolOutput.Budget; policyRevision = _outputPreference.Revision;
            if (_closed || generation != _generation || !ReferenceEquals(_detailCall, row)) return;
            var reset = payload.NeedsBudgetReload(budget);
            var offset = automatic || reset ? 0 : payload.RequestedOffset(previous);
            var storageKind = kind == "request" ? "request" : row.ResponsePayloadKind;
            var page = await _client.ExecutionGetAsync("/internal/runtime/calls/" + Escape(row.Id) + "/payload/" + storageKind + "?offset=" + offset + "&" + budget.Query, SelectionToken);
            if (_closed || generation != _generation || !ReferenceEquals(_detailCall, row) || policyRevision != _outputPreference.Revision) return;
            if (!payload.ApplyPage(page, reference, reset ? false : previous, budget)) return;
            await Dispatcher.InvokeAsync(() =>
            {
                if (_closed || generation != _generation || !ReferenceEquals(_detailCall, row)) return;
                if (automatic)
                {
                    box.ScrollToVerticalOffset(scroll);
                    if (start <= box.Text.Length) box.Select(start, Math.Min(length, box.Text.Length - start));
                }
                else box.ScrollToHome();
            }, DispatcherPriority.Loaded);
        }
        catch (OperationCanceledException) { }
        catch (Exception ex) when (ex is HttpRequestException or IOException or JsonException)
        {
            if (reference == payload.Reference) payload.ReadFailed(ex.Message);
        }
        finally
        {
            payload.SetBusy(false);
            if (!_closed && generation == _generation && ReferenceEquals(_detailCall, row) && policyRevision > 0 && _outputPreference is not null && policyRevision != _outputPreference.Revision)
                await LoadPayloadPageAsync(row, kind, false, automatic: true);
        }
    }
    private async void PayloadPage_Click(object sender, RoutedEventArgs e)
    {
        if (_detailCall is not { } row || sender is not FrameworkElement { Tag: string tag }) return;
        var split = tag.Split(':', 2);
        await LoadPayloadPageAsync(row, split[0], split.Length > 1 && split[1] == "previous");
    }
    private void ControlPanel_Click(object sender, RoutedEventArgs e) => (System.Windows.Application.Current as App)?.ShowControlPanel();
}
