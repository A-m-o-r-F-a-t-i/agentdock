using System.IO;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Controls.Primitives;
using Clipboard = System.Windows.Clipboard;
using Button = System.Windows.Controls.Button;
using MenuItem = System.Windows.Controls.MenuItem;
using MessageBox = System.Windows.MessageBox;

namespace AgentDock.ControlPanel;

public partial class ExecutionWindow
{
    private async void Objects_RightClick(object sender, MouseButtonEventArgs e)
    {
        if(e.ChangedButton!=MouseButton.Right)return;
        var container = FindAncestor<ListBoxItem>(e.OriginalSource as DependencyObject);
        if (container?.DataContext is not ExecutionObject item) { e.Handled = true; return; }
        // Context actions target the item under the pointer; an unrelated old
        // selection must never become the target of a destructive operation.
        _updating = true; ObjectsList.SelectedItems.Clear(); ObjectsList.SelectedItem = item; _updating = false;
        _frozenSelection = null; _menuSelection = item.Id == "" ? [] : [item.Id]; UpdateSelectionText();
        await GuardAsync(() => SelectObjectAsync(item));
    }
    private async void ObjectMore_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not Button { DataContext: ExecutionObject item } button) return;
        _updating = true; ObjectsList.SelectedItems.Clear(); ObjectsList.SelectedItem = item; _updating = false;
        _frozenSelection = null; _menuSelection = item.Id == "" ? [] : [item.Id]; UpdateSelectionText();
        OpenManagementMenu(button);
        await GuardAsync(() => SelectObjectAsync(item));
    }
    private void BulkMenu_Click(object sender, RoutedEventArgs e)
    {
        _menuSelection = (_frozenSelection ?? ObjectsList.SelectedItems.Cast<ExecutionObject>().Where(item => item.Id != "").Select(item => item.Id).ToArray()).ToArray();
        if (_menuSelection.Length == 0) { Warn("先选择需要管理的任务或对话。未识别来源只是查询分组，不能作为一个对话删除。"); return; }
        if (sender is Button button) OpenManagementMenu(button);
    }
    private void OpenManagementMenu(FrameworkElement anchor)
    {
        if (ObjectsList.ContextMenu is not ContextMenu menu) return;
        menu.PlacementTarget = anchor; menu.Placement = PlacementMode.Bottom; menu.IsOpen = true;
    }
    private void ManageMenu_Opened(object sender, RoutedEventArgs e)
    {
        if (sender is not ContextMenu menu) return;
        foreach (var item in menu.Items.OfType<MenuItem>()) item.IsEnabled = _menuSelection.Length > 0 && (item.Tag?.ToString() != "rename" || _menuSelection.Length == 1);
    }
    private async void SelectAll_Click(object sender, RoutedEventArgs e)
    {
        if (_view == "attention") { Warn("待处理视图针对执行调用，不进行任务批量选择。请进入任务或对话视图。"); return; }
        await GuardAsync(async () =>
        {
            var query = ListQuery(); var kind = _kind;
            var path = kind == "task" ? "/internal/runtime/execution/tasks" : "/internal/runtime/conversations";
            var response = await _client.ExecutionGetAsync(path + "?" + query + "&selection=true", _lifetime.Token);
            if (kind != _kind || query != ListQuery()) return;
            _frozenSelection = response.Array("selected_ids").Select(item => item.GetString() ?? "").Where(id => id != "").Distinct(StringComparer.Ordinal).ToArray();
            _updating = true; ObjectsList.SelectedItems.Clear(); foreach (var item in Objects.Where(item => _frozenSelection.Contains(item.Id))) ObjectsList.SelectedItems.Add(item); _updating = false;
            UpdateSelectionText(response.Number("total"));
        });
    }
    private void ClearSelection_Click(object sender, RoutedEventArgs e)
    { _frozenSelection = null; _updating = true; ObjectsList.SelectedItems.Clear(); _updating = false; UpdateSelectionText(); }
    private async void Manage_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not MenuItem { Tag: string action } || _menuSelection.Length == 0) return;
        var ids = _menuSelection.ToArray(); var kind = _kind; var title = ""; var tags = Array.Empty<string>();
        if (action == "rename")
        {
            var existing = Objects.FirstOrDefault(item => item.Id == ids[0])?.Title ?? "";
            var value = ExecutionDialogs.Prompt(this, "重命名", "只修改管理对象标题，不改变项目文件名。", existing);
            if (value is null) return; title = value;
        }
        if (action == "tags")
        {
            var value = ExecutionDialogs.Prompt(this, "设置标签", "多个标签以逗号分隔。将替换所选对象的标签，不会移动项目目录。", "");
            if (value is null) return; tags = value.Split([',', '，'], StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        }
        if (action is "trash" or "delete")
        {
            var message = action == "trash" ? $"将 {ids.Length} 个{(kind == "task" ? "任务" : "对话")}移入回收站，保留 {_preferences.RetentionDays} 天。\n\n运行中或待审批的关联对象会跳过并逐项说明。源码、仓库和工作区不会删除。"
                : $"永久删除 {ids.Length} 个已在回收站中的管理对象。\n\n此操作不能恢复这些管理数据。项目文件保持不变，执行与审批审计按独立保留策略保存。运行中或待审批项受到保护。";
            if (MessageBox.Show(this, message, action == "trash" ? "移入回收站" : "永久删除管理数据", MessageBoxButton.YesNo, MessageBoxImage.Warning, MessageBoxResult.No) != MessageBoxResult.Yes) return;
        }
        await GuardAsync(async () =>
        {
            var results = new List<JsonElement>(); var success = 0L; var skipped = 0L; var failed = 0L;
            foreach (var chunk in ids.Chunk(200))
            {
                var response = await _client.ExecutionPostAsync("/internal/runtime/" + (kind == "task" ? "tasks" : "conversations") + "/batch", new { ids = chunk, action, title, tags, retention_days = _preferences.RetentionDays, confirm_permanent = action == "delete" }, _lifetime.Token);
                success += response.Number("succeeded"); skipped += response.Number("skipped"); failed += response.Number("failed"); results.AddRange(response.Array("items"));
            }
            _frozenSelection = null; _menuSelection = [];
            var report = $"成功 {success} 项 · 跳过 {skipped} 项 · 失败 {failed} 项\n\n" + string.Join("\n", results.Select(item => $"{item.Text("id")} · {ExecutionJson.State(item.Text("status"))}\n{item.Text("message")}\n"));
            ExecutionDialogs.ShowText(this, "批量管理结果", report);
            await LoadObjectsAsync(false); await RefreshOverviewAsync();
        });
    }
    private async void LinkTask_Click(object sender, RoutedEventArgs e)
    {
        if (_selected is not { Kind: "conversation", IsUnknown: false } item) return;
        await GuardAsync(async () =>
        {
            var response = await _client.ExecutionGetAsync("/internal/runtime/execution/tasks?view=active&limit=200", _lifetime.Token);
            var choices = response.Array("tasks").Select(task => new ExecutionChoice(task.Text("id"), task.Text("title") + " · " + task.Text("id"))).ToList();
            var selected = ExecutionDialogs.Choose(this, "关联任务", "选择任务或输入已有 task_id。只建立管理关联，不改写任何历史调用。", choices);
            if (selected is null) return;
            await _client.ExecutionPostAsync("/internal/runtime/conversations/" + Encode(item.Id) + "/link-task", new { task_id = selected }, _lifetime.Token);
            await SelectObjectAsync(item);
        });
    }
    private async void SetCurrentTask_Click(object sender, RoutedEventArgs e)
    {
        if (_selected is not { Kind: "conversation", IsUnknown: false, Trashed: false } item) return;
        await GuardAsync(async () =>
        {
            var detail = await _client.ExecutionGetAsync("/internal/runtime/conversations/" + Encode(item.Id), _lifetime.Token);
            var revision = detail.Field("conversation").Field("state").Number("binding_revision");
            var response = await _client.ExecutionGetAsync("/internal/runtime/execution/tasks?view=active&limit=200", _lifetime.Token);
            var choices = new List<ExecutionChoice> { new("__unbind__", "解除当前任务绑定（保留历史）") };
            choices.AddRange(response.Array("tasks").Where(task => task.Text("status") is "active" or "blocked").Select(task => new ExecutionChoice(task.Text("id"), task.Text("title"))));
            var selected = ExecutionDialogs.Choose(this, "设置当前任务", "此操作只影响该对话后续调用。运行中的命令、待审批请求和历史记录保持原归属。", choices);
            if (selected is null) return;
            await _client.ExecutionPostAsync("/internal/runtime/conversations/" + Encode(item.Id) + "/current-task", new { task_id = selected == "__unbind__" ? "" : selected, binding_revision = revision }, _lifetime.Token);
            if (_selected?.Id == item.Id) await SelectObjectAsync(item);
        });
    }
    private async void OpenCurrentTask_Click(object sender, RoutedEventArgs e)
    {
        if (_currentConversationTaskId.Length == 0) return;
        var id = _currentConversationTaskId;
        await GuardAsync(async () =>
        {
            var detail = await _client.ExecutionGetAsync("/internal/runtime/tasks/" + Encode(id), _lifetime.Token);
            _view = _kind = "task";
            _updating = true; KindCombo.SelectedIndex = 1; _updating = false;
            _selected = ExecutionObject.From(detail.Field("task"), "task");
            await LoadObjectsAsync(false);
            await SelectObjectAsync(ExecutionObject.From(detail.Field("task"), "task"));
        });
    }

    private void Continue_Click(object sender, RoutedEventArgs e)
    {
        if (_selected?.Kind != "task") return;
        var branch = _branch == "" ? _taskSnapshot.Text("active_thread_id") : _branch;
        if (branch == "") branch = "main";
        Clipboard.SetText($"继续 AgentDock 任务 {_selected.Id}，任务分支 {branch}。先调用 task_manage(action=get, task_id={_selected.Id}) 和 task_manage(action=thread_get, task_id={_selected.Id}, thread_id={branch}) 读取实际进度，再调用 task_manage(action=resume, task_id={_selected.Id}, thread_id={branch}) 建立一次接续绑定。后续普通工具自动继承，无需重复填写任务或对话标识。不要重放结果未知或尚在运行的命令。\n");
        ConnectionText.Text = "已复制继续指令；尚未发起新的模型任务。";
    }
    private async void CancelTask_Click(object sender, RoutedEventArgs e)
    {
        if (_selected?.Kind != "task") return;
        var reason = ExecutionDialogs.Prompt(this, "取消任务", "取消任务不会自动宣称所有命令停止。存在运行项或待审批请求时，服务端会拒绝取消；先处理这些关联项。", "用户取消任务");
        if (reason is null) return;
        await GuardAsync(async () => { await _client.ControlAsync(new { action = "cancel", task_id = _selected.Id, summary = reason }, _lifetime.Token); await LoadTaskAsync(_selected, _generation); await RefreshOverviewAsync(); });
    }
    private async void Stop_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not Button { DataContext: ExecutionCallRow row } button) return;
        button.IsEnabled = false;
        try
        {
            await GuardAsync(async () =>
            {
                var response = await _client.ExecutionPostAsync("/internal/runtime/calls/" + Encode(row.Id) + "/stop", new { }, _lifetime.Token);
                ConnectionText.Text = response.Flag("stopped") ? "已确认命令退出。" : "停止请求已提交，等待原调用报告最终状态。";
                await LoadCallDetailAsync(row); await RefreshOverviewAsync();
            });
        }
        finally { if (!_closed) button.IsEnabled = true; }
    }
    private async void Approve_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not Button { DataContext: ExecutionCallRow row } button || row.ApprovalId == "") return;
        button.IsEnabled = false;
        try
        {
            await GuardAsync(async () =>
            {
                var detail = await _client.ExecutionGetAsync("/internal/runtime/approvals/" + Encode(row.ApprovalId), _lifetime.Token);
                var decision = ExecutionDialogs.Approve(this, detail);
                if (decision is null) return;
                var response = await _client.ExecutionPostAsync("/internal/runtime/approvals/" + Encode(row.ApprovalId) + "/" + decision.Value.Action, new { allow_workspace = decision.Value.AllowWorkspace }, _lifetime.Token);
                ConnectionText.Text = response.Flag("dispatched") ? "原固定请求已派发，执行结果将回写同一调用。" : "审批结果已保存，没有重复派发。";
                await LoadCallDetailAsync(row); await RefreshOverviewAsync();
            });
        }
        finally { if (!_closed) button.IsEnabled = true; }
    }
    private async void Reject_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not FrameworkElement { DataContext: ExecutionCallRow row } || row.ApprovalId == "") return;
        await GuardAsync(async () => { await _client.ExecutionPostAsync("/internal/runtime/approvals/" + Encode(row.ApprovalId) + "/reject", new { }, _lifetime.Token); await LoadCallDetailAsync(row); await RefreshOverviewAsync(); });
    }
    private void RetryInstruction_Click(object sender,RoutedEventArgs e)
    {
        if(sender is not FrameworkElement {DataContext:ExecutionCallRow row} || !row.CanRetry)return;
        Clipboard.SetText($"核对上一次调用 {row.Id} 的实际结果和失败原因。确认可以安全重试后，对同一工具使用 retry_of_call_id={row.Id} 发起一次新调用；对话身份由接入层自动解析，不填写对话标识，沿用原操作目标，不绕过权限审批。已完成的部分不要重复执行。实际命令：\n{row.Command}");
        ConnectionText.Text="已复制重试指令，没有自动重放命令。";
    }
    private void SaveFilter_Click(object sender,RoutedEventArgs e)
    {
        var name=ExecutionDialogs.Prompt(this,"保存筛选","保存当前视图、对象类型、关键词、工作区和标签。不会复制任务或移动文件。","");
        if(string.IsNullOrWhiteSpace(name))return;
        if(_preferences.SavedFilters.Count>=30 && !_preferences.SavedFilters.ContainsKey(name)){Warn("最多保存30个筛选。请选择已有名称覆盖。");return;}
        _preferences.SavedFilters[name]=[_view,_kind,SearchBox.Text,TagBox.Text,(WorkspaceCombo.SelectedItem as ExecutionChoice)?.Id??""];
        SavePreferences();ConnectionText.Text="已保存筛选："+name;
    }
    private async void LoadFilter_Click(object sender,RoutedEventArgs e)
    {
        if(_preferences.SavedFilters.Count==0){Warn("还没有保存的筛选。先设置工作区、关键词或标签，再点击保存筛选。");return;}
        var name=ExecutionDialogs.Choose(this,"载入筛选","选择已保存的筛选。",_preferences.SavedFilters.Keys.Select(key=>new ExecutionChoice(key,key)).ToArray());
        if(name is null || !_preferences.SavedFilters.TryGetValue(name,out var filter) || filter.Length!=5)return;
        _updating=true;_view=filter[0];_kind=filter[1];SearchBox.Text=filter[2];TagBox.Text=filter[3];KindCombo.SelectedIndex=_kind=="task"?1:0;
        WorkspaceCombo.SelectedItem=WorkspaceCombo.Items.Cast<ExecutionChoice>().FirstOrDefault(item=>item.Id==filter[4])??WorkspaceCombo.Items.Cast<ExecutionChoice>().First();_updating=false;
        _selected=null;_frozenSelection=null;await GuardAsync(()=>LoadObjectsAsync(false));
    }
    private void CopyCommand_Click(object sender, RoutedEventArgs e)
    { if (sender is FrameworkElement { DataContext: ExecutionCallRow row }) Clipboard.SetText(row.Command.Length > 0 ? row.Command : row.Summary); }
    private async void Permissions_Click(object sender, RoutedEventArgs e)
    {
        await GuardAsync(async () =>
        {
            var query = _selected?.Kind == "conversation" && !_selected.IsUnknown ? "?conversation_id=" + Encode(_selected.Id) : _selected?.WorkspaceId is { Length: > 0 } workspace ? "?workspace_id=" + Encode(workspace) : "";
            var detail = await _client.ExecutionGetAsync("/internal/runtime/permissions/effective" + query, _lifetime.Token);
            var change = ExecutionDialogs.Permissions(this, detail);
            if (change is null) return;
            await _client.ExecutionPostAsync("/internal/runtime/permissions", change, _lifetime.Token);
            await RefreshOverviewAsync(); if (_selected is not null) await SelectObjectAsync(_selected);
        });
    }
    private void Preferences_Click(object sender, RoutedEventArgs e)
    {
        if (ExecutionDialogs.Preferences(this, _preferences)) { FontSize = _preferences.FontSize; SavePreferences(); }
    }
    private async void Export_Click(object sender, RoutedEventArgs e)
    {
        if (_selected is null && _view != "attention") return;
        var dialog = new Microsoft.Win32.SaveFileDialog { Title = "导出脱敏执行记录", FileName = "AgentDock-execution.json", Filter = "JSON 文件 (*.json)|*.json", DefaultExt = ".json" };
        if (dialog.ShowDialog(this) != true) return;
        var query = CallQuery();
        await GuardAsync(async () =>
        {
            var rows = new List<JsonElement>(); var warnings = new HashSet<string>(); long before = 0; var gap = false;
            while (true)
            {
                var page = await _client.ExecutionGetAsync("/internal/runtime/calls?" + query + "&include_output=true&limit=200" + (before == 0 ? "" : "&before=" + before), _lifetime.Token);
                rows.AddRange(page.Array("calls")); gap |= page.Flag("gap");
                foreach (var warning in page.Array("warnings")) warnings.Add(warning.GetString() ?? "");
                if (!page.Flag("has_more")) break;
                before = page.Number("next_before"); if (before == 0) throw new InvalidDataException("服务未返回下一页游标，已停止不完整导出。");
                if (rows.Count >= 100000) { warnings.Add("导出达到 100000 条上限，其余记录未包含。"); gap = true; break; }
            }
            var export = new { schema_version = 2, exported_at = DateTimeOffset.UtcNow, query, history_incomplete = gap, warnings = warnings.ToArray(), output_policy = "已在服务端持久化前脱敏；仅含有限输出摘要，不含宿主原始对话标识或读取文件正文。", calls = rows.OrderBy(row => row.Number("created_seq")) };
            var content = JsonSerializer.Serialize(export, new JsonSerializerOptions { WriteIndented = true, Encoder = System.Text.Encodings.Web.JavaScriptEncoder.UnsafeRelaxedJsonEscaping });
            await File.WriteAllTextAsync(dialog.FileName, content, _lifetime.Token); ConnectionText.Text = $"已导出 {rows.Count} 条调用，历史缺口及截断标记随文件保留。";
        });
    }
}
