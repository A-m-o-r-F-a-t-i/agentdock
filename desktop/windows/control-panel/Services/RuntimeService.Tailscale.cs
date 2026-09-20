using System.Diagnostics;
using System.IO;
using System.Text;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed partial class RuntimeService
{
    private readonly SemaphoreSlim _tailscaleProbeGate = new(1, 1);
    private NativeTunnelStatus? _tailscaleProbe;
    private DateTimeOffset _tailscaleProbeAt = DateTimeOffset.MinValue;

    private void InvalidateTailscaleStatus() => _tailscaleProbeAt = DateTimeOffset.MinValue;

    public async Task<NativeTunnelStatus> ReadTailscaleStatusAsync(
        bool force = false, CancellationToken cancellationToken = default)
    {
        await _tailscaleProbeGate.WaitAsync(cancellationToken);
        try
        {
            if (!force && _tailscaleProbe is not null &&
                DateTimeOffset.UtcNow - _tailscaleProbeAt < TimeSpan.FromSeconds(10))
            {
                return _tailscaleProbe;
            }
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
            timeout.CancelAfter(TimeSpan.FromSeconds(25));
            var binary = await ResolveCoreBinaryAsync(timeout.Token);
            var startInfo = CreateRedirectedProcessStartInfo(binary);
            foreach (var argument in new[] { "tunnel", "status", "--runtime-root", RuntimeRoot, "--provider", "tailscale" })
            {
                startInfo.ArgumentList.Add(argument);
            }
            var output = await RunBoundedTailscaleProbeAsync(startInfo, timeout);
            _tailscaleProbe = JsonSerializer.Deserialize<NativeTunnelStatus>(output, JsonOptions)
                ?? throw new InvalidDataException(UiText.Get("RuntimeApiEmptyResponse"));
            if (_tailscaleProbe.Provider != "tailscale" || _tailscaleProbe.Mode != "funnel")
            {
                throw new InvalidDataException(UiText.Get("RuntimeApiInvalidResponse"));
            }
            _tailscaleProbeAt = DateTimeOffset.UtcNow;
            return _tailscaleProbe;
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            throw;
        }
        catch (Exception ex) when (ex is IOException or JsonException or InvalidOperationException or System.ComponentModel.Win32Exception or OperationCanceledException)
        {
            _tailscaleProbe = new NativeTunnelStatus
            {
                Provider = "tailscale", Mode = "funnel", DiagnosticCode = "probe_failed",
                Diagnostic = ex is OperationCanceledException ? UiText.Get("AccessTimeout") : ex.Message
            };
            _tailscaleProbeAt = DateTimeOffset.UtcNow;
            return _tailscaleProbe;
        }
        finally
        {
            _tailscaleProbeGate.Release();
        }
    }

    private static async Task<string> RunBoundedTailscaleProbeAsync(ProcessStartInfo startInfo, CancellationTokenSource timeout)
    {
        using var process = Process.Start(startInfo)
            ?? throw new InvalidOperationException(UiText.Get("ManagerStartFailed"));
        var stdout = ReadBoundedProbeTextAsync(process.StandardOutput, 1024 * 1024, timeout);
        var stderr = ReadBoundedProbeTextAsync(process.StandardError, 16 * 1024, timeout);
        try
        {
            await Task.WhenAll(process.WaitForExitAsync(timeout.Token), stdout, stderr);
            if (process.ExitCode != 0)
            {
                throw new InvalidOperationException(string.IsNullOrWhiteSpace(stderr.Result)
                    ? UiText.Format("ManagerFailedWithExitCode", process.ExitCode) : stderr.Result.Trim());
            }
            return stdout.Result;
        }
        finally
        {
            if (!process.HasExited)
            {
                process.Kill(entireProcessTree: true);
                using var cleanup = new CancellationTokenSource(TimeSpan.FromSeconds(3));
                await process.WaitForExitAsync(cleanup.Token);
            }
        }
    }

    private static async Task<string> ReadBoundedProbeTextAsync(StreamReader reader, int limit, CancellationTokenSource timeout)
    {
        var result = new StringBuilder();
        var buffer = new char[4096];
        int count;
        while ((count = await reader.ReadAsync(buffer.AsMemory(), timeout.Token)) != 0)
        {
            if (result.Length + count > limit)
            {
                timeout.Cancel();
                throw new InvalidDataException(UiText.Get("TailscaleOutputTooLarge"));
            }
            result.Append(buffer, 0, count);
        }
        return result.ToString();
    }
}
