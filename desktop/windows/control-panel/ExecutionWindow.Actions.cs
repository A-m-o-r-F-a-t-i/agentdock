using System.IO;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Threading;
using Button = System.Windows.Controls.Button;
using Clipboard = System.Windows.Clipboard;
using ContextMenu = System.Windows.Controls.ContextMenu;
using ListBox = System.Windows.Controls.ListBox;
using MenuItem = System.Windows.Controls.MenuItem;
using KeyEventArgs = System.Windows.Input.KeyEventArgs;
using SaveFileDialog = Microsoft.Win32.SaveFileDialog;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private readonly List<ExecutionObject> _managedItems = [];
    private int _dataOffset, _dataEpoch;
    private ulong _dataBefore;
    private bool _attentionView;

    private ContextMenu Menu(FrameworkElement anchor)
    {
        var menu = new ContextMenu { PlacementTarget = anchor, Resources = Resources }; anchor.ContextMenu = menu;
        menu.Opened += (_, _) => _openMenus++;
        menu.Closed += (_, _) => _openMenus = Math.Max(0, _openMenus - 1);
        return menu;
    }
    private static void AddMenu(ContextMenu menu, string title, Func<Task> action, bool enabled = true)
    {
        var item = new MenuItem { Header = title, IsEnabled = enabled };
        item.Click += async (_, _) => await action(); menu.Items.Add(item);
    }
    private void ActionMenu(ContextMenu menu, string title, Func<Task> action, bool enabled = true) => AddMenu(menu, title, () => GuardAsync(action), enabled);
    private void ChoiceMenu(ContextMenu menu, string title, bool selected, Func<Task> action)
    {
        var item = new MenuItem { Header = title, IsCheckable = true, IsChecked = selected };
        item.Click += async (_, _) => await GuardAsync(action);
        menu.Items.Add(item);
    }
    private static void Divider(ContextMenu menu) => menu.Items.Add(new Separator());
    private void OpenMenu(ContextMenu menu) { menu.IsOpen = true; }
    private static FrameworkElement Anchor(object sender, FrameworkElement fallback) => sender as FrameworkElement ?? fallback;
    private async Task SetConversationViewAsync(string view) { _conversationView = view; _frozenSelection = null; await LoadObjectsAsync(); SavePreferences(); }

    private void SidebarMenu_Click(object sender, RoutedEventArgs e)
    {
        var menu = Menu(Anchor(sender, ObjectsList));
        ChoiceMenu(menu, "当前对话", _conversationView == "active", () => SetConversationViewAsync("active"));
        ChoiceMenu(menu, "已归档", _conversationView == "archived", () => SetConversationViewAsync("archived"));
        ChoiceMenu(menu, "回收站", _conversationView == "trash", () => SetConversationViewAsync("trash"));
        ActionMenu(menu, "选择当前筛选的全部对话", SelectAllObjectsAsync);
        ActionMenu(menu, "管理所选对话", () => { ShowObjectMenu(ObjectsList, SelectedObjectIds()); return Task.CompletedTask; }, ObjectsList.SelectedItems.Count > 0);
        ActionMenu(menu, "历史任务与未归属记录", () => OpenDataManagerAsync(false));
        OpenMenu(menu);
    }
    private string[] SelectedObjectIds() => _frozenSelection ?? ObjectsList.SelectedItems.Cast<ExecutionObject>().Where(item => !item.IsUnknown && !item.IsOrphan && !item.IsGroupFooter).Select(item => item.Id).Distinct().ToArray();
    private async Task SelectAllObjectsAsync()
    {
        var page = await _client.ExecutionGetAsync("/internal/runtime/conversations?" + ListQuery(true), _lifetime.Token);
        _frozenSelection = page.Array("selected_ids").Select(value => value.GetString()!).Where(value => value.Length > 0).ToArray();
        _updating = true;
        try { ObjectsList.SelectAll(); } finally { _updating = false; }
        Warn($"已选择当前筛选中的 {_frozenSelection.Length} 个对话。");
    }
    private async void SelectAllObjects_Click(object sender, RoutedEventArgs e) => await GuardAsync(SelectAllObjectsAsync);
    private void Objects_RightClick(object sender, MouseButtonEventArgs e)
    {
        if (Ancestor<ListBoxItem>(e.OriginalSource as DependencyObject) is not { DataContext: ExecutionObject { IsGroupFooter: false } row } item) return;
        _menuSelection = item.IsSelected ? SelectedObjectIds() : row.IsUnknown || row.IsOrphan ? [] : [row.Id];
        ShowObjectMenu(item, _menuSelection, row); e.Handled = true;
    }
    private void ConversationMenu_Click(object sender, RoutedEventArgs e)
    {
        var ids = _selected is { IsUnknown:false, IsOrphan:false } ? new[] { _selected.Id } : Array.Empty<string>();
        _menuSelection = ids; ShowObjectMenu(Anchor(sender, ConversationHeader), ids);
    }
    private void ShowObjectMenu(FrameworkElement anchor, string[] ids, ExecutionObject? target = null)
    {
        var fixedIds = ids.ToArray(); var selected = target ?? Objects.FirstOrDefault(item => fixedIds.Contains(item.Id)) ?? _selected;
        var menu = Menu(anchor);
        ActionMenu(menu, "打开", () => selected is null ? Task.CompletedTask : OpenSidebarObjectAsync(selected), selected is { IsGroupFooter: false } && fixedIds.Length <= 1);
        ActionMenu(menu, "对话详情", () => { ShowInfo("对话详情", selected?.Snapshot.Pretty() ?? "未归属记录使用原始调用 ID 管理。"); return Task.CompletedTask; }, selected is not null);
        ActionMenu(menu, "导出执行记录", () => ExportConversationsAsync(fixedIds), selected is not null);
        if (selected is { IsUnknown:true } || selected is { IsOrphan:true })
        {
            ActionMenu(menu, "管理记录", () => OpenDataManagerAsync(false)); OpenMenu(menu); return;
        }
        Divider(menu);
        ActionMenu(menu, "重命名", () => RenameAsync("conversation", fixedIds, selected?.Title ?? ""), fixedIds.Length == 1);
        ActionMenu(menu, "分类标签", () => TagsAsync("conversation", fixedIds, selected?.Tags ?? ""), fixedIds.Length > 0);
        ActionMenu(menu, selected?.Pinned == true ? "取消置顶" : "置顶", () => BatchAsync("conversation", fixedIds, selected?.Pinned == true ? "unpin" : "pin"), fixedIds.Length > 0);
        ActionMenu(menu, selected?.Archived == true ? "取消归档" : "归档", () => BatchAsync("conversation", fixedIds, selected?.Archived == true ? "unarchive" : "archive"), fixedIds.Length > 0);
        ActionMenu(menu, selected?.Trashed == true ? "从回收站恢复" : "移入回收站", () => ConfirmBatchAsync("conversation", fixedIds, selected?.Trashed == true ? "restore" : "trash"), fixedIds.Length > 0);
        if (selected?.Trashed == true) ActionMenu(menu, "永久删除", () => ConfirmBatchAsync("conversation", fixedIds, "delete"), fixedIds.Length > 0);
        Divider(menu);
        var terminated = selected?.Terminated == true || selected?.Id == _selected?.Id && _conversationSnapshot.HasDate("terminated_at");
        ActionMenu(menu, terminated ? "恢复此对话" : "终止此对话", () => ChangeLifecycleAsync(fixedIds[0], terminated ? "resume" : "terminate"), fixedIds.Length == 1 && selected?.Trashed != true);
        ActionMenu(menu, "关联已有任务", () => LinkTaskAsync(fixedIds[0]), fixedIds.Length == 1 && !terminated && selected?.Trashed != true);
        OpenMenu(menu);
    }
    private async Task RenameAsync(string kind, string[] ids, string title)
    {
        if (ids.Length != 1) return;
        var value = ExecutionDialogs.Prompt(this, "重命名", "输入名称", title);
        if (value is null) return;
        if (value.Length == 0) { Warn("名称不能为空。"); return; }
        await BatchAsync(kind, ids, "rename", value);
    }
    private async Task TagsAsync(string kind, string[] ids, string initial)
    {
        var value = ExecutionDialogs.Prompt(this, "分类标签", "使用逗号分隔多个标签，留空可清除。", initial);
        if (value is null) return;
        var tags = value.Split([',', '，', '、'], StringSplitOptions.TrimEntries | StringSplitOptions.RemoveEmptyEntries).Distinct().ToArray();
        await BatchAsync(kind, ids, "tags", tags: tags);
    }
    private async Task ConfirmBatchAsync(string kind, string[] ids, string action)
    {
        if (ids.Length == 0) return;
        if (action is "trash" or "delete")
        {
            var prompt = action == "delete" ? $"永久删除所选的 {ids.Length} 条记录及其管理索引。此操作不能从回收站恢复，工作区源码保持不变。" : $"将所选的 {ids.Length} 条记录移入回收站。运行中或待审批的记录会被保留，工作区源码保持不变。";
            if (!ExecutionDialogs.Confirm(this, action == "delete" ? "永久删除" : "移入回收站", prompt, action == "delete" ? "永久删除" : "移入回收站")) return;
        }
        await BatchAsync(kind, ids, action);
    }
    private async Task BatchAsync(string kind, string[] ids, string action, string title = "", string[]? tags = null)
    {
        var endpoint = kind switch { "task" => "/internal/runtime/tasks/batch", "call" => "/internal/runtime/calls/batch", _ => "/internal/runtime/conversations/batch" };
        var outcomes = new List<JsonElement>(); long succeeded = 0, failed = 0, skipped = 0;
        foreach (var batch in ids.Distinct().Chunk(200))
        {
            var result = await _client.ExecutionPostAsync(endpoint, new { ids = batch, action, title, tags = tags ?? [], retention_days = _preferences.RetentionDays, confirm_permanent = action == "delete" }, _lifetime.Token);
            succeeded += result.Number("succeeded"); failed += result.Number("failed"); skipped += result.Number("skipped"); outcomes.AddRange(result.Array("items"));
        }
        _frozenSelection = null;
        await LoadObjectsAsync();
        if (_selected is not null) await LoadCallsAsync(false);
        if (DataManagementPanel.Visibility == Visibility.Visible) await LoadManagedAsync(false);
        if (failed > 0 || skipped > 0) ShowInfo("管理结果", $"已完成 {succeeded}，跳过 {skipped}，失败 {failed}。\n\n" + string.Join("\n", outcomes.Where(item => item.Text("status") != "succeeded").Select(item => item.Text("id") + "：" + item.Text("message"))));
        else Warn($"已更新 {succeeded} 条记录。");
    }
    private async Task ChangeLifecycleAsync(string id, string action)
    {
        var stopping = action == "terminate";
        if (!ExecutionDialogs.Confirm(this, stopping ? "终止此对话" : "恢复此对话", stopping ? "先阻止该对话的新执行请求，再取消待审批请求并停止正在运行的调用。历史记录保持可读。" : "重新允许此对话提交请求。已经取消的调用不会自动重试。", stopping ? "终止" : "恢复")) return;
        var result = await _client.ExecutionPostAsync("/internal/runtime/conversations/" + Escape(id) + "/" + action, new { confirm = true }, _lifetime.Token);
        if (_selected?.Id == id) _conversationSnapshot = result.Field("conversation");
        await LoadObjectsAsync(); await LoadCallsAsync(false); UpdateStopButton();
        var warnings = result.Array("warnings").Select(value => value.GetString()).Where(value => !string.IsNullOrWhiteSpace(value));
        var text = result.Flag("has_remaining_activity") ? "终止门禁已生效，仍有调用需要核对停止结果。" : stopping ? "此对话已终止，后续执行已被拦截。" : "此对话已恢复，已取消的调用未重放。";
        Warn(text + string.Join("\n", warnings.Prepend("")));
    }
    private async void Terminate_Click(object sender, RoutedEventArgs e) { if (_selected is { } selected) await GuardAsync(() => ChangeLifecycleAsync(selected.Id, "terminate")); }
    private async Task LinkTaskAsync(string conversation)
    {
        var page = await _client.ExecutionGetAsync("/internal/runtime/execution/tasks?view=active&limit=200", _lifetime.Token);
        var choices = page.Array("tasks").Select(value => new ExecutionChoice(value.Text("id", value.Text("task_id")), value.Text("title"))).ToArray();
        var id = ExecutionDialogs.Choose(this, "关联已有任务", "只建立关联，不切换正在执行的任务。", choices);
        if (id is null) return;
        await _client.ExecutionPostAsync("/internal/runtime/conversations/" + Escape(conversation) + "/link-task", new { task_id = id }, _lifetime.Token);
        if (_selected is not null) await SelectObjectAsync(_selected);
    }
    private async void Permissions_Click(object sender, RoutedEventArgs e) => await GuardAsync(async () =>
    {
        var query = _selected is { IsUnknown:false, IsOrphan:false } ? "?conversation_id=" + Escape(_selected.Id) : "";
        var detail = await _client.ExecutionGetAsync("/internal/runtime/permissions/effective" + query, _lifetime.Token);
        var change = ExecutionDialogs.Permissions(this, detail);
        if (change is not null) { await _client.ExecutionPostAsync("/internal/runtime/permissions", change, _lifetime.Token); await RefreshOverviewAsync(); Warn("权限设置已保存。"); }
    });
    private void SettingsMenu_Click(object sender, RoutedEventArgs e)
    {
        var menu = Menu(Anchor(sender, ConversationHeader));
        ActionMenu(menu, "显示、保留与通知", OpenDisplayPreferencesAsync);
        ActionMenu(menu, "历史任务与记录管理", () => OpenDataManagerAsync(false));
        ActionMenu(menu, "保存当前筛选", () => { var name = ExecutionDialogs.Prompt(this, "保存筛选", "筛选名称", ""); if (!string.IsNullOrWhiteSpace(name)) { _preferences.SavedFilters[name] = [_conversationView, SearchBox.Text, CallSearchBox.Text, ComboValue(CallStatusCombo)]; SavePreferences(); } return Task.CompletedTask; });
        foreach (var pair in _preferences.SavedFilters.ToArray())
            ActionMenu(menu, "筛选：" + pair.Key, async () => { var values = pair.Value; if (values.Length != 4) return; _conversationView = values[0]; SearchBox.Text = values[1]; CallSearchBox.Text = values[2]; CallStatusCombo.SelectedItem = CallStatusCombo.Items.Cast<ComboBoxItem>().FirstOrDefault(item => item.Tag?.ToString() == values[3]); await LoadObjectsAsync(); });
        ActionMenu(menu, "结构与使用说明", () => { ShowInfo("结构与使用说明", "左侧按工作区组织对话。任务位于当前对话内，任务选择和分支浏览不会改变正在执行的上下文。\n\n单条执行记录显示状态、动作、耗时和时间；点击记录后在下方查看命令、输出、来源和技术信息。右键或使用菜单可批量管理记录。\n\n终止对话会先写入服务端门禁，再取消待审批和运行调用。关闭本窗口只退出观察，不会停止执行。\n\n旧任务缺少步骤时显示“进度未记录”。未归属调用保留原始调用 ID，可以导出、隔离、归档和移入回收站。永久删除不会删除项目源码。\n\n快捷键：Ctrl+F 搜索对话，F5 刷新，Esc 关闭详情，Shift+F10 打开所选条目菜单。"); return Task.CompletedTask; });
        OpenMenu(menu);
    }
    private string[] SelectedCallIds() => CallsList.SelectedItems.Cast<ExecutionCallRow>().Where(row => !row.IsInsertion).Select(row => row.Id).Distinct().ToArray();
    private void Calls_RightClick(object sender, MouseButtonEventArgs e)
    {
        if (Ancestor<ListBoxItem>(e.OriginalSource as DependencyObject) is not { DataContext: ExecutionCallRow row } item) return;
        if (!item.IsSelected) CallsList.SelectedItem = row;
        if (row.IsInsertion) { ShowInsertionMenu(item,row); e.Handled=true; return; }
        ShowCallMenu(item, SelectedCallIds()); e.Handled = true;
    }
    private void CallMenu_Click(object sender, RoutedEventArgs e) => ShowCallMenu(Anchor(sender, CallsList), SelectedCallIds());
    private void ShowCallMenu(FrameworkElement anchor, string[] ids)
    {
        if (ids.Length == 0 && CallsList.SelectedItem is ExecutionCallRow { IsInsertion:true } message) { ShowInsertionMenu(anchor,message); return; }
        var fixedIds = ids.ToArray(); var menu = Menu(anchor);
        ActionMenu(menu, "导出当前筛选", () => ExportScopeAsync(CallScopeQuery(), "执行记录"));
        ActionMenu(menu, "导出所选记录", () => ExportCallIdsAsync(fixedIds), fixedIds.Length > 0);
        ActionMenu(menu, "归档所选", () => BatchAsync("call", fixedIds, "archive"), fixedIds.Length > 0);
        ActionMenu(menu, "隔离所选", () => BatchAsync("call", fixedIds, "isolate"), fixedIds.Length > 0);
        ActionMenu(menu, "移入回收站", () => ConfirmBatchAsync("call", fixedIds, "trash"), fixedIds.Length > 0); Divider(menu);
        foreach (var view in new[] { ("active", "当前记录"), ("archived", "已归档记录"), ("isolated", "已隔离记录"), ("trash", "回收站记录") })
            ChoiceMenu(menu, view.Item2, _callView == view.Item1, async () => { _callView = view.Item1; await LoadCallsAsync(false); });
        if (_callView == "trash") { ActionMenu(menu, "恢复所选", () => BatchAsync("call", fixedIds, "restore"), fixedIds.Length > 0); ActionMenu(menu, "永久删除所选", () => ConfirmBatchAsync("call", fixedIds, "delete"), fixedIds.Length > 0); }
        if (_callView == "isolated") ActionMenu(menu, "取消隔离所选", () => BatchAsync("call", fixedIds, "unisolate"), fixedIds.Length > 0);
        if (_callView == "archived") ActionMenu(menu, "取消归档所选", () => BatchAsync("call", fixedIds, "unarchive"), fixedIds.Length > 0);
        OpenMenu(menu);
    }
    private void CopyCommand_Click(object sender, RoutedEventArgs e) { if (_detailCall is { } row) CopyText(row.RequestText); }
    private void CopyOutput_Click(object sender, RoutedEventArgs e) { if (_detailCall is { } row) CopyText(row.Output); }
    private void CopyText(string value) { try { Clipboard.SetText(value); } catch (System.Runtime.InteropServices.ExternalException) { Warn("剪贴板正忙，请重试复制。"); } }
    private async void StopCall_Click(object sender, RoutedEventArgs e) { if (_detailCall is { CanStop:true } row) await GuardAsync(async () => { await _client.ExecutionPostAsync("/internal/runtime/calls/" + Escape(row.Id) + "/stop", new { }, _lifetime.Token); await LoadCallDetailAsync(row); }); }
    private async void Approve_Click(object sender, RoutedEventArgs e)
    {
        if (_detailCall is not { NeedsApproval:true } row) return;
        await GuardAsync(async () =>
        {
            var detail = await _client.ExecutionGetAsync("/internal/runtime/approvals/" + Escape(row.ApprovalId), _lifetime.Token);
            var decision = ExecutionDialogs.Approve(this, detail);
            if (decision is null) return;
            await _client.ExecutionPostAsync("/internal/runtime/approvals/" + Escape(row.ApprovalId) + "/" + decision.Value.Action, new { allow_workspace = decision.Value.AllowWorkspace }, _lifetime.Token);
            await LoadCallDetailAsync(row); await RefreshOverviewAsync();
        });
    }
    private async void Reject_Click(object sender, RoutedEventArgs e) { if (_detailCall is { NeedsApproval:true } row) await GuardAsync(async () => { await _client.ExecutionPostAsync("/internal/runtime/approvals/" + Escape(row.ApprovalId) + "/reject", new { }, _lifetime.Token); await LoadCallDetailAsync(row); await RefreshOverviewAsync(); }); }
    private void RetryInstruction_Click(object sender, RoutedEventArgs e)
    {
        if (_detailCall is not { CanRetry:true } row) return;
        var text = $"请读取调用 {row.Id} 的原始请求与失败原因，核对是否已产生部分效果，再决定是否以 retry_of_call_id={row.Id} 重试。不要直接重放摘要中的命令。";
        CopyText(text); ShowInfo("重试说明已复制", text);
    }
    private async void ContinueBranch_Click(object sender, RoutedEventArgs e)
    {
        if (_selectedTaskId.Length == 0 || _branch.Length == 0) return;
        if (!ExecutionDialogs.Confirm(this, "切换执行分支", "将此任务的继续位置切换到所选分支。已运行的命令保留原绑定。", "切换")) return;
        await GuardAsync(async () => { await _client.ControlAsync(new { action = "thread_switch", task_id = _selectedTaskId, thread_id = _branch }, _lifetime.Token); Warn("继续分支已更新。"); });
    }
    private void ContinueTask_Click(object sender, RoutedEventArgs e) { if (_selectedTaskId.Length > 0) { var text = $"继续任务 {_selectedTaskId}，先读取任务及分支 {_branch} 的最新状态，按检查点继续，已完成的操作不要重复执行。"; CopyText(text); Warn("继续任务说明已复制。"); } }
    private void TaskMenu_Click(object sender, RoutedEventArgs e)
    {
        if (_selectedTaskId.Length == 0) return;
        var id = _selectedTaskId; var menu = Menu(Anchor(sender, TaskDetailsPanel));
        ActionMenu(menu, "设为此对话当前任务", async () => { if (_selected is null || _selected.IsUnknown) return; await _client.ExecutionPostAsync("/internal/runtime/conversations/" + Escape(_selected.Id) + "/current-task", new { task_id = id, task_thread_id = _branch, binding_revision = _conversationSnapshot.Field("state").Number("binding_revision") }, _lifetime.Token); await SelectObjectAsync(_selected); });
        ActionMenu(menu, "重命名任务", () => RenameAsync("task", [id], _taskSnapshot.Text("title")));
        ActionMenu(menu, "分类标签", () => TagsAsync("task", [id], ""));
        ActionMenu(menu, "归档任务", () => BatchAsync("task", [id], "archive"));
        ActionMenu(menu, "取消任务", async () => { var reason = ExecutionDialogs.Prompt(this, "取消任务", "取消原因", ""); if (string.IsNullOrWhiteSpace(reason)) return; await _client.ControlAsync(new { action = "cancel", task_id = id, summary = reason }, _lifetime.Token); await LoadTaskAsync(id, _branch, true); });
        ActionMenu(menu, "移入回收站", () => ConfirmBatchAsync("task", [id], "trash")); OpenMenu(menu);
    }
    private async Task OpenDataManagerAsync(bool attention)
    {
        _attentionView = attention; OpenDetails(attention ? "待处理" : "历史任务与记录管理", DataManagementPanel);
        if (attention) { _updating = true; DataKindCombo.SelectedIndex = 2; DataViewCombo.SelectedIndex = 0; _updating = false; }
        await LoadManagedAsync(false);
    }
    private async void Attention_Click(object sender, RoutedEventArgs e) => await GuardAsync(() => OpenDataManagerAsync(true));
    private async void DataFilter_Changed(object sender, SelectionChangedEventArgs e) { if (_initialized && !_updating && DataManagementPanel.Visibility == Visibility.Visible) { _attentionView = false; DetailsTitle.Text = "历史任务与记录管理"; await GuardAsync(() => LoadManagedAsync(false)); } }
    private async Task LoadManagedAsync(bool more)
    {
        var epoch = ++_dataEpoch; var kind = ComboValue(DataKindCombo); var view = ComboValue(DataViewCombo); var tasks = kind == "task";
        if (tasks && view == "isolated") { _managedItems.Clear(); ManagedObjectsList.ItemsSource = _managedItems.ToArray(); MoreDataButton.Visibility = Visibility.Collapsed; return; }
        var query = tasks ? $"/internal/runtime/execution/tasks?view={view}&limit=200&offset={(more ? _dataOffset : 0)}" : $"/internal/runtime/calls?view={view}&limit=200&top_level=true" + (kind == "unattributed" ? "&unattributed=true" : "") + (_attentionView ? "&status=pending_approval" : "") + (more && _dataBefore > 0 ? "&before=" + _dataBefore : "");
        var page = await _client.ExecutionGetAsync(query, _lifetime.Token);
        if (_closed || epoch != _dataEpoch) return;
        if (!more) _managedItems.Clear();
        foreach (var raw in page.Array(tasks ? "tasks" : "calls"))
        {
            var item = tasks ? ExecutionObject.From(raw, "task") : new ExecutionObject { Id = raw.Text("call_id"), Kind = "call", Title = new ExecutionCallRow(raw).Title, Detail = ExecutionJson.State(raw.Text("status")), Archived = raw.HasDate("archived_at"), Trashed = raw.HasDate("trashed_at"), Snapshot = raw.Clone() };
            if (_managedItems.All(existing => existing.Id != item.Id)) _managedItems.Add(item);
        }
        _dataOffset = (int)page.Number("next_offset"); _dataBefore = (ulong)page.Number("next_before");
        MoreDataButton.Visibility = page.Flag("has_more") ? Visibility.Visible : Visibility.Collapsed;
        ManagedObjectsList.ItemsSource = _managedItems.ToArray();
    }
    private async void MoreData_Click(object sender, RoutedEventArgs e) => await GuardAsync(() => LoadManagedAsync(true));
    private void Data_RightClick(object sender, MouseButtonEventArgs e) { if (Ancestor<ListBoxItem>(e.OriginalSource as DependencyObject) is not { DataContext:ExecutionObject row } item) return; if (!item.IsSelected) ManagedObjectsList.SelectedItem = row; ShowDataMenu(item); e.Handled = true; }
    private void ManageData_Click(object sender, RoutedEventArgs e) => ShowDataMenu(Anchor(sender, ManagedObjectsList));
    private void ShowDataMenu(FrameworkElement anchor)
    {
        var rows = ManagedObjectsList.SelectedItems.Cast<ExecutionObject>().ToArray(); if (rows.Length == 0) { Warn("请先选择要管理的记录。"); return; }
        var ids = rows.Select(row => row.Id).ToArray(); var kind = rows[0].Kind; var view = ComboValue(DataViewCombo); var menu = Menu(anchor);
        ActionMenu(menu, "查看详情", async () => { if (kind == "task") { _selectedTaskId = ids[0]; OpenDetails(rows[0].Title, TaskDetailsPanel); await LoadTaskAsync(ids[0], "", true); } else { var row = new ExecutionCallRow(rows[0].Snapshot); _detailCall = row; CallDetailsTabs.DataContext = row; CallDetailsTabs.SelectedIndex = 0; OpenDetails(row.Title, CallDetailsTabs); await LoadCallDetailAsync(row); } }, ids.Length == 1);
        if (kind == "task") ActionMenu(menu, "重命名", () => RenameAsync(kind, ids, rows[0].Title), ids.Length == 1);
        ActionMenu(menu, view == "archived" ? "取消归档" : "归档", () => BatchAsync(kind, ids, view == "archived" ? "unarchive" : "archive"));
        if (kind == "call") ActionMenu(menu, view == "isolated" ? "取消隔离" : "隔离", () => BatchAsync(kind, ids, view == "isolated" ? "unisolate" : "isolate"));
        ActionMenu(menu, view == "trash" ? "恢复" : "移入回收站", () => ConfirmBatchAsync(kind, ids, view == "trash" ? "restore" : "trash"));
        if (view == "trash") ActionMenu(menu, "永久删除", () => ConfirmBatchAsync(kind, ids, "delete"));
        OpenMenu(menu);
    }
    private async void ExportData_Click(object sender, RoutedEventArgs e) => await GuardAsync(async () =>
    {
        var rows = ManagedObjectsList.SelectedItems.Cast<ExecutionObject>().ToArray();
        if (rows.Length == 0) { Warn("请先选择导出对象。"); return; }
        if (rows[0].Kind == "call") await ExportCallIdsAsync(rows.Select(row => row.Id).ToArray());
        else await SaveExportAsync("任务记录", rows.Select(row => row.Snapshot).ToArray());
    });
    private async Task<List<JsonElement>> ReadScopeAsync(string query)
    {
        var records = new List<JsonElement>(); ulong before = 0;
        while (true)
        {
            var page = await _client.ExecutionGetAsync("/internal/runtime/calls?" + query + "&limit=200&include_output=true" + (before > 0 ? "&before=" + before : ""), _lifetime.Token);
            records.AddRange(page.Array("calls"));
            if (!page.Flag("has_more")) return records;
            var next = (ulong)page.Number("next_before"); if (next == 0 || next == before) throw new IOException("导出分页未前进，文件尚未写入。");
            if (records.Count >= 100000) throw new IOException("当前导出超过 100000 条记录，请缩小筛选范围。"); before = next;
        }
    }
    private async Task ExportScopeAsync(string query, string title) => await SaveExportAsync(title, await ReadScopeAsync(query));
    private async Task ExportConversationsAsync(string[] ids)
    {
        var calls = new List<JsonElement>();
        if (ids.Length == 0 && _selected?.IsUnknown == true) calls = await ReadScopeAsync("unattributed=true&view=all");
        foreach (var id in ids) calls.AddRange(await ReadScopeAsync("conversation_id=" + Escape(id) + "&view=all"));
        await SaveExportAsync("对话执行记录", calls);
    }
    private async Task ExportCallIdsAsync(string[] ids)
    {
        var calls = new List<JsonElement>();
        foreach (var id in ids) calls.Add(await _client.ExecutionGetAsync("/internal/runtime/calls/" + Escape(id), _lifetime.Token));
        await SaveExportAsync("所选执行记录", calls);
    }
    private async Task SaveExportAsync(string title, object records)
    {
        var dialog = new SaveFileDialog { Title = "导出" + title, Filter = "JSON 文件|*.json", FileName = "AgentDock-" + title + "-" + DateTime.Now.ToString("yyyyMMdd-HHmm") + ".json", AddExtension = true, OverwritePrompt = true };
        if (dialog.ShowDialog(this) != true) return;
        var temporary = dialog.FileName + "." + Guid.NewGuid().ToString("N") + ".tmp";
        try { await File.WriteAllTextAsync(temporary, JsonSerializer.Serialize(new { schema_version = 2, exported_at = DateTimeOffset.UtcNow, records }, ActivityClient.JsonOptions), _lifetime.Token); File.Move(temporary, dialog.FileName, true); Warn("已导出：" + dialog.FileName); }
        finally { if (File.Exists(temporary)) File.Delete(temporary); }
    }
    private async void Window_KeyDown(object sender, KeyEventArgs e)
    {
        if (e.Key == Key.Escape) { CloseDetails(); e.Handled = true; }
        else if (e.Key == Key.F && Keyboard.Modifiers.HasFlag(ModifierKeys.Control)) { SearchBox.Focus(); SearchBox.SelectAll(); e.Handled = true; }
        else if (e.Key == Key.F5) { await GuardAsync(async () => { await LoadObjectsAsync(); await LoadCallsAsync(false); await RefreshOverviewAsync(); }); e.Handled = true; }
        else if (e.Key == Key.F10 && Keyboard.Modifiers.HasFlag(ModifierKeys.Shift)) { if (ObjectsList.IsKeyboardFocusWithin) ShowObjectMenu(ObjectsList, SelectedObjectIds()); else if (CallsList.IsKeyboardFocusWithin) ShowCallMenu(CallsList, SelectedCallIds()); else if (ManagedObjectsList.IsKeyboardFocusWithin) ShowDataMenu(ManagedObjectsList); e.Handled = true; }
    }
}
