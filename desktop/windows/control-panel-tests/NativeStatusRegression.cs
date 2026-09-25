using System.Diagnostics;
using System.Text.Json;
using AgentDock.ControlPanel;

internal static class NativeStatusRegression
{
    private const string Origin = "https://status.example";
    private const string Ready = """{"provider":"cloudflare","mode":"named","running":true,"ready":true,"public_url":"https://status.example"}""";
    internal static async Task ChildAsync(string kind, string pidFile)
    {
        await File.WriteAllTextAsync(pidFile, Environment.ProcessId.ToString());
        if (kind == "ok") { Console.Error.Write("diagnostic, not JSON"); Console.Write(Ready); return; }
        if (kind == "fail") { Console.Write(Ready); Console.Error.Write("denied"); Environment.ExitCode = 7; return; }
        if (kind == "oversize") Console.Write(new string('x', 1024 * 1024 + 1));
        await Task.Delay(Timeout.InfiniteTimeSpan);
    }
    internal static async Task RunAsync()
    {
        var count = 0;
        void Check(bool yes, string what) { count++; if (!yes) throw new InvalidOperationException(what); }
        var actual = NativeStatusReader.ParseCloudflare(Ready, "named", Origin);
        Check(actual.Running && actual.Ready, "native owner reports running and ready");
        var pending = NativeStatusReader.ParseCloudflare(Ready.Replace("\"ready\":true", "\"ready\":false"), "named", Origin);
        Check(pending.Running && !pending.Ready, "running alone never implies readiness");
        var stopped = NativeStatusReader.ParseCloudflare("""{"provider":"cloudflare","mode":"quick","running":false,"ready":false}""", "quick", "");
        Check(!stopped.Running && !stopped.Ready, "actual stopped observation is retained");
        foreach (var text in new[] {
            "{}", "null", "[]", Ready + "{}", Ready.Replace("named", "quick"),
            Ready.Replace("cloudflare", "tailscale"), Ready.Replace("status.example", "other.example"),
            Ready.Replace("status.example", "status.example/other"), Ready.Replace("https://", "https://user@"),
            Ready.Replace("\"running\":true", "\"running\":false"),
            Ready.Replace("\"running\":true,", ""), Ready.Replace("\"ready\":true", "\"ready\":null"),
            Ready.Replace("\"ready\":true", "\"ready\":\"true\""),
            Ready.Replace("\"ready\":true", "\"ready\":0"),
            Ready.Replace("\"ready\":true", "\"ready\":false,\"ready\":true"),
            Ready.Replace("https://status.example", ""), new string(' ', 65537)
        })
        {
            var rejected = false;
            try { NativeStatusReader.ParseCloudflare(text, "named", Origin); }
            catch (Exception ex) when (ex is JsonException or InvalidDataException or InvalidOperationException) { rejected = true; }
            Check(rejected, "invalid or wrong-target native tunnel status rejected: " + text[..Math.Min(80, text.Length)]);
        }
        foreach (var running in new[] { true, false })
        {
            var service = NativeStatusReader.ParseService(JsonSerializer.Serialize(new { running, nexus_connected = false }));
            Check(service.Running == running, "native core observation preserved");
        }
        foreach (var text in new[] { "{}", "null", """{"running":null,"nexus_connected":false}""", """{"running":"true","nexus_connected":false}""", """{"running":true,"running":false,"nexus_connected":false}""" })
        {
            var rejected = false;
            try { NativeStatusReader.ParseService(text); }
            catch (Exception ex) when (ex is JsonException or InvalidDataException) { rejected = true; }
            Check(rejected, "missing/ambiguous service status is not false");
        }
        var root = Path.Combine(Path.GetTempPath(), "agentdock-native-status-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            foreach (var kind in new[] { "ok", "fail", "oversize", "cancel" })
            {
                var pidFile = Path.Combine(root, kind + ".pid");
                var start = new ProcessStartInfo(Environment.ProcessPath!) { UseShellExecute = false, CreateNoWindow = true,
                    RedirectStandardOutput = true, RedirectStandardError = true };
                if (Path.GetFileNameWithoutExtension(Environment.ProcessPath!).Equals("dotnet", StringComparison.OrdinalIgnoreCase))
                    start.ArgumentList.Add(typeof(NativeStatusRegression).Assembly.Location);
                foreach (var arg in new[] { "--native-status-child", kind, pidFile }) start.ArgumentList.Add(arg);
                using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(10));
                var task = NativeStatusProbe.RunAsync(start, timeout);
                if (kind == "cancel")
                {
                    while (!File.Exists(pidFile)) await Task.Delay(10, timeout.Token);
                    timeout.Cancel();
                }
                Exception? failure = null;
                string output = "";
                try { output = await task; } catch (Exception ex) { failure = ex; }
                if (kind == "ok") Check(failure is null && NativeStatusReader.ParseCloudflare(output, "named", Origin).Ready, "real bounded query parses stdout without stderr");
                if (kind == "fail") Check(failure is InvalidOperationException && failure.Message.Contains("denied"), "nonzero query is not successful false/true");
                if (kind == "oversize") Check(failure is InvalidDataException or OperationCanceledException, "oversized native output rejected");
                if (kind == "cancel") Check(failure is OperationCanceledException, "caller cancellation preserved");
                Check(File.Exists(pidFile), "query child actually ran");
                var alive = false;
                try { using var child = Process.GetProcessById(int.Parse(await File.ReadAllTextAsync(pidFile))); alive = !child.HasExited; }
                catch (ArgumentException) { }
                Check(!alive, "query child reaped after " + kind);
            }
        }
        finally { Directory.Delete(root, recursive: true); }
        Console.WriteLine($"Native status boundary: {count} assertions passed; only isolated query children started.");
    }
}
