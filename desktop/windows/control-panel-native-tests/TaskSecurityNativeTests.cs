using System.IO;
using System.Security.Principal;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private static void RunDaclRestoration(dynamic service, dynamic folder, WindowsIdentity identity, string root)
    {
        var name = "AgentDock-Acceptance-" + Guid.NewGuid().ToString("N");
        var directory = Path.Combine(root, "security-contract");
        Directory.CreateDirectory(directory);
        dynamic definition = service.NewTask(0);
        definition.Principal.UserId = identity.User!.Value;
        definition.Principal.LogonType = 3;
        definition.Settings.Enabled = false;
        dynamic action = definition.Actions.Create(0);
        action.Path = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System), "cmd.exe");
        action.Arguments = "/d /c exit 0";
        try
        {
            dynamic original = folder.RegisterTaskDefinition(name, definition, 6 | 0x10 | 0x20, identity.User.Value, null, 3,
                $"D:P(D;;FW;;;AN)(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;{identity.User.Value})");
            original.Enabled = false;
            var recovery = Path.Combine(directory, "recovery");
            Native("prepare-standard", name, directory, recovery, identity);
            Native("restore", name, directory, recovery, identity);
            var match = TaskAdminService.VerifyRestoredBackup(name, recovery);
            Check(match == TaskSecurityMatch.Exact, "Canonical task with an explicit Deny must restore exactly; no reordering exception applies");
            dynamic restored = folder.GetTask(name);
            string security = restored.GetSecurityDescriptor(4);
            restored.SetSecurityDescriptor(security + "(A;;FR;;;WD)", 0x10);
            var rejected = false;
            try { TaskAdminService.VerifyRestoredBackup(name, recovery); }
            catch (IOException) { rejected = true; }
            Check(rejected && File.Exists(Path.Combine(recovery, "state.json")), "Additional native grant must fail verification and retain recovery evidence");
            Evidence.Add(new { scenario = "native_dacl_tampering", exact_deny_restore = true, changed_grant_rejected = true });
        }
        finally { RemoveFixtureTask(folder, name); }
    }
}
