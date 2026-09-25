using System.ComponentModel;
using System.IO;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed partial class RuntimeService
{
    private readonly SemaphoreSlim _nativeStatusGate = new(1, 1);

    private async Task<T?> ReadNativeStatusAsync<T>(string binary, string command,
        Func<string, T> parse, CancellationToken cancellationToken) where T : class
    {
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _tailscaleLifetime.Token);
        timeout.CancelAfter(TimeSpan.FromSeconds(8));
        var entered = false;
        try
        {
            await _nativeStatusGate.WaitAsync(timeout.Token).ConfigureAwait(false);
            entered = true;
            var start = CreateRedirectedProcessStartInfo(binary);
            foreach (var argument in new[] { command, "status", "--runtime-root", RuntimeRoot })
                start.ArgumentList.Add(argument);
            return parse(await NativeStatusProbe.RunAsync(start, timeout).ConfigureAwait(false));
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested || _tailscaleLifetime.IsCancellationRequested) { throw; }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or JsonException or
            InvalidOperationException or Win32Exception or OperationCanceledException)
        {
            // Query failure is unknown, not a fabricated stopped/false observation.
            return null;
        }
        finally { if (entered) _nativeStatusGate.Release(); }
    }
}
