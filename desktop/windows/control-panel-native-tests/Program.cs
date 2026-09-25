using System.Diagnostics;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Security.Principal;
using System.Text.Json;
using AgentDock.ControlPanel;

internal static class Program
{
    private static int _assertions;
    private static readonly List<object> Evidence = [];
    private static readonly List<string> Failures = [];
    private static void Check(bool value, string message)
    {
        _assertions++;
        if (!value) throw new InvalidOperationException(message);
    }

    [STAThread]
    private static int Main(string[] args)
    {
        if (Environment.GetEnvironmentVariable("GITHUB_ACTIONS") != "true" || Environment.GetEnvironmentVariable("AGENTDOCK_NATIVE_ACCEPTANCE") != "1")
        {
            Console.Error.WriteLine("Native acceptance requires an explicitly enabled isolated GitHub runner.");
            return 2;
        }
        var temp = Environment.GetEnvironmentVariable("RUNNER_TEMP") ?? throw new InvalidOperationException("Missing isolated temp root");
        using var identity = WindowsIdentity.GetCurrent();
        Check(new WindowsPrincipal(identity).IsInRole(WindowsBuiltInRole.Administrator), "Runner administrator token required");
        var root = Path.Combine(temp, "agentdock-native-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        var output = Path.GetFullPath(args.Length == 0 ? "dist/native-validation" : args[0]);
        Directory.CreateDirectory(output);
        var schedulerType = Type.GetTypeFromProgID("Schedule.Service") ?? throw new InvalidOperationException("Native Task Scheduler missing");
        dynamic service = Activator.CreateInstance(schedulerType)!;
        service.Connect();
        dynamic folder = service.GetFolder("\\");
        try
        {
            foreach (var elevated in new[] { true, false })
                foreach (var scenario in new[] { "success", "prepare", "apply", "verify", "cancel", "restore", "verify_restored", "native_unknown", "tampered_definition" })
                {
                    try { RunScenario(service, folder, identity, root, output, scenario, elevated); }
                    catch (Exception error) { Failures.Add($"{scenario}/elevated={elevated}: {error}"); Console.Error.WriteLine(Failures[^1]); }
                }
            foreach (var elevated in new[] { true, false })
            {
                try { RunAbsentTask(service, folder, identity, root, elevated); }
                catch (Exception error) { Failures.Add($"absent/elevated={elevated}: {error}"); Console.Error.WriteLine(Failures[^1]); }
            }
            var report = new
            {
                assertions = _assertions, platform = RuntimeInformation.OSDescription,
                architecture = RuntimeInformation.ProcessArchitecture.ToString(), isolation = "GitHub hosted runner",
                scheduler = "native COM", recovery_files = "native NTFS", ui_started = false,
                production_runtime_started = false, scenarios = Evidence, failures = Failures
            };
            File.WriteAllText(Path.Combine(output, "native-privilege-validation.json"), JsonSerializer.Serialize(report, new JsonSerializerOptions { WriteIndented = true }));
            Console.WriteLine($"Native privilege recovery: {_assertions} assertions, {Evidence.Count} completed scenarios, {Failures.Count} failures.");
            return Failures.Count == 0 ? 0 : 1;
        }
        finally
        {
            Marshal.FinalReleaseComObject(folder);
            Marshal.FinalReleaseComObject(service);
            if (Directory.Exists(root)) Directory.Delete(root, true);
        }
    }

    private static dynamic? FindTask(dynamic folder, string name)
    {
        try { return folder.GetTask(name); }
        catch (Exception error) when ((uint)error.HResult is 0x80070002 or 0x80070003) { return null; }
    }

    private static void RemoveFixtureTask(dynamic folder, string name)
    {
        if (!name.StartsWith("AgentDock-Acceptance-", StringComparison.Ordinal)) throw new InvalidOperationException("Non-fixture task refused");
        dynamic? task = FindTask(folder, name);
        if (task is not null)
        {
            if (Convert.ToInt32(task.State) == 4) task.Stop(0);
            folder.DeleteTask(name, 0);
        }
        Check(FindTask(folder, name) is null, "Isolated task removed");
    }

    private static void Native(string kind, string name, string directory, string recovery, WindowsIdentity identity)
    {
        var result = TaskAdminService.Run(["--task-admin", kind, "--task-name", name, "--backup-directory", recovery,
            "--runtime-root", directory, "--launcher-path", Path.Combine(directory, "fixture-never-started.exe"),
            "--user-sid", identity.User!.Value, "--user-name", identity.Name]);
        if (result != 0) throw new IOException("Native TaskAdmin " + kind + " failed");
    }

    private static void RunAbsentTask(dynamic service, dynamic folder, WindowsIdentity identity, string root, bool elevated)
    {
        var name = "AgentDock-Acceptance-" + Guid.NewGuid().ToString("N");
        var directory = Path.Combine(root, "absent-" + elevated);
        Directory.CreateDirectory(directory);
        var recovery = Path.Combine(directory, "recovery");
        try
        {
            Check(FindTask(folder, name) is null, "No original task in absent fixture");
            Native(elevated ? "prepare-elevated" : "prepare-standard", name, directory, recovery, identity);
            TaskAdminService.VerifyCurrentTask(name, elevated, false);
            Native("restore", name, directory, recovery, identity);
            TaskAdminService.VerifyRestoredBackup(name, recovery);
            Check(FindTask(folder, name) is null, "Restore must preserve original task absence");
            Evidence.Add(new { scenario = "absent_task", elevated, restored = true });
        }
        finally { RemoveFixtureTask(folder, name); }
    }

    private static void RunScenario(dynamic service, dynamic folder, WindowsIdentity identity, string root, string output, string scenario, bool elevated)
    {
        var label = (elevated ? "elevated-" : "standard-") + scenario;
        Console.WriteLine("Native scenario: " + label);
        var name = "AgentDock-Acceptance-" + Guid.NewGuid().ToString("N");
        var directory = Path.Combine(root, label);
        Directory.CreateDirectory(directory);
        var recovery = Path.Combine(directory, "recovery");
        var manifest = Path.Combine(directory, "runtime-state.json");
        File.WriteAllText(manifest, "original");
        dynamic definition = service.NewTask(0);
        definition.Principal.UserId = identity.User!.Value;
        definition.Principal.LogonType = 3;
        definition.Principal.RunLevel = elevated ? 0 : 1;
        definition.Settings.Enabled = false;
        dynamic action = definition.Actions.Create(0);
        action.Path = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System), "cmd.exe");
        action.Arguments = "/d /c exit 0";
        dynamic original = folder.RegisterTaskDefinition(name, definition, 6, identity.User.Value, null, 3, null);
        original.Enabled = false;
        string oldXml = original.Xml;
        int restoreCount = 0;
        bool injectedBoundaryReached = false;
        Exception? failure = null;
        Process? unknown = null;
        using var cancelled = new CancellationTokenSource();
        try
        {
            var actions = new PrivilegeTransitionActions(
                async token =>
                {
                    Native(elevated ? "prepare-elevated" : "prepare-standard", name, directory, recovery, identity);
                    TaskAdminService.VerifyCurrentTask(name, elevated, false);
                    Check(File.Exists(Path.Combine(recovery, "state.json")) && File.Exists(Path.Combine(recovery, "task.xml")), "Native backup precedes mutations");
                    if (scenario == "native_unknown")
                    {
                        var start = new ProcessStartInfo("powershell.exe") { UseShellExecute = false, CreateNoWindow = true };
                        foreach (var value in new[] { "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 120" }) start.ArgumentList.Add(value);
                        unknown = Process.Start(start) ?? throw new IOException("Could not create isolated native waiting process");
                        injectedBoundaryReached = true;
                        cancelled.Cancel();
                        var method = typeof(RuntimeService).GetMethod("WaitForNativeExitAsync", BindingFlags.NonPublic | BindingFlags.Static)!;
                        await (Task)method.Invoke(null, [unknown, token])!;
                    }
                    if (scenario == "cancel") { injectedBoundaryReached = true; cancelled.Cancel(); token.ThrowIfCancellationRequested(); }
                    if (scenario == "prepare") { injectedBoundaryReached = true; throw new IOException("Injected after real native preparation"); }
                },
                token =>
                {
                    RecoveryFiles.WriteText(manifest, "new");
                    if (scenario is "apply" or "restore" or "verify_restored" or "tampered_definition")
                    {
                        injectedBoundaryReached = scenario == "apply";
                        throw new IOException("Injected manifest boundary failure");
                    }
                    return Task.CompletedTask;
                },
                token =>
                {
                    TaskAdminService.VerifyCurrentTask(name, elevated, false);
                    Check(File.ReadAllText(manifest) == "new", "Forward manifest state");
                    if (scenario == "verify") { injectedBoundaryReached = true; throw new IOException("Injected forward verification failure"); }
                    return Task.CompletedTask;
                },
                token =>
                {
                    restoreCount++;
                    Check(!token.IsCancellationRequested, "Recovery token independent of user cancellation");
                    if (scenario == "restore") { injectedBoundaryReached = true; throw new IOException("Injected native restore failure"); }
                    Native("restore", name, directory, recovery, identity);
                    RecoveryFiles.WriteText(manifest, "original");
                    if (scenario == "tampered_definition")
                    {
                        dynamic restored = folder.GetTask(name);
                        dynamic altered = restored.Definition;
                        altered.Actions.Item(1).Arguments = "/d /c exit 7";
                        folder.RegisterTaskDefinition(name, altered, 6, identity.User.Value, null, 3, null);
                        injectedBoundaryReached = true;
                    }
                    return Task.CompletedTask;
                },
                token =>
                {
                    TaskAdminService.VerifyRestoredBackup(name, recovery);
                    Check(File.ReadAllText(manifest) == "original", "Original manifest restored");
                    if (scenario == "verify_restored") { injectedBoundaryReached = true; throw new IOException("Injected recovered verification failure"); }
                    return Task.CompletedTask;
                },
                () => File.Exists(Path.Combine(recovery, "state.json")));
            try { PrivilegeTransition.RunAsync(recovery, actions, cancelled.Token).GetAwaiter().GetResult(); }
            catch (Exception error) { failure = error; }
            if (failure is not null && File.Exists(Path.Combine(recovery,"state.json")))
            {
                using var state=JsonDocument.Parse(File.ReadAllText(Path.Combine(recovery,"state.json")));
                dynamic? retainedTask=FindTask(folder,name);
                string? actualSecurity=retainedTask is null ? null : (string)retainedTask.GetSecurityDescriptor(4);
                var expectedSecurity=state.RootElement.GetProperty("SecurityDescriptor").GetString();
                File.WriteAllText(Path.Combine(output,label+"-acl-difference.json"),JsonSerializer.Serialize(new{expected=expectedSecurity,actual=actualSecurity}));
            }
            Check((failure is null) == (scenario == "success"), "Native outcome " + label + ": " + failure);
            if (scenario != "success") Check(injectedBoundaryReached, "Expected native failure boundary not reached: " + failure);
            var retained = scenario is "native_unknown" or "restore" or "verify_restored" or "tampered_definition";
            Check(Directory.Exists(recovery) == retained, "Native evidence cleanup " + label + ": " + failure);
            if (scenario == "native_unknown") Check(restoreCount == 0 && unknown is { HasExited: false }, "No conflicting recovery while actual process remains alive");
            if (scenario == "cancel") Check(failure is OperationCanceledException && restoreCount == 1, "Cancellation survives actual native restoration");
            if (retained)
                foreach (var filename in new[] { "task.xml", "state.json", "transition.json" })
                {
                    Check(File.Exists(Path.Combine(recovery, filename)), "Retained native recovery file " + filename);
                    File.Copy(Path.Combine(recovery, filename), Path.Combine(output, label + "-" + filename), true);
                }
            if (!retained && scenario != "success")
            {
                dynamic restored = folder.GetTask(name);
                Check(!(bool)restored.Enabled && Convert.ToInt32(restored.Definition.Principal.RunLevel) == (elevated ? 0 : 1) &&
                    (string)restored.Definition.Actions.Item(1).Arguments == "/d /c exit 0", "Actual task policy and action restored");
            }
            Evidence.Add(new { scenario, elevated, restored = restoreCount, retained, expected_failure = failure is not null, original_task_bytes = oldXml.Length, process_exit_unknown = scenario == "native_unknown" });
        }
        finally
        {
            if (unknown is not null)
            {
                if (!unknown.HasExited) { unknown.Kill(true); unknown.WaitForExit(10000); }
                unknown.Dispose();
            }
            RemoveFixtureTask(folder, name);
        }
    }
}
