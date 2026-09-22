using System.Diagnostics;
using System.IO;
using System.Text;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed partial class RuntimeService
{
    private readonly SemaphoreSlim _tailscaleProbeGate = new(1, 1);
    private sealed record TailscaleCache(NativeTunnelStatus Value, DateTimeOffset CheckedAt);
    private volatile TailscaleCache? _tailscaleCache;
    private readonly FunnelVerificationService _funnelVerification = new();
    private readonly object _tailscaleRefreshGate = new();
    private Task _tailscaleRefresh = Task.CompletedTask;
    private readonly CancellationTokenSource _tailscaleLifetime = new();
    private long _tailscaleGeneration;

    private void InvalidateTailscaleStatus()
    {
        Interlocked.Increment(ref _tailscaleGeneration);
        _tailscaleCache = null;
        _funnelVerification.Cancel();
    }

    private NativeTunnelStatus CachedTailscaleStatus(string publicUrl)
    {
        var snapshot = _tailscaleCache;
        lock (_tailscaleRefreshGate)
        {
            if (!_tailscaleLifetime.IsCancellationRequested && _tailscaleRefresh.IsCompleted &&
                (snapshot is null || DateTimeOffset.UtcNow - snapshot.CheckedAt > TimeSpan.FromSeconds(10)))
            {
                _tailscaleRefresh = Task.Run(async () =>
                {
                    try { await ReadTailscaleStatusAsync(false, _tailscaleLifetime.Token).ConfigureAwait(false); }
                    catch (OperationCanceledException) when (_tailscaleLifetime.IsCancellationRequested) { }
                    catch (Exception error)
                    {
                        _tailscaleCache = new(FailedProbe(error), DateTimeOffset.UtcNow);
                    }
                });
            }
        }
        return snapshot?.Value ?? new NativeTunnelStatus
        {
            Provider = "tailscale", Mode = "funnel", Phase = "CheckingLocal", PublicUrl = publicUrl,
            Diagnostic = "正在核对本地配置，界面可继续使用。"
        };
    }

    public async Task<NativeTunnelStatus> ReadTailscaleStatusAsync(bool force = false, CancellationToken cancellationToken = default)
    {
        var requestedAt = DateTimeOffset.UtcNow;
        var generation = Interlocked.Read(ref _tailscaleGeneration);
        await _tailscaleProbeGate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            var cached = _tailscaleCache;
            if (cached is not null && ((!force && DateTimeOffset.UtcNow - cached.CheckedAt < TimeSpan.FromSeconds(10)) || cached.CheckedAt >= requestedAt)) return cached.Value;
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _tailscaleLifetime.Token);
            timeout.CancelAfter(TimeSpan.FromSeconds(8));
            var binary = await ResolveCoreBinaryAsync(timeout.Token).ConfigureAwait(false);
            var startInfo = CreateRedirectedProcessStartInfo(binary);
            foreach (var argument in new[] { "tunnel", "status", "--runtime-root", RuntimeRoot, "--provider", "tailscale" }) startInfo.ArgumentList.Add(argument);
            var output = await RunBoundedTailscaleProbeAsync(startInfo, timeout).ConfigureAwait(false);
            var status = ParseTailscaleStatus(output);
            if (generation != Interlocked.Read(ref _tailscaleGeneration)) return status;
            _tailscaleCache = new(status, DateTimeOffset.UtcNow);
            if (status.LocalReady)
            {
                _ = _funnelVerification.Ensure(status.PublicUrl + "|" + status.LocalOrigin, ProbeTailscalePublicAsync,
                    result => { if (generation == Interlocked.Read(ref _tailscaleGeneration)) _tailscaleCache = new(result, DateTimeOffset.UtcNow); });
            }
            return status;
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested || _tailscaleLifetime.IsCancellationRequested) { throw; }
        catch (Exception error) when (error is IOException or JsonException or InvalidOperationException or System.ComponentModel.Win32Exception or OperationCanceledException)
        {
            var status = FailedProbe(error);
            if (generation == Interlocked.Read(ref _tailscaleGeneration)) _tailscaleCache = new(status, DateTimeOffset.UtcNow);
            return status;
        }
        finally { _tailscaleProbeGate.Release(); }
    }

    private async Task<NativeTunnelStatus> ProbeTailscalePublicAsync(CancellationToken cancellationToken)
    {
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _tailscaleLifetime.Token);
        timeout.CancelAfter(TimeSpan.FromSeconds(8));
        try
        {
            var binary = await ResolveCoreBinaryAsync(timeout.Token).ConfigureAwait(false);
            var start = CreateRedirectedProcessStartInfo(binary);
            foreach (var argument in new[] { "tunnel", "verify", "--runtime-root", RuntimeRoot }) start.ArgumentList.Add(argument);
            return ParseTailscaleStatus(await RunBoundedTailscaleProbeAsync(start, timeout).ConfigureAwait(false));
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested || _tailscaleLifetime.IsCancellationRequested) { throw; }
        catch (Exception error) when (error is IOException or JsonException or InvalidOperationException or System.ComponentModel.Win32Exception or OperationCanceledException) { return FailedProbe(error); }
    }

    private static NativeTunnelStatus ParseTailscaleStatus(string output)
    {
        var status = JsonSerializer.Deserialize<NativeTunnelStatus>(output, JsonOptions) ?? throw new InvalidDataException(UiText.Get("RuntimeApiEmptyResponse"));
        if (status.Provider != "tailscale" || status.Mode != "funnel") throw new InvalidDataException(UiText.Get("RuntimeApiInvalidResponse"));
        return status;
    }

    private static NativeTunnelStatus FailedProbe(Exception error) => new()
    {
        Provider = "tailscale", Mode = "funnel", Phase = "Degraded", DiagnosticCode = "probe_failed",
        Diagnostic = "公网验证暂未完成，本地配置未撤销：" + (error is OperationCanceledException ? UiText.Get("AccessTimeout") : error.Message)
    };

    private static async Task<string> RunBoundedTailscaleProbeAsync(ProcessStartInfo startInfo, CancellationTokenSource timeout)
    {
        using var process = Process.Start(startInfo) ?? throw new InvalidOperationException(UiText.Get("ManagerStartFailed"));
        var stdout = ReadBoundedProbeTextAsync(process.StandardOutput, 1024 * 1024, timeout);
        var stderr = ReadBoundedProbeTextAsync(process.StandardError, 16 * 1024, timeout);
        try
        {
            await Task.WhenAll(process.WaitForExitAsync(timeout.Token), stdout, stderr).ConfigureAwait(false);
            if (process.ExitCode != 0)
            {
                var errorText = await stderr.ConfigureAwait(false);
                throw new InvalidOperationException(string.IsNullOrWhiteSpace(errorText) ? UiText.Format("ManagerFailedWithExitCode", process.ExitCode) : errorText.Trim());
            }
            return await stdout.ConfigureAwait(false);
        }
        finally
        {
            if (!process.HasExited)
            {
                process.Kill(entireProcessTree: true);
                using var cleanup = new CancellationTokenSource(TimeSpan.FromSeconds(3));
                await process.WaitForExitAsync(cleanup.Token).ConfigureAwait(false);
            }
        }
    }

    private static async Task<string> ReadBoundedProbeTextAsync(StreamReader reader, int limit, CancellationTokenSource timeout)
    {
        var result = new StringBuilder();
        var buffer = new char[4096];
        int count;
        while ((count = await reader.ReadAsync(buffer.AsMemory(), timeout.Token).ConfigureAwait(false)) != 0)
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
