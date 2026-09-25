using System.Text;
using System.Text.Json;
using System.Windows;
using System.Windows.Input;
using System.Windows.Threading;
using KeyEventArgs = System.Windows.Input.KeyEventArgs;
using TextChangedEventArgs = System.Windows.Controls.TextChangedEventArgs;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private const int InsertionTextLimit = 8192;
    private const int MaximumDrafts = 128;
    private const int MaximumStateCache = 64;
    private readonly Dictionary<string, string> _insertionDrafts = new(StringComparer.Ordinal);
    private readonly Dictionary<string, (string ID, string Text)> _insertionSubmissions = new(StringComparer.Ordinal);
    private readonly Dictionary<string, JsonElement[]> _insertionStates = new(StringComparer.Ordinal);
    private readonly HashSet<string> _insertionSending = new(StringComparer.Ordinal);
	private readonly Dictionary<string,long> _insertionReadVersions = new(StringComparer.Ordinal);
	private long _insertionReadVersion;
    private FrameworkElement? _bottomPane;
    private bool _restoringDraft, _imeComposing, _readingInsertions;

    private void InitializeComposer()
    {
        TextCompositionManager.AddPreviewTextInputStartHandler(InsertionTextBox, (_, _) => _imeComposing = true);
        TextCompositionManager.AddPreviewTextInputUpdateHandler(InsertionTextBox, (_, _) => _imeComposing = true);
        TextCompositionManager.AddPreviewTextInputHandler(InsertionTextBox, (_, _) =>
            Dispatcher.BeginInvoke(() => _imeComposing = false, DispatcherPriority.ContextIdle));
        InsertionTextBox.LostKeyboardFocus += (_, _) => _imeComposing = false;
    }
    private void SaveComposerDraft()
    {
        if (_selected is not { IsUnknown: false, IsOrphan: false, IsGroupFooter: false } selected || InsertionTextBox is null) return;
        var text = InsertionTextBox.Text;
        if (text.Length == 0) _insertionDrafts.Remove(selected.Id);
        else if (_insertionDrafts.ContainsKey(selected.Id) || _insertionDrafts.Count < MaximumDrafts) _insertionDrafts[selected.Id] = text;
    }
    private void RestoreComposerDraft()
    {
        _restoringDraft = true;
        try { InsertionTextBox.Text = _selected is { } selected ? _insertionDrafts.GetValueOrDefault(selected.Id, "") : ""; }
        finally { _restoringDraft = false; }
        _imeComposing = false;
        RenderInsertionStatus();
    }
    private void HideBottomPane()
    {
        DetailsPanel.Visibility = Visibility.Collapsed; _detailCall = null; _bottomPane = null;
        foreach (var pane in new FrameworkElement[] { InsertionPanel, CallDetailsTabs, TaskDetailsPanel, InfoDetailsText, DataManagementPanel })
            pane.Visibility = Visibility.Collapsed;
    }
    private bool CanUseComposer() => _selected is { IsUnknown: false, IsOrphan: false, IsGroupFooter: false, Trashed: false, Terminated: false } selected &&
        !_conversationSnapshot.HasDate("terminated_at") && _activityClock.ServerNow is { } now &&
        ConversationActivityClock.CanInsert(selected.LastToolCallAt, now, false);

    private void UpdateComposerAvailability()
    {
        if (InsertionPanel is null || _closed) return;
        var eligible = CanUseComposer();
        InsertionTextBox.IsReadOnly = _selected is { } current && !_insertionDrafts.ContainsKey(current.Id) && _insertionDrafts.Count >= MaximumDrafts;
        InsertButton.IsEnabled = eligible;
        InsertButton.ToolTip = eligible ? "插入补充要求" : "最近 3 分钟没有工具调用";
        if (eligible && (_bottomPane is null || _bottomPane == InsertionPanel)) OpenDetails("", InsertionPanel);
        else if (!eligible && _bottomPane == InsertionPanel) { SaveComposerDraft(); HideBottomPane(); }
        StopConversationButton.Visibility = eligible ? Visibility.Visible : Visibility.Collapsed;
        StopConversationButton.IsEnabled = eligible;
        SendInsertionButton.IsEnabled = eligible && _selected is { } selected && !_insertionSending.Contains(selected.Id) &&
            !string.IsNullOrWhiteSpace(InsertionTextBox.Text) && Encoding.UTF8.GetByteCount(InsertionTextBox.Text) <= InsertionTextLimit;
    }
    private void Insert_Click(object sender, RoutedEventArgs e)
    {
        if (!CanUseComposer()) return;
        OpenDetails("", InsertionPanel); InsertionTextBox.Focus();
    }
    private void InsertionText_Changed(object sender, TextChangedEventArgs e)
    {
        if (_restoringDraft || !_initialized) return;
        SaveComposerDraft(); UpdateComposerAvailability(); RenderInsertionStatus();
    }
    private async void Insertion_KeyDown(object sender, KeyEventArgs e)
    {
        if (e.Key != Key.Return || Keyboard.Modifiers != ModifierKeys.None || _imeComposing || e.ImeProcessedKey == Key.Return) return;
        e.Handled = true;
        await GuardAsync(SendInsertionAsync);
    }
    private async void SendInsertion_Click(object sender, RoutedEventArgs e) => await GuardAsync(SendInsertionAsync);
    private async Task SendInsertionAsync()
    {
        if (!CanUseComposer() || _selected is not { } selected) return;
        var conversation = selected.Id; var text = InsertionTextBox.Text;
        if (string.IsNullOrWhiteSpace(text) || Encoding.UTF8.GetByteCount(text) > InsertionTextLimit || !_insertionSending.Add(conversation)) return;
        if (!_insertionSubmissions.TryGetValue(conversation, out var submission) || submission.Text != text)
        {
            if (_insertionSubmissions.Count >= MaximumDrafts && !_insertionSubmissions.ContainsKey(conversation))
            { _insertionSending.Remove(conversation); InsertionStatus.Text = "待确认发送过多，请先核对已有插入状态。"; return; }
            _insertionSubmissions[conversation] = submission = (Guid.NewGuid().ToString("N"), text);
        }
        UpdateComposerAvailability();
        try
        {
            var accepted = await _client.ExecutionPostAsync("/internal/runtime/conversations/" + Escape(conversation) + "/insertions",
                new { submission_id = submission.ID, text = submission.Text }, _lifetime.Token);
			AcceptInsertionResult(conversation, accepted);
            _insertionSubmissions.Remove(conversation);
            if (_insertionDrafts.GetValueOrDefault(conversation) == text) _insertionDrafts.Remove(conversation);
            if (_selected?.Id == conversation && InsertionTextBox.Text == text) InsertionTextBox.Clear();
            await ReadInsertionsAsync(conversation);
        }
        finally { _insertionSending.Remove(conversation); UpdateComposerAvailability(); }
    }
    private async Task RefreshInsertionsAsync()
    {
        if (_readingInsertions || _selected is not { IsUnknown: false, IsOrphan: false, IsGroupFooter: false } selected) return;
        _readingInsertions = true;
        try { await ReadInsertionsAsync(selected.Id); }
        finally { _readingInsertions = false; }
    }
    private async Task ReadInsertionsAsync(string conversation)
    {
		if (!_insertionReadVersions.ContainsKey(conversation) && _insertionReadVersions.Count >= MaximumStateCache) _insertionReadVersions.Remove(_insertionReadVersions.Keys.First());
		var version = ++_insertionReadVersion; _insertionReadVersions[conversation] = version;
        var value = await _client.ExecutionGetAsync("/internal/runtime/conversations/" + Escape(conversation) + "/insertions", _lifetime.Token);
        if (_closed || _insertionReadVersions.GetValueOrDefault(conversation) != version) return;
        if (!_insertionStates.ContainsKey(conversation) && _insertionStates.Count >= MaximumStateCache)
            _insertionStates.Remove(_insertionStates.Keys.First());
        _insertionStates[conversation] = value.Array("insertions");
		_activityClock.Synchronize(value.Date("server_now"));
        if (_selected?.Id == conversation) { RenderInsertionStatus(); await RefreshInsertionTimelineAsync(); }
    }
    private void RenderInsertionStatus()
    {
        if (InsertionStatus is null) return;
        WithdrawInsertionButton.Visibility = Visibility.Collapsed;
        if (InsertionTextBox.IsReadOnly) { InsertionStatus.Text = "已有 128 个对话草稿，请先发送或清空已有草稿。"; return; }
        if (Encoding.UTF8.GetByteCount(InsertionTextBox.Text) > InsertionTextLimit)
        { InsertionStatus.Text = "补充内容超过 8192 字节，请缩短后发送。"; return; }
        var items = _selected is { } selected ? _insertionStates.GetValueOrDefault(selected.Id, []) : [];
        var pending = items.Where(item => item.Text("status") is "pending" or "target_changed").ToArray();
        if (pending.Length > 0)
        {
            InsertionStatus.Text = pending.Any(item => item.Text("status") == "target_changed") ? "目标任务已变化，请撤回后重新发送。" : $"{pending.Length} 条等待下一次工具调用";
            WithdrawInsertionButton.Visibility = Visibility.Visible;
        }
        else
        {
            InsertionStatus.Text = items.Length == 0 ? "" : InsertionPresentation.State(items[^1]);
        }
    }
    private async void WithdrawInsertion_Click(object sender, RoutedEventArgs e)
    {
        if (_selected is not { } selected) return;
        var conversation = selected.Id;
        var ids = _insertionStates.GetValueOrDefault(conversation, []).Where(item => item.Text("status") is "pending" or "target_changed").Select(item => item.Text("insertion_id")).ToArray();
        await GuardAsync(async () =>
        {
            foreach (var id in ids)
                await _client.ExecutionPostAsync("/internal/runtime/conversations/" + Escape(conversation) + "/insertions/" + Escape(id) + "/cancel", new { }, _lifetime.Token);
            await ReadInsertionsAsync(conversation);
        });
    }
}
