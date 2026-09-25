using System.Diagnostics;
using System.IO;
using System.Text;

namespace AgentDock.ControlPanel;

// Shared bounded read-only command runner; owns only the query process, never Core.
internal static class NativeStatusProbe
{
    internal static async Task<string> RunAsync(ProcessStartInfo startInfo, CancellationTokenSource timeout)
    {
        using var process = Process.Start(startInfo) ?? throw new InvalidOperationException("Native status process did not start.");
        var stdout = ReadBoundedProbeTextAsync(process.StandardOutput, 1024 * 1024, timeout);
        var stderr = ReadBoundedProbeTextAsync(process.StandardError, 16 * 1024, timeout);
        try
        {
            await Task.WhenAll(process.WaitForExitAsync(timeout.Token), stdout, stderr).ConfigureAwait(false);
            if (process.ExitCode != 0)
            {
                var errorText = await stderr.ConfigureAwait(false);
                throw new InvalidOperationException(string.IsNullOrWhiteSpace(errorText) ? $"Native status process failed with exit code {process.ExitCode}." : errorText.Trim());
            }
            return await stdout.ConfigureAwait(false);
        }
        finally
        {
            if (!process.HasExited)
            {
                try { process.Kill(entireProcessTree: true); }
                catch (InvalidOperationException) when (process.HasExited) { }
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
                throw new InvalidDataException("Native status output exceeded the size limit.");
            }
            result.Append(buffer, 0, count);
        }
        return result.ToString();
    }
}
