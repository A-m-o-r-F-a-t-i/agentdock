using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Automation;
using Button = System.Windows.Controls.Button;
using CheckBox = System.Windows.Controls.CheckBox;
using ComboBox = System.Windows.Controls.ComboBox;
using MessageBox = System.Windows.MessageBox;
using TextBox = System.Windows.Controls.TextBox;
using Color = System.Windows.Media.Color;
using HorizontalAlignment = System.Windows.HorizontalAlignment;
using FontFamily = System.Windows.Media.FontFamily;
using Orientation = System.Windows.Controls.Orientation;

namespace AgentDock.ControlPanel;

internal static class ExecutionDialogs
{
    private static (Window Window, DockPanel Root, StackPanel Actions) Create(Window owner, string title, int width = 620, int height = 460)
    {
        var window = new Window { Owner = owner, Title = title, Width = width, Height = height, MinWidth = 360, MinHeight = 160, ShowInTaskbar = false, WindowStartupLocation = WindowStartupLocation.CenterOwner, FontFamily = owner.FontFamily, FontSize = owner.FontSize, Resources = owner.Resources, Background = owner.Background, Foreground = owner.Foreground };
        var root = new DockPanel { Margin = new Thickness(18) }; window.Content = root;
        var actions = new StackPanel { Orientation = Orientation.Horizontal, HorizontalAlignment = HorizontalAlignment.Right, Margin = new Thickness(0, 12, 0, 0) }; DockPanel.SetDock(actions, Dock.Bottom); root.Children.Add(actions);
        return (window, root, actions);
    }
    private static Button Action(string text, string? automationId = null)
    {
        var button = new Button { Content = text, Padding = new Thickness(16, 7, 16, 7), Margin = new Thickness(4, 0, 0, 0), MinHeight = 34 };
        if (automationId is not null) AutomationProperties.SetAutomationId(button, automationId);
        return button;
    }
    private static TextBlock Label(string value) => new() { Text = value, TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 8, 0, 6) };
    private static TextBox Readonly(string value) => new() { Text = value, IsReadOnly = true, AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, VerticalScrollBarVisibility = ScrollBarVisibility.Auto, Padding = new Thickness(10), FontFamily = new FontFamily("Consolas, Microsoft YaHei UI") };
    internal static void ShowText(Window owner, string title, string text)
    {
        var ui = Create(owner, title); var close = Action("关闭"); close.Click += (_, _) => ui.Window.Close(); ui.Actions.Children.Add(close); ui.Root.Children.Add(Readonly(text)); ui.Window.ShowDialog();
    }
    internal static bool Confirm(Window owner, string title, string explanation, string confirm = "确定")
    {
        var ui = Create(owner, title, 500, 230);
        ui.Window.ResizeMode = ResizeMode.NoResize;
        ui.Root.Children.Add(Label(explanation));
        var cancel = Action("取消", "ExecutionConfirmCancel"); cancel.IsCancel = true;
        var ok = Action(confirm, "ExecutionConfirmAccept");
        cancel.Click += (_, _) => ui.Window.Close();
        ok.Click += (_, _) => ui.Window.DialogResult = true;
        ui.Actions.Children.Add(cancel); ui.Actions.Children.Add(ok);
        ui.Window.Loaded += (_, _) => cancel.Focus();
        return ui.Window.ShowDialog() == true;
    }
    internal static string? Prompt(Window owner, string title, string explanation, string initial)
    {
        var ui = Create(owner, title, 500, 220); var panel = new StackPanel(); panel.Children.Add(Label(explanation));
        var box = new TextBox { Text = initial, TextWrapping = TextWrapping.Wrap, MinHeight = 42, MaxHeight = 150, Padding = new Thickness(8) }; AutomationProperties.SetAutomationId(box, "ExecutionPromptValue"); panel.Children.Add(box); ui.Root.Children.Add(panel);
        string? result = null; var ok = Action("确定", "ExecutionPromptConfirm"); var cancel = Action("取消");
        ok.Click += (_, _) => { result = box.Text.Trim(); ui.Window.DialogResult = true; }; cancel.Click += (_, _) => ui.Window.Close(); ui.Actions.Children.Add(cancel); ui.Actions.Children.Add(ok);
        ui.Window.Loaded += (_, _) => { box.Focus(); box.SelectAll(); }; ui.Window.ShowDialog(); return result;
    }
    internal static string? Choose(Window owner, string title, string explanation, IReadOnlyList<ExecutionChoice> choices)
    {
        var ui = Create(owner, title, 560, 290); var panel = new StackPanel(); panel.Children.Add(Label(explanation));
        var combo = new ComboBox { ItemsSource = choices, DisplayMemberPath = "Title", MinHeight = 34, IsEditable = false }; panel.Children.Add(combo);
        panel.Children.Add(Label("或输入未在前 200 项中显示的 task_id：")); var text = new TextBox { MinHeight = 34, Padding = new Thickness(6) }; panel.Children.Add(text); ui.Root.Children.Add(panel);
        string? result = null; var confirm = Action("关联任务"); confirm.Click += (_, _) => { result = text.Text.Trim().Length > 0 ? text.Text.Trim() : (combo.SelectedItem as ExecutionChoice)?.Id; if (result is not null) ui.Window.DialogResult = true; }; ui.Actions.Children.Add(confirm); ui.Window.ShowDialog(); return result;
    }
    internal static (string Action, bool AllowWorkspace)? Approve(Window owner, JsonElement detail)
    {
        var a = detail.Field("approval"); var pending = a.Text("status") == "pending" && detail.Flag("request_available");
        var ui = Create(owner, "执行前审批 · 固定请求", 840, 730);
        var panel = new DockPanel(); ui.Root.Children.Add(panel);
        var header = Label($"来源对话：{a.Text("conversation_id")}\n关联任务：{(a.Text("task_id") == "" ? "无" : a.Text("task_id"))}\n操作：{a.Text("tool")}\n拦截规则：{a.Text("rule_id")}\n原因：{a.Text("reason")}\n\n影响范围：\n{a.Text("scope_description")}\n\n当前状态：{(pending ? "尚未执行" : a.Text("status"))}"); DockPanel.SetDock(header, Dock.Top); panel.Children.Add(header);
        var grant = new CheckBox { Content = "同时允许此工作区的同类工具操作（创建下列 Allow 规则）", IsEnabled = pending && a.Text("workspace_id") != "", Margin = new Thickness(0, 9, 0, 5) };
        var grantPanel = new StackPanel(); grantPanel.Children.Add(grant); var rule = Readonly(detail.Field("rule_preview").Pretty()); rule.Height = 120; grantPanel.Children.Add(rule); DockPanel.SetDock(grantPanel, Dock.Bottom); panel.Children.Add(grantPanel);
        var fixedText = Readonly(detail.Text("fixed_request")); AutomationProperties.SetAutomationId(fixedText, "ApprovalFixedRequest"); panel.Children.Add(fixedText);
        (string, bool)? result = null; var reject = Action("拒绝", "ApprovalReject"); var once = Action("允许一次", "ApprovalApproveOnce"); var cancel = Action("返回");
        reject.IsEnabled = once.IsEnabled = pending;
        grant.Checked += (_, _) => once.Content = "批准并保存工作区规则"; grant.Unchecked += (_, _) => once.Content = "允许一次";
        reject.Click += (_, _) => { result = ("reject", false); ui.Window.DialogResult = true; };
        once.Click += (_, _) => { result = ("approve", grant.IsChecked == true); ui.Window.DialogResult = true; };
        cancel.Click += (_, _) => ui.Window.Close(); ui.Actions.Children.Add(cancel); ui.Actions.Children.Add(reject); ui.Actions.Children.Add(once); ui.Window.ShowDialog(); return result;
    }
    internal static object? Permissions(Window owner, JsonElement detail)
    {
        var ui = Create(owner, "执行权限 · 服务端策略", 840, 710); var policy = detail.Field("policy");
        var panel = new StackPanel(); ui.Root.Children.Add(new ScrollViewer { Content = panel, VerticalScrollBarVisibility = ScrollBarVisibility.Auto });
        panel.Children.Add(Label($"当前选中对象生效模式：{ExecutionJson.Mode(detail.Field("effective").Text("mode"))}\n默认模式：{ExecutionJson.Mode(policy.Text("global_mode"))} · 策略修订：{policy.Number("revision")}\n权限只控制 AgentDock 是否派发请求，不提升操作系统权限。显式 Deny 规则在完全权限下仍然有效。"));
        var scopes = new List<ExecutionChoice> { new("global:", "全局默认") };
        var conversationId = detail.Text("conversation_id");
        if (conversationId.Length > 0) scopes.Add(new("conversation:" + conversationId, "当前对话"));
        panel.Children.Add(Label("继承关系：对话设置 → 工作区设置 → 全局默认。未配置的层级继续继承。当前生效来源：" + detail.Field("effective").Text("scope")));
        scopes.AddRange(detail.Array("workspaces").Select(workspace => new ExecutionChoice("workspace:" + workspace.Text("workspace_id"), workspace.Text("name") + " · " + workspace.Text("root"))));
        panel.Children.Add(Label("作用范围")); var scope = new ComboBox { ItemsSource = scopes, DisplayMemberPath = "Title", SelectedIndex = 0, MinHeight = 34 }; panel.Children.Add(scope);
        panel.Children.Add(Label("执行模式")); var mode = new ComboBox { ItemsSource = new[] { new ExecutionChoice("readonly", "只读检查：写入和未知副作用禁止派发"), new ExecutionChoice("rules", "按规则审批：已确认安全项直行，其余等待决定"), new ExecutionChoice("full", "完全权限：当前范围免审批，显式禁止仍生效") }, DisplayMemberPath = "Title", MinHeight = 34 }; panel.Children.Add(mode);
        AutomationProperties.SetAutomationId(scope, "PermissionScope"); AutomationProperties.SetAutomationId(mode, "PermissionMode");
        void SelectMode()
        {
            var selected = ((scope.SelectedItem as ExecutionChoice)?.Id ?? "global:").Split(':', 2);
            var name = policy.Text("global_mode");
            if (selected[0] == "conversation")
                foreach (var existing in policy.Array("scopes")) if (existing.Text("kind") == "workspace" && existing.Text("id") == detail.Text("workspace_id")) name = existing.Text("mode");
            foreach (var existing in policy.Array("scopes")) if (existing.Text("kind") == selected[0] && existing.Text("id") == selected[1]) name = existing.Text("mode");
            mode.SelectedItem = mode.Items.Cast<ExecutionChoice>().FirstOrDefault(item => item.Id == name) ?? mode.Items[1];
        }
        SelectMode(); scope.SelectionChanged += (_, _) => SelectMode();
        var enableRuleEdit = new CheckBox { Content = "同时修改危险规则（高级）", Margin = new Thickness(0, 14, 0, 5) }; panel.Children.Add(enableRuleEdit);
        panel.Children.Add(Label("规则按工具名、可选 action 和工作区匹配。effect 为 deny、ask 或 allow。未列出的有副作用操作默认等待审批。"));
        var rules = new TextBox { Text = policy.Field("rules").Pretty(), AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, Height = 210, VerticalScrollBarVisibility = ScrollBarVisibility.Auto, IsReadOnly = true, FontFamily = new FontFamily("Consolas"), Padding = new Thickness(8) }; panel.Children.Add(rules);
        enableRuleEdit.Checked += (_, _) => rules.IsReadOnly = false; enableRuleEdit.Unchecked += (_, _) => rules.IsReadOnly = true;
        var errorText = Label(""); errorText.SetResourceReference(TextBlock.ForegroundProperty, "DangerBrush"); panel.Children.Add(errorText);
        void LabelError(string message) { errorText.Text = message; }
        object? result = null; var save = Action("保存并生效", "PermissionSave"); var cancel = Action("取消"); cancel.Click += (_, _) => ui.Window.Close();
        save.Click += (_, _) =>
        {
            if (mode.SelectedItem is not ExecutionChoice selectedMode || scope.SelectedItem is not ExecutionChoice selectedScope) return;
            if (selectedMode.Id == "full" && selectedScope.Id.StartsWith("conversation:", StringComparison.Ordinal)) { LabelError("完全权限需要选择工作区或全局范围。对话范围只用于限制权限。"); return; }
            if (selectedMode.Id == "full" && !Confirm(ui.Window, "确认完全权限范围", $"在“{selectedScope.Title}”启用完全权限。命令、文件写入及第三方工具将免去可选审批，显式禁止规则仍生效。", "启用")) return;
            try
            {
                var scopeParts = selectedScope.Id.Split(':', 2);
                var change = new Dictionary<string, object> { ["scope"] = scopeParts[0], ["scope_id"] = scopeParts[1], ["mode"] = selectedMode.Id, ["confirm_full"] = selectedMode.Id == "full", ["expected_revision"] = policy.Number("revision") };
                if (enableRuleEdit.IsChecked == true)
                {
                    using var parsed = JsonDocument.Parse(rules.Text);
                    if (parsed.RootElement.ValueKind != JsonValueKind.Array) throw new JsonException("规则必须是 JSON 数组。");
                    change["rules"] = parsed.RootElement.Clone();
                }
                result = change; ui.Window.DialogResult = true;
            }
            catch (JsonException ex) { MessageBox.Show(ui.Window, ex.Message, "规则无效", MessageBoxButton.OK, MessageBoxImage.Error); }
        };
        ui.Actions.Children.Add(cancel); ui.Actions.Children.Add(save); ui.Window.ShowDialog(); return result;
    }
    internal static bool Preferences(Window owner, ExecutionPreferences preferences, McpUiPreference display, Func<ToolOutputSettings, Task> saveOutput)
    {
        var ui = Create(owner, "显示与回收站保留", 560, 620); var panel = new StackPanel(); ui.Root.Children.Add(new ScrollViewer { Content = panel, VerticalScrollBarVisibility = ScrollBarVisibility.Auto });
        var outputEnabled = new CheckBox { Content = "截断工具输出", IsChecked = display.ToolOutput.Enabled, Margin = new Thickness(0, 6, 0, 4) };
        AutomationProperties.SetAutomationId(outputEnabled, "ToolOutputEnabled"); panel.Children.Add(outputEnabled);
        panel.Children.Add(Label("输出上限（字符，1,000–100,000）"));
        var outputChars = new ComboBox { IsEditable = true, ItemsSource = new[] { 1000, 5000, 10000, 20000, 50000, 100000 }, Text = display.ToolOutput.MaxChars.ToString(System.Globalization.CultureInfo.InvariantCulture), MinHeight = 34 };
        AutomationProperties.SetAutomationId(outputChars, "ToolOutputMaxChars"); panel.Children.Add(outputChars);
        panel.Children.Add(Label("作用于当前设备后续普通文本工具返回及活动中心分页。规则、结构化控制信息和用户插入保持完整；关闭后原有资源上限仍有效。"));
        outputChars.ToolTip = "按 Unicode 标量计数：普通汉字、英文及单码点 emoji 各为一字符；组合字符按码点计数，CRLF 计两字符。位置和续读仍使用 UTF-8 字节偏移。";
        if (display.Warning.Length > 0) panel.Children.Add(Label(display.Warning));
        panel.Children.Add(Label("新移入回收站对象的保留天数（1–3650）。已有对象继续使用其原定到期日期。工作区和源码不在回收站清理范围。"));
        var days = new TextBox { Text = preferences.RetentionDays.ToString(), MinHeight = 34, Padding = new Thickness(6) }; panel.Children.Add(days);
        panel.Children.Add(Label("界面字号（12–20）")); var font = new ComboBox { ItemsSource = new[] { 12d, 13d, 14d, 16d, 18d, 20d }, SelectedItem = preferences.FontSize, MinHeight = 34 }; panel.Children.Add(font);
        var notify = new CheckBox { Content = "提示新增待审批请求", IsChecked = preferences.Notifications, Margin = new Thickness(0, 16, 0, 0) }; panel.Children.Add(notify);
        var error = Label(""); error.SetResourceReference(TextBlock.ForegroundProperty, "DangerBrush"); panel.Children.Add(error);
        var saved = false; var saving = false; var save = Action("保存", "ExecutionPreferencesSave");
        ui.Window.Closing += (_, args) => { if (saving) args.Cancel = true; };
        save.Click += async (_, _) =>
        {
            if (saving) return;
            if (!int.TryParse(days.Text, out var count) || count is < 1 or > 3650) { error.Text = "请输入 1–3650 天。"; return; }
            if (!ToolOutputSettings.TryParse(outputChars.Text, outputEnabled.IsChecked == true, out var output)) { error.Text = "请输入 1,000–100,000 的整数。"; return; }
            var selectedFont = font.SelectedItem is double size ? size : 14; var selectedNotify = notify.IsChecked == true;
            saving = true; save.IsEnabled = false; panel.IsEnabled = false; error.Text = "";
            try
            {
                await saveOutput(output);
                preferences.RetentionDays = count; preferences.FontSize = selectedFont; preferences.Notifications = selectedNotify;
                saved = true;
            }
            catch (Exception exception) when (exception is System.Net.Http.HttpRequestException or System.IO.IOException or JsonException or InvalidOperationException or OperationCanceledException)
            { error.Text = exception.Message; }
            finally { saving = false; save.IsEnabled = true; panel.IsEnabled = true; }
            if (saved) ui.Window.DialogResult = true;
        };
        ui.Actions.Children.Add(save); ui.Window.ShowDialog(); return saved;
    }
}
