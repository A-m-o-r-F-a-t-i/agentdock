using System.Diagnostics;
using System.IO;
using System.Text;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed partial class RuntimeService
{
    private readonly SemaphoreSlim _tailscaleProbeGate = new(1, 1);
    private readonly TailscaleObservationCache _tailscaleObservations = new();
    private readonly FunnelVerificationService _funnelVerification = new();
    private readonly object _tailscaleRefreshGate = new();
    private Task _tailscaleRefresh = Task.CompletedTask;
    private readonly CancellationTokenSource _tailscaleLifetime = new();

    private void InvalidateTailscaleStatus()
    {
        _tailscaleObservations.Invalidate();
        _funnelVerification.Cancel();
    }

    private async Task<NativeTunnelStatus> CachedTailscaleStatusAsync(string publicUrl, string localOrigin, CancellationToken token)
    {
        var identity = await ReadTailscaleIdentityAsync(token).ConfigureAwait(false);
        var generation = _tailscaleObservations.Bind(identity);
        var snapshot = _tailscaleObservations.Read(identity, generation);
        lock (_tailscaleRefreshGate)
        {
            if (!_tailscaleLifetime.IsCancellationRequested && _tailscaleRefresh.IsCompleted &&
                (snapshot is null || DateTimeOffset.UtcNow - snapshot.CheckedAt > TimeSpan.FromSeconds(10)))
            {
                _tailscaleRefresh = Task.Run(async () =>
                {
                    try { await ReadTailscaleStatusAsync(false, _tailscaleLifetime.Token).ConfigureAwait(false); }
                    catch (OperationCanceledException) when (_tailscaleLifetime.IsCancellationRequested) { }
                    catch (Exception) { } // A failed refresh cannot publish against another binding.
                });
            }
        }
        if (identity.Active && TailscaleObservationIdentity.SameOrigin(identity.PublicOrigin, publicUrl) &&
            TailscaleObservationIdentity.SameOrigin(identity.LocalOrigin, localOrigin) && snapshot is not null &&
            DateTimeOffset.UtcNow - snapshot.CheckedAt <= TimeSpan.FromSeconds(10)) return snapshot.Value;
        return new NativeTunnelStatus
        {
            Provider = "tailscale", Mode = "funnel", Phase = "CheckingLocal", PublicUrl = publicUrl, LocalOrigin = localOrigin,
            Diagnostic = "正在核对本地配置，界面可继续使用。"
        };
    }

    public async Task<NativeTunnelStatus> ReadTailscaleStatusAsync(bool force = false, CancellationToken cancellationToken = default)
    {
        var requestedAt = DateTimeOffset.UtcNow;
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _tailscaleLifetime.Token);
        timeout.CancelAfter(TimeSpan.FromSeconds(8));
        await _tailscaleProbeGate.WaitAsync(timeout.Token).ConfigureAwait(false);
        try
        {
            var identity = await ReadTailscaleIdentityAsync(timeout.Token).ConfigureAwait(false);
            var generation = _tailscaleObservations.Bind(identity);
            var cached = _tailscaleObservations.Read(identity, generation);
            if (cached is not null && ((!force && DateTimeOffset.UtcNow - cached.CheckedAt < TimeSpan.FromSeconds(10)) || cached.CheckedAt >= requestedAt)) return cached.Value;
            var order = _tailscaleObservations.NextOrder();
            var status = await QueryTailscaleAsync("status", identity, timeout, cancellationToken).ConfigureAwait(false);
            _tailscaleObservations.Publish(identity, generation, order, status, DateTimeOffset.UtcNow);
            if (identity.Active && status.LocalReady)
            {
                long verificationOrder = 0;
                _ = _funnelVerification.Ensure(JsonSerializer.Serialize(identity) + "|" + generation,
                    async token =>
                    {
                        // Local and public observations share a gate. A delayed local
                        // read cannot overwrite a newer completed public failure.
                        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(token, _tailscaleLifetime.Token);
                        deadline.CancelAfter(TimeSpan.FromSeconds(8));
                        await _tailscaleProbeGate.WaitAsync(deadline.Token).ConfigureAwait(false);
                        try
                        {
                            verificationOrder = _tailscaleObservations.NextOrder();
                            return await QueryTailscaleAsync("verify", identity, deadline, token).ConfigureAwait(false);
                        }
                        finally { _tailscaleProbeGate.Release(); }
                    },
                    result => _tailscaleObservations.Publish(identity, generation, verificationOrder, result, DateTimeOffset.UtcNow));
            }
            return status;
        }
        finally { _tailscaleProbeGate.Release(); }
    }

    private async Task<NativeTunnelStatus> QueryTailscaleAsync(string operation, TailscaleObservationIdentity identity,
        CancellationTokenSource timeout, CancellationToken caller)
    {
        try
        {
            if (identity != await ReadTailscaleIdentityAsync(timeout.Token).ConfigureAwait(false))
                throw new InvalidDataException("Tailscale configuration changed before observation.");
            var binary = await ResolveCoreBinaryAsync(timeout.Token).ConfigureAwait(false);
            var start = CreateRedirectedProcessStartInfo(binary);
            foreach (var argument in new[] { "tunnel", operation, "--runtime-root", RuntimeRoot }) start.ArgumentList.Add(argument);
            if (operation == "status") { start.ArgumentList.Add("--provider"); start.ArgumentList.Add("tailscale"); }
            var status = ParseTailscaleStatus(await RunBoundedTailscaleProbeAsync(start, timeout).ConfigureAwait(false));
            if (identity != await ReadTailscaleIdentityAsync(timeout.Token).ConfigureAwait(false) || !identity.Matches(status))
                throw new InvalidDataException("Tailscale observation does not match the current configuration.");
            return status;
        }
        catch (OperationCanceledException) when (caller.IsCancellationRequested || _tailscaleLifetime.IsCancellationRequested) { throw; }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or JsonException or InvalidOperationException or System.ComponentModel.Win32Exception or OperationCanceledException)
        { return FailedProbe(error); }
    }

    private async Task<TailscaleObservationIdentity> ReadTailscaleIdentityAsync(CancellationToken token)
    {
        string Stamp(string name)
        {
            var file = new FileInfo(Path.Combine(RuntimeRoot, name));
            return name + "|" + (file.Exists ? file.Length + "|" + file.LastWriteTimeUtc.Ticks + "|" + file.CreationTimeUtc.Ticks : "missing");
        }
        string Revision() => string.Join(";", new[] { "runtime.json", "control-panel-settings.json", "active-version.json", "cloudflared-mode.txt", "server-url.txt" }.Select(Stamp));
        var revision = Revision();
        var manifest = await ReadRuntimeManifestAsync(token).ConfigureAwait(false) ?? throw new InvalidDataException("Runtime manifest is missing.");
        var configuration = "missing";
        var path = Path.Combine(RuntimeRoot, "tailscale-funnel-state.json");
        try
        {
            await using var stream = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete, 4096, FileOptions.Asynchronous);
            var bytes = new byte[16385]; var length = 0;
            while (length < bytes.Length)
            {
                var read = await stream.ReadAsync(bytes.AsMemory(length), token).ConfigureAwait(false);
                if (read == 0) break;
                length += read;
            }
            if (length > 16384) throw new InvalidDataException("Tailscale ownership exceeds the size limit.");
            using var doc = JsonDocument.Parse(bytes.AsMemory(0, length), new JsonDocumentOptions { MaxDepth = 8 });
            var root = doc.RootElement;
            // Read only configuration identity, never verification timestamps: the
            // public query updates those itself without creating a new binding.
            configuration = JsonSerializer.Serialize(new[] { "schema_version", "managed", "enabled", "configured_at", "device_id", "dns_name", "public_origin", "local_origin" }
                .Select(key => root.TryGetProperty(key, out var value) ? value.GetRawText() : "missing").ToArray());
        }
        catch (FileNotFoundException) { }
        catch (DirectoryNotFoundException) { }
        if (revision != Revision()) throw new InvalidDataException("Runtime configuration changed while reading.");
        var active = manifest.PublicAccessProvider == "tailscale";
        var origin = active ? manifest.PublicAccessUrl : "";
        if (active && string.IsNullOrEmpty(origin)) origin = manifest.PublicUrl;
        return new(manifest.PublicAccessProvider, active ? "funnel" : manifest.TunnelMode,
            origin.TrimEnd('/'), $"http://127.0.0.1:{manifest.ListenPort}", revision + "|" + configuration);
    }

    private static NativeTunnelStatus ParseTailscaleStatus(string output) => TailscaleObservationCache.Parse(output);

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
