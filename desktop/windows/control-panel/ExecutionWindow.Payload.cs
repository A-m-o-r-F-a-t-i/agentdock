using System.IO;
using System.Net.Http;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private async Task LoadPayloadPageAsync(ExecutionCallRow row, string kind, bool previous, bool automatic = false)
    {
        var payload = kind == "request" ? row.RequestPayload : row.ResponsePayload;
        if (payload.Reference.Length == 0 || payload.Busy || automatic && (!payload.NeedsLoad || payload.HasReadError)) return;
        var reference = payload.Reference;
        var offset = automatic ? 0 : payload.RequestedOffset(previous);
        var box = kind == "request" ? RequestPayloadText : ResponsePayloadText;
        var scroll = box.VerticalOffset;
        var start = box.SelectionStart; var length = box.SelectionLength;
        var generation = _generation;
        payload.SetBusy(true);
        try
        {
            var page = await _client.ExecutionGetAsync("/internal/runtime/calls/" + Escape(row.Id) + "/payload/" + kind + "?offset=" + offset + "&limit=" + ExecutionPayloadView.PageBytes, SelectionToken);
            if (_closed || generation != _generation || !ReferenceEquals(_detailCall, row)) return;
            if (!payload.ApplyPage(page, reference, previous)) return;
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
        finally { payload.SetBusy(false); }
    }
    private async void PayloadPage_Click(object sender, RoutedEventArgs e)
    {
        if (_detailCall is not { } row || sender is not FrameworkElement { Tag: string tag }) return;
        var split = tag.Split(':', 2);
        await LoadPayloadPageAsync(row, split[0], split.Length > 1 && split[1] == "previous");
    }
    private void ControlPanel_Click(object sender, RoutedEventArgs e) => (System.Windows.Application.Current as App)?.ShowControlPanel();
}
