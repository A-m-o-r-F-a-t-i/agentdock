using System.IO;
using System.Text;
using System.Text.Json;

namespace AgentDock.ControlPanel;

// A native process may outlive cancellation. Never start conflicting rollback
// until its exit is known; retaining the recovery directory is intentional.
public sealed class NativeProcessStateUnknownException(int processId, Exception inner)
    : Exception($"原生进程 {processId} 的退出状态尚未确认，未启动冲突恢复。", inner)
{
    public int ProcessId { get; } = processId;
}

public sealed record PrivilegeTransitionActions(
    Func<CancellationToken, Task> Prepare,
    Func<CancellationToken, Task> Apply,
    Func<CancellationToken, Task> Verify,
    Func<CancellationToken, Task> Restore,
    Func<CancellationToken, Task> VerifyRestored,
    Func<bool> CanRestore);

public static class RecoveryFiles
{
    public static void WriteText(string path, string text, Encoding? encoding = null)
    {
        var temporary = path + ".tmp." + Guid.NewGuid().ToString("N");
        try
        {
            using (var stream = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None,
                       4096, FileOptions.WriteThrough))
            {
                encoding ??= new UTF8Encoding(false);
                stream.Write(encoding.GetPreamble());
                var bytes = encoding.GetBytes(text);
                stream.Write(bytes); stream.Flush(flushToDisk: true);
            }
            File.Move(temporary, path, overwrite: true);
        }
        finally
        {
            try { File.Delete(temporary); }
            catch (IOException) { }
            catch (UnauthorizedAccessException) { }
        }
    }
}

public static class PrivilegeTransition
{
    public static async Task RunAsync(string recoveryDirectory, PrivilegeTransitionActions actions,
        CancellationToken cancellationToken = default)
    {
        cancellationToken.ThrowIfCancellationRequested();
        Directory.CreateDirectory(recoveryDirectory);
        var clean = false;
        void Record(string phase, Exception? error = null) => RecoveryFiles.WriteText(
            Path.Combine(recoveryDirectory, "transition.json"), JsonSerializer.Serialize(new
            {
                schema_version = 1, phase, updated_at = DateTimeOffset.UtcNow,
                error = error?.Message,
                native_process_id = (error as NativeProcessStateUnknownException)?.ProcessId
            }));
        void TryRecord(string phase, Exception error)
        {
            try { Record(phase, error); }
            catch (Exception writeError) when (writeError is IOException or UnauthorizedAccessException)
            { error.Data["recovery_record_error"] = writeError.Message; }
        }
        try
        {
            Record("preparing");
            await actions.Prepare(cancellationToken);
            Record("task_prepared");
            cancellationToken.ThrowIfCancellationRequested();
            await actions.Apply(cancellationToken);
            Record("manifest_applied");
            await actions.Verify(cancellationToken);
            Record("committed");
            clean = true;
        }
        catch (Exception transitionError)
        {
            transitionError.Data["recovery_directory"] = recoveryDirectory;
            if (transitionError is NativeProcessStateUnknownException)
            {
                TryRecord("native_state_unknown", transitionError);
                throw new IOException($"权限切换状态未知；恢复材料保留在 {recoveryDirectory}", transitionError);
            }
            if (!actions.CanRestore())
            {
                // The native helper publishes state.json before its first
                // mutation. A declined launch can be cleaned only after the
                // unchanged original state has also been verified.
                using var unchanged = new CancellationTokenSource(TimeSpan.FromMinutes(3));
                try
                {
                    await actions.VerifyRestored(unchanged.Token);
                    Record("restored"); clean = true;
                }
                catch (Exception verificationError)
                {
                    TryRecord("preparation_failed", verificationError);
                    throw new AggregateException($"权限切换准备未完成，原状态尚未验证；材料保留在 {recoveryDirectory}", transitionError, verificationError);
                }
                throw;
            }
            // User cancellation stops new forward changes, not required recovery.
            using var recovery = new CancellationTokenSource(TimeSpan.FromMinutes(3));
            try
            {
                Record("restoring");
                await actions.Restore(recovery.Token);
                await actions.VerifyRestored(recovery.Token);
                Record("restored");
                clean = true;
            }
            catch (Exception rollbackError)
            {
                TryRecord(rollbackError is NativeProcessStateUnknownException ? "native_state_unknown" : "recovery_required", rollbackError);
                throw new AggregateException($"权限切换与恢复均未完成；恢复材料保留在 {recoveryDirectory}", transitionError, rollbackError);
            }
            throw;
        }
        finally
        {
            if (clean)
            {
                try { Directory.Delete(recoveryDirectory, recursive: true); }
                catch (IOException) { /* Verified state is safe; retain cleanup material. */ }
                catch (UnauthorizedAccessException) { }
            }
        }
    }
}
