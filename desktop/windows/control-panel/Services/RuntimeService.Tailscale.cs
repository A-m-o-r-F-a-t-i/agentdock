using System.Diagnostics;
using System.IO;
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
            var output = await NativeStatusProbe.RunAsync(startInfo, timeout).ConfigureAwait(false);
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
            return ParseTailscaleStatus(await NativeStatusProbe.RunAsync(start, timeout).ConfigureAwait(false));
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
}
