using System.Diagnostics;
using System.IO;
using System.Net.Http;
using System.Text;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed partial class RuntimeService
{
    private readonly object _capabilityLogGate = new();

    public Task<CapabilityInventory> GetCapabilityInventoryAsync(
        IProgress<CapabilityInventoryUpdate>? progress,
        CancellationToken cancellationToken = default) => Task.Run(async () =>
    {
        // Resolve the runtime and DPAPI token once on a worker, not three times
        // on WPF's dispatcher. Inventory endpoints read local state only; they
        // never require a live external MCP handshake to display plugin cards.
        var total = Stopwatch.StartNew();
        var origin = await ResolveLocalRuntimeOriginAsync(cancellationToken).ConfigureAwait(false);
        var token = ReadBearerToken();
        var connectionMilliseconds = total.ElapsedMilliseconds;
        var plugins = ReadCapabilitySectionAsync<RuntimePluginsResponse>("plugins", "/internal/runtime/plugins",
            origin, token, response => new CapabilityInventory { Plugins = response.Plugins ?? [] }, progress, cancellationToken);
        var skills = ReadCapabilitySectionAsync<RuntimeSkillsResponse>("skills", "/internal/runtime/skills?summary=true",
            origin, token, response => new CapabilityInventory { Skills = response.Skills ?? [] }, progress, cancellationToken);
        var mcp = ReadCapabilitySectionAsync<RuntimeMcpResponse>("mcp", "/internal/runtime/mcp",
            origin, token, response => new CapabilityInventory { McpServers = response.Servers ?? [] }, progress, cancellationToken);
        var sections = await Task.WhenAll(plugins, skills, mcp).ConfigureAwait(false);
        var result = new CapabilityInventory
        {
            Plugins = sections[0].Plugins,
            Skills = sections[1].Skills,
            McpServers = sections[2].McpServers
        };
        foreach (var section in sections)
        {
            foreach (var error in section.Errors) result.Errors[error.Key] = error.Value;
            foreach (var elapsed in section.TimingMilliseconds) result.TimingMilliseconds[elapsed.Key] = elapsed.Value;
        }
        result.TimingMilliseconds["connection"] = connectionMilliseconds;
        result.TimingMilliseconds["total"] = total.ElapsedMilliseconds;
        RecordCapabilityTiming("fetch", result);
        return result;
    }, cancellationToken);

    private async Task<CapabilityInventory> ReadCapabilitySectionAsync<T>(
        string section, string path, string origin, string token,
        Func<T, CapabilityInventory> project,
        IProgress<CapabilityInventoryUpdate>? progress,
        CancellationToken cancellationToken)
    {
        var timer = Stopwatch.StartNew();
        CapabilityInventory result;
        var error = "";
        try
        {
            var response = await SendRuntimeApiAsync<T>(HttpMethod.Get, path, null,
                cancellationToken, origin, token).ConfigureAwait(false);
            result = project(response);
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested) { throw; }
        catch (Exception exception) when (exception is HttpRequestException or InvalidOperationException or IOException or TaskCanceledException)
        {
            // One unavailable section does not erase successfully loaded local
            // inventory, and is not misrepresented as an empty healthy list.
            error = exception.Message;
            result = new CapabilityInventory();
            result.Errors[section] = error;
        }
        result.TimingMilliseconds[section] = timer.ElapsedMilliseconds;
        progress?.Report(new CapabilityInventoryUpdate(section, result, timer.ElapsedMilliseconds, error));
        return result;
    }

    internal void RecordCapabilityRenderTime(long elapsedMilliseconds) =>
        RecordCapabilityTiming("render", new CapabilityInventory
        {
            TimingMilliseconds = new Dictionary<string, long> { ["render"] = elapsedMilliseconds }
        });

    private void RecordCapabilityTiming(string phase, CapabilityInventory inventory)
    {
        // Keep small bounded diagnostic records. Never include tokens, request
        // bodies, plugin paths, or the potentially sensitive server error text.
        var line = JsonSerializer.Serialize(new
        {
            timestamp_utc = DateTimeOffset.UtcNow,
            phase,
            timing_ms = inventory.TimingMilliseconds,
            failed_sections = inventory.Errors.Keys.ToArray()
        });
        try
        {
            lock (_capabilityLogGate)
            {
                Directory.CreateDirectory(LogsDirectory);
                var path = Path.Combine(LogsDirectory, "capabilities.log");
                if (File.Exists(path) && new FileInfo(path).Length > 128 * 1024)
                    File.Move(path, path + ".1", overwrite: true);
                File.AppendAllText(path, line + Environment.NewLine, new UTF8Encoding(false));
            }
        }
        catch (Exception exception) when (exception is IOException or UnauthorizedAccessException)
        {
            Trace.WriteLine($"Capability timing log unavailable: {exception.GetType().Name}");
        }
    }
}
