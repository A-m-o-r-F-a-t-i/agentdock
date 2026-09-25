using System.IO;
using System.Xml;
using System.Xml.Linq;

namespace AgentDock.ControlPanel;

// Setup's explicit administrative adapter validates old definitions before
// changing them. This does not participate in normal runtime task control.
internal static class TaskDefinitionPolicy
{
    internal static void Validate(string xml, string taskName, string root, string sid, Func<string,string> resolveSid)
    {
        if (taskName != "AgentDock" || !Path.IsPathFullyQualified(root) || string.IsNullOrWhiteSpace(sid))
            throw new InvalidOperationException("task_owner_mismatch: task name, runtime root or user is not valid.");
        using var reader = XmlReader.Create(new StringReader(xml), new XmlReaderSettings { DtdProcessing = DtdProcessing.Prohibit, XmlResolver = null, MaxCharactersInDocument = 65536 });
        var document = XDocument.Load(reader);
        XNamespace ns = "http://schemas.microsoft.com/windows/2004/02/mit/task";
        if (document.Root?.Name != ns + "Task") throw new InvalidOperationException("task_owner_mismatch: unsupported task XML.");
        var principals = document.Root.Element(ns+"Principals")?.Elements().ToArray() ?? [];
        var actions = document.Root.Element(ns+"Actions")?.Elements().ToArray() ?? [];
        if (principals.Length != 1 || principals[0].Name != ns+"Principal" || actions.Length != 1 || actions[0].Name != ns+"Exec")
            throw new InvalidOperationException("task_owner_mismatch: expected one principal and one executable action.");
        var principal = principals[0]; var action = actions[0];
        if (!string.Equals(resolveSid(principal.Element(ns+"UserId")?.Value ?? ""), sid, StringComparison.OrdinalIgnoreCase) ||
            principal.Element(ns+"LogonType")?.Value != "InteractiveToken" || principal.Element(ns+"RunLevel")?.Value != "HighestAvailable")
            throw new InvalidOperationException("task_owner_mismatch: principal, session type or privilege differs.");
        var executable = action.Element(ns+"Command")?.Value ?? "";
        var arguments = action.Element(ns+"Arguments")?.Value ?? "";
        var directory = action.Element(ns+"WorkingDirectory")?.Value ?? "";
        if (!Path.IsPathFullyQualified(executable) || directory.Length > 0 && !Same(directory,root))
            throw new InvalidOperationException("task_owner_mismatch: executable or working directory is not owned.");
        var canonicalRoot = Path.GetFullPath(root).TrimEnd(Path.DirectorySeparatorChar);
        var native = Same(executable,Path.Combine(root,"bin","agentdock-tray.exe")) &&
            string.Equals(arguments,$"--run-core-task --runtime-root \"{canonicalRoot}\"",StringComparison.OrdinalIgnoreCase);
        var legacy = Same(executable,Path.Combine(root,"bin","agentdock.exe")) &&
            string.Equals(arguments,$"service launch-core --runtime-root \"{canonicalRoot}\"",StringComparison.OrdinalIgnoreCase);
        if (!native && !legacy) throw new InvalidOperationException("task_owner_mismatch: action is not an exact known stable AgentDock entry.");
    }
    internal static bool Same(string left, string right) => Path.IsPathFullyQualified(left) && Path.IsPathFullyQualified(right) &&
        string.Equals(Path.GetFullPath(left).TrimEnd(Path.DirectorySeparatorChar),Path.GetFullPath(right).TrimEnd(Path.DirectorySeparatorChar),StringComparison.OrdinalIgnoreCase);
}
