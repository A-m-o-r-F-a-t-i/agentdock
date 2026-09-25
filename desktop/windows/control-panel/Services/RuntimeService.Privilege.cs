using System.ComponentModel;
using System.Diagnostics;
using System.IO;
using System.Security.AccessControl;
using System.Security.Principal;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed partial class RuntimeService
{
    private readonly SemaphoreSlim _privilegeGate = new(1, 1);

    public async Task SetPrivilegeModeAsync(bool elevated, CancellationToken cancellationToken = default)
    {
        await _privilegeGate.WaitAsync(cancellationToken);
        try { await SetPrivilegeModeCoreAsync(elevated, cancellationToken); }
        finally { _privilegeGate.Release(); }
    }

    private async Task SetPrivilegeModeCoreAsync(bool elevated, CancellationToken cancellationToken)
    {
        var recoveryRoot = Path.Combine(RuntimeRoot, "recovery");
        Directory.CreateDirectory(recoveryRoot);
        using var transitionLock = new FileStream(Path.Combine(recoveryRoot, "privilege.lock"),
            FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.None);
        if (Directory.Exists(recoveryRoot))
            foreach (var directory in Directory.EnumerateDirectories(recoveryRoot, "privilege-*"))
            {
                var statePath = Path.Combine(directory, "transition.json");
                if (!File.Exists(statePath)) throw new IOException($"存在待核对的权限恢复材料：{directory}");
                if (new FileInfo(statePath).Length > 65536) throw new IOException($"权限恢复状态文件无效：{directory}");
                using var document = JsonDocument.Parse(await File.ReadAllTextAsync(statePath, cancellationToken));
                var phase = document.RootElement.TryGetProperty("phase", out var value) ? value.GetString() : null;
                if (phase is not ("committed" or "restored"))
                    throw new IOException($"前次权限转换尚未确认终态，请先恢复：{directory}");
            }
        var manifest = await ReadRuntimeManifestAsync(cancellationToken)
            ?? throw new InvalidOperationException(UiText.Get("RuntimeJsonMissing"));
        var wasElevated = string.Equals(manifest.PrivilegeMode, "elevated", StringComparison.OrdinalIgnoreCase);
        if (wasElevated == elevated) return;
        var snapshot = await GetSnapshotAsync(cancellationToken);
        var backupDirectory = Path.Combine(recoveryRoot, $"privilege-{Guid.NewGuid():N}");
        Directory.CreateDirectory(backupDirectory);
        using (var identity = WindowsIdentity.GetCurrent())
        {
            var owner = identity.User ?? throw new IOException("无法确定恢复材料的 Windows 所有者。");
            var security = new DirectorySecurity();
            security.SetAccessRuleProtection(isProtected: true, preserveInheritance: false);
            foreach (var sid in new[] { owner, new SecurityIdentifier(WellKnownSidType.LocalSystemSid, null), new SecurityIdentifier(WellKnownSidType.BuiltinAdministratorsSid, null) })
                security.AddAccessRule(new FileSystemAccessRule(sid, FileSystemRights.FullControl,
                    InheritanceFlags.ContainerInherit | InheritanceFlags.ObjectInherit, PropagationFlags.None, AccessControlType.Allow));
            new DirectoryInfo(backupDirectory).SetAccessControl(security);
        }
        await PrivilegeTransition.RunAsync(backupDirectory, new PrivilegeTransitionActions(
            Prepare: token => RunTaskAdminTransitionAsync(elevated ? "prepare-elevated" : "prepare-standard", manifest, backupDirectory, token),
            Apply: async token =>
            {
                await WritePrivilegeModeAsync(elevated, token, manifest.AgentDockTaskName);
                if (elevated)
                {
                    SetStandardCoreStartup(manifest, enabled: false);
                    if (snapshot.CoreStartupEnabled || snapshot.CoreRunning) await SetStartupAsync("core", true, token);
                    if (snapshot.CoreRunning) await RunCoreActionAsync("start", token);
                    if (!snapshot.CoreStartupEnabled) await SetStartupAsync("core", false, token);
                }
                else
                {
                    SetStandardCoreStartup(manifest, snapshot.CoreStartupEnabled);
                    if (snapshot.CoreRunning) await RunCoreActionAsync("start", token);
                }
            },
            Verify: token => VerifyPrivilegeStateAsync(elevated, snapshot.CoreStartupEnabled, snapshot.CoreRunning, manifest, null, token),
            Restore: async token =>
            {
                await RunTaskAdminTransitionAsync("restore", manifest, backupDirectory, token);
                await WritePrivilegeModeAsync(wasElevated, token, manifest.AgentDockTaskName);
                SetStandardCoreStartup(manifest, !wasElevated && snapshot.CoreStartupEnabled);
                if (!wasElevated && snapshot.CoreRunning) await RunCoreActionAsync("start", token);
            },
            VerifyRestored: token => VerifyPrivilegeStateAsync(wasElevated, snapshot.CoreStartupEnabled, snapshot.CoreRunning, manifest,
                File.Exists(Path.Combine(backupDirectory, "state.json")) ? backupDirectory : null, token),
            CanRestore: () => File.Exists(Path.Combine(backupDirectory, "state.json"))), cancellationToken);
    }

    private static async Task WaitForNativeExitAsync(Process process, CancellationToken cancellationToken)
    {
        try { await process.WaitForExitAsync(cancellationToken); }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            using var settle = new CancellationTokenSource(TimeSpan.FromSeconds(30));
            try { await process.WaitForExitAsync(settle.Token); }
            catch (Exception error) when (error is OperationCanceledException or Win32Exception or InvalidOperationException)
            { throw new NativeProcessStateUnknownException(process.Id, error); }
            throw;
        }
        catch (Exception error) when (error is Win32Exception or InvalidOperationException)
        { throw new NativeProcessStateUnknownException(process.Id, error); }
    }

    private async Task VerifyPrivilegeStateAsync(bool elevated, bool startupEnabled, bool running,
        RuntimeManifest original, string? backupDirectory, CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        var current = await ReadRuntimeManifestAsync(token)
            ?? throw new IOException("无法验证权限切换后的 runtime.json。");
        if (!string.Equals(current.PrivilegeMode, elevated ? "elevated" : "standard", StringComparison.OrdinalIgnoreCase))
            throw new IOException("权限模式未达到预期，保留恢复材料。");
        var taskName = string.IsNullOrWhiteSpace(original.AgentDockTaskName) ? "AgentDock" : original.AgentDockTaskName.Trim();
        await Task.Run(() =>
        {
            if (backupDirectory is null) TaskAdminService.VerifyCurrentTask(taskName, elevated, startupEnabled);
            else TaskAdminService.VerifyRestoredBackup(taskName, backupDirectory);
        }, token);
        if (!elevated && IsRunValuePresent(current.StartupValueName, "AgentDock") != startupEnabled)
            throw new IOException("普通权限启动项未达到预期。");
        var binary = await ResolveCoreBinaryAsync(token);
        using var verification = CancellationTokenSource.CreateLinkedTokenSource(token);
        verification.CancelAfter(TimeSpan.FromSeconds(30));
        while (true)
        {
            var start = CreateRedirectedProcessStartInfo(binary);
            foreach (var argument in new[] { "service", "status", "--runtime-root", RuntimeRoot }) start.ArgumentList.Add(argument);
            using var document = JsonDocument.Parse(await RunProcessAsync(start, verification.Token));
            if (!document.RootElement.TryGetProperty("running", out var actual) || actual.ValueKind is not (JsonValueKind.True or JsonValueKind.False))
                throw new IOException("Core 状态响应无法验证，未清理恢复材料。");
            if (actual.GetBoolean() == running) return;
            await Task.Delay(200, verification.Token);
        }
    }
}
