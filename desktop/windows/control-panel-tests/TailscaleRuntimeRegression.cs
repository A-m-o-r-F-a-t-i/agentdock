using System.Reflection;
using System.Text.Json;

// Exercise the actual RuntimeService wiring without constructing App/Window,
// launching native queries, or accessing an installed runtime directory.
internal static class TailscaleRuntimeRegression
{
    internal static async Task RunAsync(string assemblyPath)
    {
        var assembly = Assembly.LoadFrom(Path.GetFullPath(assemblyPath));
        var type = assembly.GetType("AgentDock.ControlPanel.RuntimeService", true)!;
        var native = assembly.GetType("AgentDock.ControlPanel.NativeTunnelStatus", true)!;
        const BindingFlags methods = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
        var count = 0;
        void Check(bool yes, string message) { ++count; if (!yes) throw new InvalidOperationException(message); }
        object? Call(object instance, string name, params object?[] args) => instance.GetType().GetMethod(name, methods)!.Invoke(instance, args);
        async Task<object> Await(object task)
        { await (Task)task; return task.GetType().GetProperty("Result")!.GetValue(task)!; }
        bool Ready(object value) => (bool)value.GetType().GetProperty("Ready")!.GetValue(value)!;
        string Text(object value, string name) => (string)value.GetType().GetProperty(name)!.GetValue(value)!;
        var root = Path.Combine(Path.GetTempPath(), "agentdock-tailscale-binding-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            var origin = "https://old.example.ts.net"; var port = 8765; var configured = "2026-09-25T00:00:00Z";
            void Files(string provider = "tailscale")
            {
                File.WriteAllText(Path.Combine(root, "runtime.json"), JsonSerializer.Serialize(new {
                    install_root = root, agentdock_binary = Path.Combine(root, "inert.exe"), port,
                    public_access_provider = provider, public_access_mode = "funnel", tunnel_mode = "funnel", public_access_url = origin }));
                File.WriteAllText(Path.Combine(root, "tailscale-funnel-state.json"), JsonSerializer.Serialize(new {
                    schema_version = 1, managed = true, enabled = true, configured_at = configured,
                    device_id = "fixture", dns_name = "old.example.ts.net", public_origin = origin, local_origin = $"http://127.0.0.1:{port}" }));
            }
            Files();
            using var runtime = (IDisposable)Activator.CreateInstance(type, [root])!;
            // In-flight refresh fixture prevents any subprocess launch.
            var held = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            type.GetField("_tailscaleRefresh", methods)!.SetValue(runtime, held.Task);
            var cache = type.GetField("_tailscaleObservations", methods)!.GetValue(runtime)!;
            async Task Seed()
            {
                var identity = await Await(Call(runtime, "ReadTailscaleIdentityAsync", CancellationToken.None)!);
                var generation = Call(cache, "Bind", identity)!;
                var order = Call(cache, "NextOrder")!;
                var value = JsonSerializer.Deserialize(JsonSerializer.Serialize(new {
                    provider = "tailscale", mode = "funnel", running = true, ready = true, local_ready = true,
                    verified_at = DateTimeOffset.UtcNow, public_url = origin, local_origin = $"http://127.0.0.1:{port}" }), native)!;
                Check((bool)Call(cache, "Publish", identity, generation, order, value, DateTimeOffset.UtcNow)!, "seed actual observation cache");
            }
            Task<object> Read() => Await(Call(runtime, "CachedTailscaleStatusAsync", origin, $"http://127.0.0.1:{port}", CancellationToken.None)!);
            await Seed(); Check(Ready(await Read()), "same-origin runtime cache is used");
            origin = "https://new.example.ts.net"; Files();
            var changed = await Read();
            Check(!Ready(changed) && Text(changed, "PublicUrl") == origin, "new origin cannot inherit old Ready");
            await Seed(); Check(Ready(await Read()), "new origin recovers after fresh observation");
            port = 8877; Files(); changed = await Read();
            Check(!Ready(changed) && Text(changed, "LocalOrigin").EndsWith(":8877"), "local target change invalidates runtime cache");
            await Seed(); configured = "2026-09-25T00:01:00Z"; Files();
            Check(!Ready(await Read()), "same-address new configuration invalidates old generation");
            await Seed(); Files("cloudflare");
            Check(!Ready(await Read()), "different active provider cannot inherit Tailscale Ready");
            Check(!held.Task.IsCompleted, "no native query, network, task or runtime started");
        }
        finally { Directory.Delete(root, true); }
        Console.WriteLine($"Actual RuntimeService Tailscale binding: {count} assertions passed; isolated files only.");
    }
}
