using System.Text.Json;
using System.Net.Http;

namespace AgentDock.ControlPanel;

public sealed partial class RuntimeService
{
    public async Task SetPluginHeavyAsync(string name, bool heavy, CancellationToken cancellationToken = default) =>
        _ = await SendRuntimeApiAsync<JsonElement>(HttpMethod.Post, "/internal/runtime/plugins",
            new { action = heavy ? "heavy_enable" : "heavy_disable", name }, cancellationToken);

    public async Task<RuntimeOptionsView> GetRuntimeOptionsAsync(CancellationToken cancellationToken = default)
    {
        var binary = await ResolveCoreBinaryAsync(cancellationToken);
        var startInfo = CreateRedirectedProcessStartInfo(binary);
        foreach (var argument in new[] { "config", "runtime-get", "--runtime-root", RuntimeRoot })
        {
            startInfo.ArgumentList.Add(argument);
        }
        var output = await RunProcessAsync(startInfo, cancellationToken);
        return JsonSerializer.Deserialize<RuntimeOptionsView>(output, JsonOptions)
            ?? throw new InvalidOperationException(UiText.Get("RuntimeApiEmptyResponse"));
    }

    public Task SaveRuntimeOptionsAsync(RuntimeOptions options, CancellationToken cancellationToken = default) =>
        RunNativeAgentDockAsync("config",
            ["runtime-update", "--options-json", JsonSerializer.Serialize(options)], cancellationToken);
}
