using System.Text.Json;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using CheckBox = System.Windows.Controls.CheckBox;
using ComboBox = System.Windows.Controls.ComboBox;

namespace AgentDock.ControlPanel;

internal sealed class PermissionSettingsEditor
{
    private readonly JsonElement _policy;
    private readonly CheckBox _custom = new() { Content = "启用自定义权限设置", Margin = new Thickness(0, 14, 0, 6) };
    private readonly CheckBox _inherit = new() { Content = "继承全局自定义权限设置", Margin = new Thickness(0, 6, 0, 6) };
    private readonly StackPanel _fields = new();
    private readonly StackPanel _categories = new();
    private readonly ComboBox _filesystem;
    private readonly ComboBox _network;
    private readonly ComboBox _boundary;
    private readonly ComboBox _approval;
    private readonly ComboBox _reviewer;
    private readonly Dictionary<string, CheckBox> _gates = new();
    private string _scope = "global";
    internal StackPanel View { get; } = new();

    internal PermissionSettingsEditor(JsonElement policy)
    {
        _policy = policy;
        var modeNote = Note("未启用自定义权限时，权限由当前执行模式统一控制；已保存的三层设置会保留，但不参与权限判定。");
        AutomationProperties.SetAutomationId(_custom, "PermissionSettingsEdit");
        AutomationProperties.SetAutomationId(modeNote, "CustomPermissionModeNote");
        AutomationProperties.SetAutomationId(_inherit, "PermissionSettingsInherit");
        AutomationProperties.SetAutomationId(_fields, "CustomPermissionFields");
        View.Children.Add(_custom); View.Children.Add(modeNote); View.Children.Add(_inherit); View.Children.Add(_fields);
        _filesystem = Choice("Filesystem · 文件系统", "PermissionFilesystem", [new("deny", "deny · 禁止文件读写"), new("read", "read · 只允许读取"), new("write", "write · 允许读写")]);
        _network = Choice("Network · 网络", "PermissionNetwork", [new("allow", "allow · 允许联网工具"), new("deny", "deny · 拒绝联网及无法约束的工具")]);
        _boundary = Choice("Sandbox boundary · 准入边界", "PermissionSandboxBoundary", [new("none", "none · 无额外工作区边界"), new("workspace", "workspace · 仅固定工作区目标")]);
        _fields.Children.Add(Note("这是工具准入边界，不是操作系统沙箱。受限配置下，不透明命令、第三方 MCP 和 WSL 会被拒绝，不尝试提升权限执行。"));
        _approval = Choice("Approval Policy · 审批策略", "ApprovalPolicy", [new("on-request", "on-request · 需要确认时申请审批"), new("never", "never · 不受理审批，需要确认的操作直接拒绝"), new("granular", "granular · 按类别决定是否受理审批")]);
        _fields.Children.Add(_categories);
        foreach (var (key, label) in new[] { ("file_writes", "文件写入"), ("commands", "命令与进程控制"), ("network", "网络"), ("mcp", "第三方 MCP"), ("management", "管理操作"), ("other", "其他操作") })
        {
            var box = new CheckBox { Content = "允许发起审批：" + label, Margin = new Thickness(8, 3, 0, 3) };
            AutomationProperties.SetAutomationId(box, "ApprovalCategory_" + key);
            _gates.Add(key, box); _categories.Children.Add(box);
        }
        _reviewer = Choice("Approval Reviewer · 审批主体", "ApprovalReviewer", [new("user", "user · 用户审批"), new("auto_review", "auto_review · 独立审查器")]);
        _fields.Children.Add(Note("auto_review 需要管理员配置 auto-review.json 与可信独立审查程序；未配置、超时、无效返回或拒绝均不执行。它不能越过 Profile 或创建永久授权。"));
        _custom.Checked += (_, _) => UpdateEnabled(); _custom.Unchecked += (_, _) => UpdateEnabled();
        _inherit.Checked += (_, _) => UpdateEnabled(); _inherit.Unchecked += (_, _) => UpdateEnabled();
        _approval.SelectionChanged += (_, _) => UpdateEnabled();
        SelectScope("global", "");
    }

