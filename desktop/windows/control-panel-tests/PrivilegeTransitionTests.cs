using AgentDock.ControlPanel;
using System.Text;

internal static class PrivilegeTransitionTests
{
    internal static async Task Run(Action<bool, string> check)
    {
        foreach (var scenario in new[] { "success", "prepare", "apply", "verify", "cancel", "native_unknown", "restore", "verify_restored", "no_backup", "unchanged_unknown" })
        {
            var directory = Path.Combine(Path.GetTempPath(), "agentdock-privilege-fixture-" + Guid.NewGuid().ToString("N"));
            using var cancellation = new CancellationTokenSource();
            var restored = 0; var verified = 0; Exception? failure = null;
            Task Fail(string stage) => throw new IOException("synthetic " + stage);
            var actions = new PrivilegeTransitionActions(
                token =>
                {
                    if (scenario is "no_backup" or "unchanged_unknown") return Fail("no_backup");
                    RecoveryFiles.WriteText(Path.Combine(directory, "task.xml"), "<Task>中文😀</Task>", Encoding.Unicode);
                    RecoveryFiles.WriteText(Path.Combine(directory, "state.json"), "{\"synthetic\":true}");
                    check(File.ReadAllText(Path.Combine(directory, "task.xml")) == "<Task>中文😀</Task>", "UTF16 backup remains readable");
                    if (scenario == "native_unknown") throw new NativeProcessStateUnknownException(123, new OperationCanceledException());
                    if (scenario == "cancel") { cancellation.Cancel(); token.ThrowIfCancellationRequested(); }
                    return scenario == "prepare" ? Fail("prepare") : Task.CompletedTask;
                },
                token => scenario is "apply" or "restore" or "verify_restored" ? Fail("apply") : Task.CompletedTask,
                token =>
                {
                    check(Directory.Exists(directory), "backup retained until forward verification");
                    return scenario == "verify" ? Fail("verify") : Task.CompletedTask;
                },
                token =>
                {
                    restored++;
                    check(!token.IsCancellationRequested, "recovery does not inherit user cancellation");
                    check(File.Exists(Path.Combine(directory, "task.xml")), "recovery materials still present");
                    return scenario == "restore" ? Fail("restore") : Task.CompletedTask;
                },
                token =>
                {
                    verified++;
                    check(Directory.Exists(directory), "backup retained until restoration verification");
                    return scenario is "verify_restored" or "unchanged_unknown" ? Fail("verify_restored") : Task.CompletedTask;
                },
                () => File.Exists(Path.Combine(directory, "state.json")));
            try
            {
                try { await PrivilegeTransition.RunAsync(directory, actions, cancellation.Token); }
                catch (Exception error) { failure = error; }
                check((failure is null) == (scenario == "success"), "forward outcome " + scenario);
                var retained = scenario is "native_unknown" or "restore" or "verify_restored" or "unchanged_unknown";
                check(Directory.Exists(directory) == retained, "cleanup only after verified terminal state " + scenario);
                if (retained) check(File.Exists(Path.Combine(directory, "transition.json")), "recoverable phase persisted " + scenario);
                if (scenario == "native_unknown") check(restored == 0 && verified == 0, "no conflicting rollback while native state unknown");
                if (scenario == "cancel") check(failure is OperationCanceledException && restored == 1, "cancel is preserved after independent successful recovery");
                if (scenario is "no_backup" or "unchanged_unknown") check(restored == 0 && verified == 1, "no-backup launch failure requires unchanged-state verification");
            }
            finally { if (Directory.Exists(directory)) Directory.Delete(directory, true); }
        }
    }
}
