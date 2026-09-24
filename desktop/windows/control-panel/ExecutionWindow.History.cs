using System.IO;
using System.Net.Http;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private bool _hasOlderCalls, _loadingOlderCalls, _restoringCallScroll, _historyReadFailed;
    private sealed record CallScrollAnchor(int Generation, string Id, double WithinRow, double AbsoluteOffset);

    private CallScrollAnchor CaptureCallAnchor()
    {
        var offset = FindVisualChild<ScrollViewer>(CallsList)?.VerticalOffset ?? 0;
        var height = (double)FindResource("ExecutionRowHeight");
        var index = Calls.Count == 0 ? -1 : Math.Clamp((int)(offset / height), 0, Calls.Count - 1);
        return new(_generation, index < 0 ? "" : Calls[index].Id, index < 0 ? 0 : offset - index * height, offset);
    }
    private async Task RestoreCallAnchorAsync(CallScrollAnchor anchor)
    {
        if (_closed || anchor.Generation != _generation) return;
        _restoringCallScroll = true;
        try
        {
            await Dispatcher.InvokeAsync(() =>
            {
                if (_closed || anchor.Generation != _generation) return;
                var index = anchor.Id.Length > 0 && _callsById.TryGetValue(anchor.Id, out var row) ? Calls.IndexOf(row) : -1;
                var offset = index < 0 ? anchor.AbsoluteOffset : index * (double)FindResource("ExecutionRowHeight") + anchor.WithinRow;
                CallsList.UpdateLayout();
                FindVisualChild<ScrollViewer>(CallsList)?.ScrollToVerticalOffset(Math.Max(0, offset));
            }, DispatcherPriority.Loaded);
        }
        finally { _restoringCallScroll = false; }
    }
    private async Task TryLoadOlderCallsAsync(bool retry = false)
    {
        if (_closed || _selected is null || !_hasOlderCalls || _before == 0 || _loadingOlderCalls || _following || _historyReadFailed && !retry) return;
        var scroll = FindVisualChild<ScrollViewer>(CallsList);
        if (scroll is null || !retry && scroll.VerticalOffset > Math.Max(72, scroll.ViewportHeight * 0.1)) return;
        var anchor = CaptureCallAnchor();
        var epoch = _streamEpoch;
        _loadingOlderCalls = true;
        HistoryRetryButton.Visibility = Visibility.Collapsed;
        try
        {
            await LoadCallsAsync(true);
            if (_closed || epoch != _streamEpoch || anchor.Generation != _generation) return;
            _historyReadFailed = false;
            await RestoreCallAnchorAsync(anchor);
        }
        catch (OperationCanceledException) { }
        catch (Exception ex) when (ex is HttpRequestException or IOException or JsonException or InvalidOperationException)
        {
            if (_closed || epoch != _streamEpoch || anchor.Generation != _generation) return;
            _historyReadFailed = true;
            HistoryRetryButton.ToolTip = ex.Message;
            HistoryRetryButton.Visibility = Visibility.Visible;
        }
        finally { _loadingOlderCalls = false; }
    }
    private async void RetryHistory_Click(object sender, RoutedEventArgs e) => await TryLoadOlderCallsAsync(true);
}
