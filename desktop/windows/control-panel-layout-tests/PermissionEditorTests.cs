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
        var change = new Dictionary<string, object>(); editor.AddChange(change);
        check(change.Count == 0, "Opening legacy settings must not silently migrate or broaden permissions.");
        Find<CheckBox>(editor, "PermissionSettingsEdit").IsChecked = true;
        Find<ComboBox>(editor, "PermissionFilesystem").SelectedValue = "read";
        Find<ComboBox>(editor, "PermissionNetwork").SelectedValue = "deny";
        Find<ComboBox>(editor, "PermissionSandboxBoundary").SelectedValue = "workspace";
        Find<ComboBox>(editor, "ApprovalPolicy").SelectedValue = "granular";
        Find<ComboBox>(editor, "ApprovalReviewer").SelectedValue = "auto_review";
        Find<CheckBox>(editor, "ApprovalCategory_file_writes").IsChecked = true;
        editor.AddChange(change);
        var json = JsonSerializer.SerializeToElement(change).GetProperty("settings");
        check(json.GetProperty("permission_profile").GetProperty("filesystem").GetString() == "read", "Filesystem selection not serialized.");
        check(json.GetProperty("permission_profile").GetProperty("network").GetString() == "deny", "Network selection not serialized.");
        check(json.GetProperty("permission_profile").GetProperty("sandbox_boundary").GetString() == "workspace", "Boundary selection not serialized.");
        check(json.GetProperty("approval_reviewer").GetString() == "auto_review", "Reviewer selection not serialized.");
        var gates = json.GetProperty("approval_policy").GetProperty("granular");
        check(gates.GetProperty("file_writes").GetBoolean() && !gates.GetProperty("network").GetBoolean(), "Unchecked granular categories must remain denied.");
        Find<ComboBox>(editor, "ApprovalPolicy").SelectedValue = "never";
        change.Clear(); editor.AddChange(change);
        json = JsonSerializer.SerializeToElement(change).GetProperty("settings").GetProperty("approval_policy");
        check(!json.TryGetProperty("granular", out _), "Non-granular mode must not retain hidden category settings.");
        editor.SelectScope("workspace", "wsp_fixture");
        change.Clear(); editor.AddChange(change);
        check(change.Count == 0, "Selecting a workspace must not save a profile automatically.");
        Find<CheckBox>(editor, "PermissionSettingsEdit").IsChecked = true;
        change.Clear(); editor.AddChange(change);
        check(change.TryGetValue("inherit_settings", out var inherited) && inherited is true && !change.ContainsKey("settings"), "Workspace inheritance was not preserved.");
        editor.SelectScope("conversation", "conv_fixture");
        check(!Find<CheckBox>(editor, "PermissionSettingsEdit").IsEnabled, "Conversation must not select or broaden a permission profile.");
        change.Clear(); editor.AddChange(change);
        check(change.Count == 0, "Conversation setting produced a profile override.");

        var settings = new { permission_profile = new { filesystem = "deny", network = "deny", sandbox_boundary = "workspace" }, approval_policy = new { mode = "never" }, approval_reviewer = "user" };
        var scoped = JsonSerializer.SerializeToElement(new { global_mode = "rules", scopes = new[] { new { kind = "workspace", id = "wsp_saved", settings } } });
        var savedEditor = new PermissionSettingsEditor(scoped); savedEditor.SelectScope("workspace", "wsp_saved");
        check((string?)Find<ComboBox>(savedEditor, "PermissionFilesystem").SelectedValue == "deny", "Saved workspace profile was not restored.");
        check(Find<CheckBox>(savedEditor, "PermissionSettingsInherit").IsChecked == false, "Explicit saved profile was replaced by inherited settings.");
    }
}
