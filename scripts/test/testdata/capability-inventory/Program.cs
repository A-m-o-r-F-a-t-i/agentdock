using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Reflection;
using System.Text;
using System.Text.Json;
using System.Windows;
using System.Windows.Controls;
using AgentDock.ControlPanel;

internal static class Program
{
    [STAThread]
    private static int Main(string[] args)
    {
        if (args.Length != 1 || Directory.Exists(args[0])) throw new ArgumentException("Provide a fresh isolated test directory.");
        Directory.CreateDirectory(args[0]);
        try
        {
            RunNetworkTests(args[0]).GetAwaiter().GetResult();
            RunRenderingTests(args[0]);
            File.WriteAllText(Path.Combine(args[0], "ui-result.json"), "{\"success\":true,\"collapsed_members_lazy\":true,\"expanded_details_render\":true,\"failed_refresh_retains_inventory\":true}");
            Console.WriteLine("Capability inventory regression passed.");
            return 0;
        }
        catch (Exception error) { Console.Error.WriteLine(error); return 1; }
    }

    private static async Task RunNetworkTests(string root)
    {
        using var host = new LocalFixture();
        await File.WriteAllTextAsync(Path.Combine(root, "runtime.json"), JsonSerializer.Serialize(new { port = host.Port }));
        using var runtime = new RuntimeService(root);
        var arrived = new TaskCompletionSource<CapabilityInventoryUpdate>(TaskCreationOptions.RunContinuationsAsynchronously);
        var progress = new DirectProgress<CapabilityInventoryUpdate>(update => { if (update.Section == "plugins") arrived.TrySetResult(update); });
        var started = Stopwatch.StartNew();
        var fetch = runtime.GetCapabilityInventoryAsync(progress);
        var first = await arrived.Task.WaitAsync(TimeSpan.FromSeconds(5));
        Require(first.Inventory.Plugins.Count == 1 && !fetch.IsCompleted, "Plugin cards waited for the blocked MCP response.");
        var firstMs = started.ElapsedMilliseconds;
        host.ReleaseMcp.TrySetResult();
        var inventory = await fetch.WaitAsync(TimeSpan.FromSeconds(5));
        Require(inventory.Plugins.Count == 1 && inventory.Skills.Count == 1 && inventory.McpServers.Count == 1 && inventory.Errors.Count == 0, "Incomplete successful inventory.");
        Require(host.SawSummary, "Control panel did not use the lightweight Skill endpoint.");

        host.FailMcp = true;
        var partial = await runtime.GetCapabilityInventoryAsync();
        Require(partial.Errors.ContainsKey("mcp") && partial.Plugins.Count == 1 && partial.Skills.Count == 1, "A failed MCP section discarded local inventory.");
        host.FailMcp = false;
        host.MalformedSkills = true;
        partial = await runtime.GetCapabilityInventoryAsync();
        Require(partial.Errors.ContainsKey("skills") && partial.Plugins.Count == 1 && partial.McpServers.Count == 1, "Malformed Skill JSON discarded other sections.");
        host.MalformedSkills = false;
        using var canceled = new CancellationTokenSource();
        canceled.Cancel();
        try { await runtime.GetCapabilityInventoryAsync(canceled.Token); throw new InvalidOperationException("Cancellation was swallowed."); }
        catch (OperationCanceledException) { }
        await File.WriteAllTextAsync(Path.Combine(root, "result.json"), JsonSerializer.Serialize(new
        {
            success = true, first_plugin_ms = firstMs, summary_endpoint_used = host.SawSummary,
            blocked_mcp_did_not_block_cards = true, per_section_failure_retained_other_data = true,
            malformed_json_isolated = true, cancellation_propagated = true
        }, new JsonSerializerOptions { WriteIndented = true }));
    }

