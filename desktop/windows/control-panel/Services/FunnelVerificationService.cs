using System.Diagnostics;

namespace AgentDock.ControlPanel;

internal sealed class FunnelVerificationService : IDisposable
{
    private readonly object _gate = new();
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    private CancellationTokenSource? _current;
    private Task _work = Task.CompletedTask;
    private string _identity = "";
    private DateTimeOffset _completedAt = DateTimeOffset.MinValue;
    private long _generation;
    private bool _disposed;

    internal FunnelVerificationService(Func<TimeSpan, CancellationToken, Task>? delay = null) => _delay = delay ?? Task.Delay;

    internal Task Ensure(string identity, Func<CancellationToken, Task<NativeTunnelStatus>> probe, Action<NativeTunnelStatus> report)
    {
        lock (_gate)
        {
            if (_disposed) return Task.CompletedTask;
            if (_identity == identity && (!_work.IsCompleted || DateTimeOffset.UtcNow - _completedAt < TimeSpan.FromMinutes(5))) return _work;
            _current?.Cancel();
            var generation = ++_generation;
            _identity = identity;
            var cancellation = new CancellationTokenSource(TimeSpan.FromMinutes(10));
            _current = cancellation;
            _work = Task.Run(() => RunAsync(generation, cancellation, probe, report));
            return _work;
        }
    }

    private async Task RunAsync(long generation, CancellationTokenSource cancellation, Func<CancellationToken, Task<NativeTunnelStatus>> probe, Action<NativeTunnelStatus> report)
    {
        using (cancellation)
        {
            try
            {
                for (var attempt = 0; attempt < 12; attempt++)
                {
                    cancellation.Token.ThrowIfCancellationRequested();
                    var started = Stopwatch.GetTimestamp();
                    var result = await probe(cancellation.Token).ConfigureAwait(false);
                    result.PublicProbeMs ??= (long)Stopwatch.GetElapsedTime(started).TotalMilliseconds;
                    lock (_gate)
                    {
                        if (_disposed || generation != _generation || cancellation.IsCancellationRequested) return;
                        report(result);
                    }
                    if (result.Ready || result.DiagnosticCode is not ("public_unreachable" or "verification_pending" or "probe_failed")) return;
                    if (attempt < 11) await _delay(Backoff(attempt), cancellation.Token).ConfigureAwait(false);
                }
            }
            catch (OperationCanceledException) when (cancellation.IsCancellationRequested) { }
            catch (Exception error)
            {
                lock (_gate)
                {
                    if (!_disposed && generation == _generation)
                        report(new NativeTunnelStatus { Provider = "tailscale", Mode = "funnel", Phase = "Degraded", DiagnosticCode = "probe_failed", Diagnostic = "公网验证未完成：" + error.Message });
                }
            }
            finally
            {
                lock (_gate)
                {
                    if (generation == _generation) { _completedAt = DateTimeOffset.UtcNow; _current = null; }
                }
            }
        }
    }

    internal static TimeSpan Backoff(int attempt) => TimeSpan.FromSeconds(Math.Min(60, Math.Pow(2, Math.Clamp(attempt, 0, 10) + 1)));

    internal void Cancel()
    {
        lock (_gate)
        {
            ++_generation;
            _identity = "";
            _completedAt = DateTimeOffset.MinValue;
            _current?.Cancel();
            _current = null;
        }
    }

    public void Dispose()
    {
        lock (_gate) { _disposed = true; Cancel(); }
    }
}