    private static TextBlock Note(string text) => new() { Text = text, TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 7, 0, 6) };
    private ComboBox Choice(string title, string id, ExecutionChoice[] values)
    {
        _fields.Children.Add(Note(title));
        var box = new ComboBox { ItemsSource = values, DisplayMemberPath = "Title", SelectedValuePath = "Id", MinHeight = 34 };
        AutomationProperties.SetAutomationId(box, id); _fields.Children.Add(box); return box;
    }
    private static string Value(ComboBox box) => (box.SelectedItem as ExecutionChoice)?.Id ?? "";
    private static void Select(ComboBox box, string value, string fallback) => box.SelectedValue = value.Length == 0 ? fallback : value;
    private static bool TryFlag(JsonElement source, string name, out bool value)
    {
        var field = source.Field(name);
        if (field.ValueKind == JsonValueKind.True) { value = true; return true; }
        if (field.ValueKind == JsonValueKind.False) { value = false; return true; }
        value = false; return false;
    }
    private static bool LegacyCustomState(JsonElement source, JsonElement settings) =>
        TryFlag(source, "custom_permissions_enabled", out var enabled) ? enabled : settings.ValueKind == JsonValueKind.Object;

    internal void SelectScope(string scope, string id)
    {
        _scope = scope;
        var globalSettings = _policy.Field("settings");
        var settings = globalSettings;
        var customEnabled = LegacyCustomState(_policy, globalSettings);
        var overridden = false;
        foreach (var existing in _policy.Array("scopes"))
        {
            if (existing.Text("kind") != scope || existing.Text("id") != id) continue;
            var localSettings = existing.Field("settings");
            var hasSettings = localSettings.ValueKind == JsonValueKind.Object;
            var hasFlag = TryFlag(existing, "custom_permissions_enabled", out var localEnabled);
            if (hasSettings) { settings = localSettings; overridden = true; }
            if (hasFlag) { customEnabled = localEnabled; overridden = true; }
            else if (hasSettings) customEnabled = true;
        }
        _inherit.IsChecked = scope == "workspace" && !overridden;
        _custom.IsChecked = customEnabled;
        var profile = settings.Field("permission_profile");
        Select(_filesystem, profile.Text("filesystem"), "write");
        Select(_network, profile.Text("network"), "allow");
        Select(_boundary, profile.Text("sandbox_boundary"), "none");
        Select(_approval, settings.Field("approval_policy").Text("mode"), "on-request");
        Select(_reviewer, settings.Text("approval_reviewer"), "user");
        var categories = settings.Field("approval_policy").Field("granular");
        foreach (var (key, box) in _gates) box.IsChecked = categories.Flag(key);
        UpdateEnabled();
    }

    private void UpdateEnabled()
    {
        var inheriting = _scope == "workspace" && _inherit.IsChecked == true;
        _inherit.Visibility = _scope == "workspace" ? Visibility.Visible : Visibility.Collapsed;
        _inherit.IsEnabled = _scope == "workspace";
        _custom.IsEnabled = _scope != "conversation" && !inheriting;
        var showFields = _scope != "conversation" && _custom.IsChecked == true && !inheriting;
        _fields.Visibility = showFields ? Visibility.Visible : Visibility.Collapsed;
        _fields.IsEnabled = _scope != "conversation" && !inheriting;
        if (_approval is not null) _categories.Visibility = showFields && Value(_approval) == "granular" ? Visibility.Visible : Visibility.Collapsed;
    }

    internal void AddChange(Dictionary<string, object> change)
    {
        if (_scope == "conversation") return;
        if (_scope == "workspace" && _inherit.IsChecked == true) { change["inherit_settings"] = true; return; }
        change["custom_permissions_enabled"] = _custom.IsChecked == true;
        var approval = new Dictionary<string, object> { ["mode"] = Value(_approval) };
        if (Value(_approval) == "granular") approval["granular"] = _gates.ToDictionary(item => item.Key, item => item.Value.IsChecked == true);
        change["settings"] = new Dictionary<string, object> {
            ["permission_profile"] = new { filesystem = Value(_filesystem), network = Value(_network), sandbox_boundary = Value(_boundary) },
            ["approval_policy"] = approval, ["approval_reviewer"] = Value(_reviewer)
        };
    }
}