    private static void RunRenderingTests(string root)
    {
        // Do not run App.OnStartup: it owns the production tray singleton. A
        // hidden WPF window and resource dictionary suffice for renderer tests.
        var app = new App();
        app.InitializeComponent(); // Resources only. Never call App.Run/OnStartup.
        using var runtime = new RuntimeService(root);
        var window = new MainWindow(runtime);
        var buildCard = typeof(MainWindow).GetMethod("BuildPluginCard", BindingFlags.Instance | BindingFlags.NonPublic)!;
        var card = (Border)buildCard.Invoke(window, new object[] { new PluginCapabilityInfo { Name = "fixture", Enabled = true, Heavy = true, Skills = [] } })!;
        var expander = ((StackPanel)card.Child).Children.OfType<Expander>().Single();
        Require(expander.Content is null, "Collapsed Heavy plugin created member controls eagerly.");
        expander.IsExpanded = true;
        Require(expander.Content is StackPanel, "Expanding a Heavy plugin did not create its details.");

        var merge = typeof(MainWindow).GetMethod("MergeCapabilitySection", BindingFlags.Instance | BindingFlags.NonPublic)!;
        merge.Invoke(window, new object[] { "plugins", new CapabilityInventory { Plugins = [new PluginCapabilityInfo { Name = "last-known" }] } });
        merge.Invoke(window, new object[] { "plugins", new CapabilityInventory { Errors = new() { ["plugins"] = "offline" } } });
        var retained = (CapabilityInventory)typeof(MainWindow).GetField("_capabilityInventory", BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(window)!;
        Require(retained.Plugins.Single().Name == "last-known" && retained.Errors.ContainsKey("plugins"), "Failed refresh erased the last displayed inventory.");
        typeof(MainWindow).GetMethod("CloseForReplacement", BindingFlags.Instance | BindingFlags.NonPublic)!.Invoke(window, null);
    }

    private static void Require(bool condition, string error) { if (!condition) throw new InvalidOperationException(error); }
    private sealed class DirectProgress<T>(Action<T> handler) : IProgress<T> { public void Report(T value) => handler(value); }

    private sealed class LocalFixture : IDisposable
    {
        private readonly HttpListener listener = new();
        public int Port { get; }
        public bool FailMcp, MalformedSkills, SawSummary;
        public TaskCompletionSource ReleaseMcp { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);

        public LocalFixture()
        {
            var probe = new TcpListener(IPAddress.Loopback, 0);
            probe.Start(); Port = ((IPEndPoint)probe.LocalEndpoint).Port; probe.Stop();
            listener.Prefixes.Add($"http://127.0.0.1:{Port}/"); listener.Start();
            _ = Task.Run(async () => { try { while (listener.IsListening) { var context = await listener.GetContextAsync(); _ = Respond(context); } } catch (HttpListenerException) { } catch (ObjectDisposedException) { } });
        }

        private async Task Respond(HttpListenerContext context)
        {
            try
            {
                var path = context.Request.Url!.AbsolutePath;
                string body;
                if (path.EndsWith("/plugins")) body = "{\"plugins\":[{\"name\":\"fixture\",\"heavy\":true,\"enabled\":true}]}";
                else if (path.EndsWith("/skills"))
                {
                    SawSummary = context.Request.QueryString["summary"] == "true";
                    body = MalformedSkills ? "{" : "{\"skills\":[{\"skill\":\"fixture\",\"enabled\":true}]}";
                }
                else
                {
                    await ReleaseMcp.Task;
                    if (FailMcp) context.Response.StatusCode = 503;
                    body = FailMcp ? "{\"error\":\"fixture unavailable\"}" : "{\"servers\":[{\"name\":\"fixture\",\"status\":\"disconnected\"}]}";
                }
                var bytes = Encoding.UTF8.GetBytes(body);
                context.Response.ContentType = "application/json";
                context.Response.ContentLength64 = bytes.Length;
                await context.Response.OutputStream.WriteAsync(bytes);
            }
            finally { context.Response.Close(); }
        }

        public void Dispose() { ReleaseMcp.TrySetResult(); listener.Close(); }
    }
}
