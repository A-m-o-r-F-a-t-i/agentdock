using System.Text.Json;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using AgentDock.ControlPanel;

internal static class PermissionEditorTests
{
    private static IEnumerable<DependencyObject> Tree(DependencyObject root)
    {
        yield return root;
        foreach (var child in LogicalTreeHelper.GetChildren(root).OfType<DependencyObject>())
            foreach (var descendant in Tree(child)) yield return descendant;
    }
    private static T Find<T>(PermissionSettingsEditor editor, string id) where T : DependencyObject =>
        Tree(editor.View).OfType<T>().Single(item => AutomationProperties.GetAutomationId(item) == id);

    internal static void Run(Action<bool, string> check)
    {
        var oldPolicy = JsonSerializer.SerializeToElement(new { global_mode = "rules", scopes = Array.Empty<object>() });
        var editor = new PermissionSettingsEditor(oldPolicy);
        var toggle = Find<CheckBox>(editor, "PermissionSettingsEdit");
        var fields = Find<StackPanel>(editor, "CustomPermissionFields");
        var modeNote = Find<TextBlock>(editor, "CustomPermissionModeNote");
        check(toggle.Content?.ToString() == "启用自定义权限设置", "Custom permission toggle wording is ambiguous.");
        check(modeNote.Text.Contains("未启用自定义权限时，权限由当前执行模式统一控制"), "Execution-mode ownership note is missing.");
        check(toggle.IsChecked == false && fields.Visibility == Visibility.Collapsed, "Legacy policy must default to mode-controlled collapsed settings.");
        var change = new Dictionary<string, object>(); editor.AddChange(change);
        var legacySave = JsonSerializer.SerializeToElement(change);
        check(!legacySave.GetProperty("custom_permissions_enabled").GetBoolean(), "Saving a legacy policy silently enabled custom permissions.");
        check(legacySave.TryGetProperty("settings", out _), "Disabled custom settings must retain explicit historical values.");

        toggle.IsChecked = true;
        check(fields.Visibility == Visibility.Visible, "Enabling custom permissions did not expand the editor.");
        Find<ComboBox>(editor, "PermissionFilesystem").SelectedValue = "read";
        Find<ComboBox>(editor, "PermissionNetwork").SelectedValue = "deny";
        Find<ComboBox>(editor, "PermissionSandboxBoundary").SelectedValue = "workspace";
        Find<ComboBox>(editor, "ApprovalPolicy").SelectedValue = "granular";
        Find<ComboBox>(editor, "ApprovalReviewer").SelectedValue = "auto_review";
        Find<CheckBox>(editor, "ApprovalCategory_file_writes").IsChecked = true;
        change.Clear(); editor.AddChange(change);
        var serialized = JsonSerializer.SerializeToElement(change);
        check(serialized.GetProperty("custom_permissions_enabled").GetBoolean(), "Enabled state not serialized.");
        var json = serialized.GetProperty("settings");
        check(json.GetProperty("permission_profile").GetProperty("filesystem").GetString() == "read", "Filesystem selection not serialized.");
        check(json.GetProperty("permission_profile").GetProperty("network").GetString() == "deny", "Network selection not serialized.");
        check(json.GetProperty("permission_profile").GetProperty("sandbox_boundary").GetString() == "workspace", "Boundary selection not serialized.");
        check(json.GetProperty("approval_reviewer").GetString() == "auto_review", "Reviewer selection not serialized.");
        var gates = json.GetProperty("approval_policy").GetProperty("granular");
        check(gates.GetProperty("file_writes").GetBoolean() && !gates.GetProperty("network").GetBoolean(), "Unchecked granular categories must remain denied.");

        toggle.IsChecked = false;
        check(fields.Visibility == Visibility.Collapsed, "Disabling custom permissions did not collapse the editor.");
        change.Clear(); editor.AddChange(change);
        serialized = JsonSerializer.SerializeToElement(change);
        check(!serialized.GetProperty("custom_permissions_enabled").GetBoolean(), "Disabled state not serialized.");
        check(serialized.GetProperty("settings").GetProperty("permission_profile").GetProperty("filesystem").GetString() == "read", "Disabling erased historical field choices.");
        toggle.IsChecked = true;
        check((string?)Find<ComboBox>(editor, "PermissionFilesystem").SelectedValue == "read", "Toggle round-trip lost field choices.");
        Find<ComboBox>(editor, "ApprovalPolicy").SelectedValue = "never";
        change.Clear(); editor.AddChange(change);
        json = JsonSerializer.SerializeToElement(change).GetProperty("settings").GetProperty("approval_policy");
        check(!json.TryGetProperty("granular", out _), "Non-granular mode must not retain hidden category settings.");

        editor.SelectScope("workspace", "wsp_fixture");
        var inherit = Find<CheckBox>(editor, "PermissionSettingsInherit");
        check(inherit.IsChecked == true && !toggle.IsEnabled && fields.Visibility == Visibility.Collapsed, "Workspace inheritance did not collapse local custom settings.");
        change.Clear(); editor.AddChange(change);
        check(change.TryGetValue("inherit_settings", out var inherited) && inherited is true && !change.ContainsKey("settings"), "Workspace inheritance was not preserved.");
        editor.SelectScope("conversation", "conv_fixture");
        check(!toggle.IsEnabled, "Conversation must not select or broaden a permission profile.");
        check(fields.Visibility == Visibility.Collapsed, "Conversation scope must not expose editable custom fields.");
        change.Clear(); editor.AddChange(change);
        check(change.Count == 0, "Conversation setting produced a profile override.");

        var settings = new { permission_profile = new { filesystem = "deny", network = "deny", sandbox_boundary = "workspace" }, approval_policy = new { mode = "never" }, approval_reviewer = "user" };
        var scopedLegacy = JsonSerializer.SerializeToElement(new { global_mode = "rules", scopes = new[] { new { kind = "workspace", id = "wsp_saved", settings } } });
        var savedEditor = new PermissionSettingsEditor(scopedLegacy); savedEditor.SelectScope("workspace", "wsp_saved");
        check(Find<CheckBox>(savedEditor, "PermissionSettingsEdit").IsChecked == true, "Legacy explicit workspace settings were not inferred as enabled.");
        check((string?)Find<ComboBox>(savedEditor, "PermissionFilesystem").SelectedValue == "deny", "Saved workspace profile was not restored.");
        check(Find<CheckBox>(savedEditor, "PermissionSettingsInherit").IsChecked == false, "Explicit saved profile was replaced by inherited settings.");

        var scopedDisabled = JsonSerializer.SerializeToElement(new {
            global_mode = "full", custom_permissions_enabled = true, settings,
            scopes = new[] { new { kind = "workspace", id = "wsp_disabled", custom_permissions_enabled = false, settings } }
        });
        var disabledEditor = new PermissionSettingsEditor(scopedDisabled); disabledEditor.SelectScope("workspace", "wsp_disabled");
        check(Find<CheckBox>(disabledEditor, "PermissionSettingsEdit").IsChecked == false, "Explicit disabled workspace state was ignored.");
        check(Find<CheckBox>(disabledEditor, "PermissionSettingsInherit").IsChecked == false, "Explicit disabled workspace state was mistaken for inheritance.");
        check(Find<StackPanel>(disabledEditor, "CustomPermissionFields").Visibility == Visibility.Collapsed, "Explicit disabled workspace fields remained expanded.");
        change.Clear(); disabledEditor.AddChange(change);
        check(change.TryGetValue("custom_permissions_enabled", out var disabled) && disabled is false && change.ContainsKey("settings"), "Disabled workspace history was not serialized.");
    }
}
