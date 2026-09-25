using System.Security.AccessControl;

namespace AgentDock.ControlPanel;

internal enum TaskSecurityMatch { Exact, CanonicalizedAllowEntries, Mismatch }

// Registered tasks are leaf securable objects. Windows may convert a legacy
// all-Allow DACL to automatic inheritance and stably move explicit ACEs first.
// This predicate accepts only that known transformation with IDENTICAL ACE
// bytes, multiplicity and relative order. It is not a general ACL equivalence
// routine and must never be used for filesystem backup metadata comparison.
internal static class TaskSecurityDescriptor
{
    internal static TaskSecurityMatch Compare(string expected, string actual)
    {
        var original = new RawSecurityDescriptor(expected);
        var restored = new RawSecurityDescriptor(actual);
        if (original.GetSddlForm(AccessControlSections.All) == restored.GetSddlForm(AccessControlSections.All))
            return TaskSecurityMatch.Exact;
        if (!Equals(original.Owner, restored.Owner) || !Equals(original.Group, restored.Group) ||
            original.SystemAcl is not null || restored.SystemAcl is not null)
            return TaskSecurityMatch.Mismatch;
        var delta = original.ControlFlags ^ restored.ControlFlags;
        var automatic = ControlFlags.DiscretionaryAclAutoInherited;
        // No loss of any control bit, no change of protection, presence,
        // inheritance request, or defaulting. Only the provider's AI marker may
        // be added, and only with a byte-exact stable partition of Allow ACEs.
        if (delta != 0 && (delta != automatic || (restored.ControlFlags & automatic) == 0))
            return TaskSecurityMatch.Mismatch;
        var before = original.DiscretionaryAcl;
        var after = restored.DiscretionaryAcl;
        if (before is null || after is null || before.Count == 0 || before.Count > 1024 ||
            before.Count != after.Count || before.Revision != after.Revision)
            return TaskSecurityMatch.Mismatch;
        var entries = new List<GenericAce>(before.Count);
        var inherited = false;
        foreach (GenericAce ace in before)
        {
            if (!SimpleAllow(ace)) return TaskSecurityMatch.Mismatch;
            inherited |= (ace.AceFlags & AceFlags.Inherited) != 0;
            if ((ace.AceFlags & AceFlags.Inherited) == 0) entries.Add(ace);
        }
        if (!inherited) return TaskSecurityMatch.Mismatch;
        foreach (GenericAce ace in before)
            if ((ace.AceFlags & AceFlags.Inherited) != 0) entries.Add(ace);
        for (var index = 0; index < entries.Count; index++)
        {
            if (!SimpleAllow(after[index]) || !Identical(entries[index], after[index]))
                return TaskSecurityMatch.Mismatch;
        }
        return TaskSecurityMatch.CanonicalizedAllowEntries;
    }

    private static bool SimpleAllow(GenericAce ace) =>
        ace is CommonAce { AceQualifier: AceQualifier.AccessAllowed, IsCallback: false } &&
        (ace.AceFlags & ~AceFlags.Inherited) == 0;

    private static bool Identical(GenericAce left, GenericAce right)
    {
        if (left.BinaryLength != right.BinaryLength) return false;
        var expected = new byte[left.BinaryLength];
        var actual = new byte[right.BinaryLength];
        left.GetBinaryForm(expected, 0); right.GetBinaryForm(actual, 0);
        return expected.AsSpan().SequenceEqual(actual);
    }
}
