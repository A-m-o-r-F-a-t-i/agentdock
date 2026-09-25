using System.Security.AccessControl;
using System.IO;
using System.Reflection;
using System.Text.Json;
using AgentDock.ControlPanel;

internal static class TaskSecurityDescriptorTests
{
    internal static void Run(Action<bool, string> check)
    {
        const string old = "D:(A;ID;0x1f019f;;;BA)(A;ID;0x1f019f;;;SY)(A;ID;FA;;;BA)(A;;FR;;;LA)";
        const string canonical = "D:AI(A;;FR;;;LA)(A;ID;0x1f019f;;;BA)(A;ID;0x1f019f;;;SY)(A;ID;FA;;;BA)";
        check(TaskSecurityDescriptor.Compare(old, old) == TaskSecurityMatch.Exact, "Exact descriptors remain exact");
        check(TaskSecurityDescriptor.Compare(old, canonical) == TaskSecurityMatch.CanonicalizedAllowEntries, "Only documented stable all-Allow partition accepted");
        check(TaskSecurityDescriptor.Compare(canonical, old) == TaskSecurityMatch.Mismatch, "Automatic inheritance marker cannot be removed");
        foreach (var changed in new[] {
            canonical.Replace("FR;;;LA", "FA;;;LA"),
            canonical.Replace("FR;;;LA", "FR;;;WD"),
            canonical.Replace("D:AI", "D:PAI"),
            canonical.Replace("D:AI", "D:ARAI"),
            canonical.Replace("A;ID;FA", "A;;FA"),
            canonical.Replace("(A;ID;FA;;;BA)", ""),
            canonical + "(A;;FR;;;LA)",
            canonical.Replace("(A;ID;0x1f019f;;;BA)(A;ID;0x1f019f;;;SY)", "(A;ID;0x1f019f;;;SY)(A;ID;0x1f019f;;;BA)"),
            canonical.Replace("(A;;FR;;;LA)", "(D;;FR;;;LA)"),
            canonical.Replace("(A;;FR;;;LA)", "(A;IO;FR;;;LA)"),
            "O:BA" + canonical,
        }) check(TaskSecurityDescriptor.Compare(old, changed) == TaskSecurityMatch.Mismatch, "Mutation must not be accepted as normalization: " + changed);
        const string denyOld = "D:(A;ID;FR;;;BA)(D;;FW;;;AN)(A;;FA;;;SY)";
        const string denyReordered = "D:AI(D;;FW;;;AN)(A;;FA;;;SY)(A;ID;FR;;;BA)";
        check(TaskSecurityDescriptor.Compare(denyOld, denyOld) == TaskSecurityMatch.Exact, "Exact mixed ACL remains supported");
        check(TaskSecurityDescriptor.Compare(denyOld, denyReordered) == TaskSecurityMatch.Mismatch, "Even apparently harmless Deny reordering is outside equivalence contract");
        check(TaskSecurityDescriptor.Compare("D:NO_ACCESS_CONTROL", "D:") == TaskSecurityMatch.Mismatch, "Null and empty DACL are never equivalent");
        check(TaskSecurityDescriptor.Compare("D:", "D:AI") == TaskSecurityMatch.Mismatch, "Empty ACL cannot use the normalization exception");
        check(TaskSecurityDescriptor.Compare("D:(A;CIID;FR;;;BA)(A;;FA;;;SY)", "D:AI(A;;FA;;;SY)(A;CIID;FR;;;BA)") == TaskSecurityMatch.Mismatch, "Container inheritance is not a registered-task leaf exception");
        check(TaskSecurityDescriptor.Compare("D:(OA;ID;FR;00000000-0000-0000-0000-000000000001;;BA)(A;;FA;;;SY)", "D:AI(A;;FA;;;SY)(OA;ID;FR;00000000-0000-0000-0000-000000000001;;BA)") == TaskSecurityMatch.Mismatch, "Object ACEs require exact restoration");
        var original = new RawSecurityDescriptor(old).DiscretionaryAcl!;
        var restored = new RawSecurityDescriptor(canonical).DiscretionaryAcl!;
        var sids = original.Cast<CommonAce>().Select(ace => ace.SecurityIdentifier.Value).Distinct().ToArray();
        // Exhaust all SID-membership combinations for the actual runner case.
        // For unconditional Allow ACEs the granted mask is their union; no
        // Deny or callback is admitted by the production exception.
        for (var subset = 0; subset < (1 << sids.Length); subset++)
        {
            var members = sids.Where((_, index) => (subset & (1 << index)) != 0).ToHashSet();
            uint Mask(RawAcl acl) => acl.Cast<CommonAce>().Where(ace => members.Contains(ace.SecurityIdentifier.Value)).Aggregate(0u, (mask, ace) => mask | unchecked((uint)ace.AccessMask));
            check(Mask(original) == Mask(restored), "Every membership subset retains the exact granted mask");
        }
        // Exercise each access bit independently: no bit change can slip
        // through the byte comparison even if another ACE grants that bit.
        for (var bit = 0; bit < 32; bit++)
        {
            var descriptor = new RawSecurityDescriptor(canonical);
            var ace = (CommonAce)descriptor.DiscretionaryAcl![0];
            ace.AccessMask ^= unchecked((int)(1u << bit));
            check(TaskSecurityDescriptor.Compare(old, descriptor.GetSddlForm(AccessControlSections.Access)) == TaskSecurityMatch.Mismatch, "Every changed access bit is rejected");
        }
        var directory = Path.Combine(Path.GetTempPath(), "workbench-backup-contract-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        try
        {
            var read = typeof(TaskAdminService).GetMethod("ReadBackup", BindingFlags.Static | BindingFlags.NonPublic)!;
            foreach (var version in new[] { 0, 2 })
            {
                var state = Path.Combine(directory, "state.json");
                File.WriteAllText(state, JsonSerializer.Serialize(new { SchemaVersion = version, Exists = true }));
                var rejected = false;
                try { read.Invoke(null, [directory]); }
                catch (TargetInvocationException error) when (error.InnerException is IOException e && e.Message.Contains("旧备份")) { rejected = true; }
                check(rejected && File.Exists(state), "Missing task security metadata is refused before any scheduler action");
            }
        }
        finally { Directory.Delete(directory, true); }
    }
}
