using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Threading;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private string _shownInsertion = "";
    private readonly HashSet<string> _insertionActions = new(StringComparer.Ordinal);

    private void AcceptInsertionResult(string conversation, JsonElement result)
    {
        var item = result.Field("insertion");
        if (item.ValueKind != JsonValueKind.Object || item.Text("conversation_id") != conversation) return;
        // A list request started before this accepted send cannot erase it.
        if (!_insertionReadVersions.ContainsKey(conversation) && _insertionReadVersions.Count >= MaximumStateCache) _insertionReadVersions.Remove(_insertionReadVersions.Keys.First());
        _insertionReadVersions[conversation] = ++_insertionReadVersion;
        if (!_insertionStates.ContainsKey(conversation) && _insertionStates.Count >= MaximumStateCache) _insertionStates.Remove(_insertionStates.Keys.First());
        var items = _insertionStates.GetValueOrDefault(conversation, []).Where(old => old.Text("insertion_id") != item.Text("insertion_id")).Append(item.Clone()).OrderBy(old => old.Number("sequence")).ToArray();
        _insertionStates[conversation] = items;
        _activityClock.Synchronize(result.Date("server_now"));
        if (_selected?.Id == conversation) { SyncInsertionTimeline(); RenderInsertionStatus(); }
    }

    private void SyncInsertionTimeline()
    {
        var conversation = _selected?.Id ?? "";
        var desired = InsertionTimeline.Merge(Calls, _insertionStates.GetValueOrDefault(conversation, []), conversation, _taskFilter, CallSearchBox.Text.Trim(), ComboValue(CallStatusCombo), _callView, _activityClock.ServerNow);
        var previous = _updating; _updating = true;
        try
        {
            var retained = desired.ToHashSet();
            for (var index = Calls.Count-1; index >= 0; index--) if (!retained.Contains(Calls[index])) Calls.RemoveAt(index);
            for (var index = 0; index < desired.Count; index++)
            {
                if (index < Calls.Count && ReferenceEquals(Calls[index], desired[index])) continue;
                var old = Calls.IndexOf(desired[index]);
                if (old >= 0) Calls.Move(old,index); else Calls.Insert(index,desired[index]);
            }
        }
        finally { _updating = previous; }
        if (_bottomPane == InfoDetailsText && DetailsTitle.Text == "用户补充" && Calls.FirstOrDefault(row => row.IsInsertion && row.Id == _shownInsertion) is { } shown) InfoDetailsText.Text = shown.InsertionDetails;
        UpdateEmpty();
    }

    private async Task RefreshInsertionTimelineAsync()
    {
        var anchor = !_following ? CaptureCallAnchor() : null;
        var previousIds = Calls.Select(row => row.Id).ToArray();
        SyncInsertionTimeline();
        if (anchor is not null && !previousIds.SequenceEqual(Calls.Select(row => row.Id))) await RestoreCallAnchorAsync(anchor);
        else if (_following && Calls.Count > 0 && !previousIds.SequenceEqual(Calls.Select(row => row.Id)))
            await Dispatcher.InvokeAsync(() => { if (!_closed && Calls.Count > 0) CallsList.ScrollIntoView(Calls[^1]); }, DispatcherPriority.Loaded);
    }

    private void ShowInsertionDetails(ExecutionCallRow row)
    {
        _detailCall = null; _shownInsertion = row.Id;
        ShowInfo("用户补充", row.InsertionDetails);
    }

    private void ShowInsertionMenu(FrameworkElement anchor, ExecutionCallRow row)
    {
        var menu = Menu(anchor);
        ActionMenu(menu,"查看补充",() => { ShowInsertionDetails(row); return Task.CompletedTask; });
        ActionMenu(menu,"复制补充",() => { CopyText(row.InsertionText); return Task.CompletedTask; });
        ActionMenu(menu,"安全重投补充",() => ChangeInsertionAsync(row,"retry"),row.CanRedeliverInsertion);
        ActionMenu(menu,"停止后续投递",() => ChangeInsertionAsync(row,"cancel"),row.CanCancelInsertion);
        OpenMenu(menu);
    }

    private async void RetryInsertionRow_Click(object sender,RoutedEventArgs e)
    {
        e.Handled = true;
        if (sender is FrameworkElement { DataContext:ExecutionCallRow { IsInsertion:true } row }) await GuardAsync(() => ChangeInsertionAsync(row,"retry"));
    }

    private async Task ChangeInsertionAsync(ExecutionCallRow row,string action)
    {
        if (!row.IsInsertion || _selected?.Id != row.ConversationId || !_insertionActions.Add(row.Id)) return;
        try
        {
            await _client.ExecutionPostAsync("/internal/runtime/conversations/"+Escape(row.ConversationId)+"/insertions/"+Escape(row.Id)+"/"+action,new {},_lifetime.Token);
            await ReadInsertionsAsync(row.ConversationId);
        }
        finally { _insertionActions.Remove(row.Id); }
    }
}
