using System.Net;
using System.Net.Http;
using System.Reflection;
using System.Text;
using System.Text.Json;

// Tests load only two BCL-only production types, not App/Window/RuntimeService.
// All HTTP responses are synthetic; no product, installer or task is started.
internal static class CoreHealthRegression
{
    private static int _assertions;
    private static void Check(bool value, string message)
    {
        _assertions++;
        if (!value) throw new InvalidOperationException(message);
    }
    private const string Good = "{\"ok\":true,\"service\":\"agentdock\",\"process_id\":123,\"version\":\"1.1.6\"}";
    private static IDisposable Construct(Type type, params object?[] args) =>
        (IDisposable)(Activator.CreateInstance(type, BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic, null, args, null)
            ?? throw new InvalidOperationException("Could not create " + type.Name));
    private static Task<(bool Healthy, string Version)> Read(Type type, IDisposable reader, string origin, CancellationToken token = default) =>
        (Task<(bool Healthy, string Version)>)type.GetMethod("ReadAsync")!.Invoke(reader, [origin, token])!;
    private static Task<string> Cached(Type type, IDisposable cache, string binary, string pointer, CancellationToken token = default) =>
        (Task<string>)type.GetMethod("ReadAsync")!.Invoke(cache, [binary, pointer, token])!;
    private static async Task Cancelled(Task operation, string message)
    {
        try { await operation; }
        catch (OperationCanceledException) { Check(true, message); return; }
        throw new InvalidOperationException(message);
    }
    private static HttpResponseMessage Response(string body, HttpStatusCode status = HttpStatusCode.OK, string media = "application/json") =>
        new(status) { Content = new StringContent(body, Encoding.UTF8, media) };

    public static async Task RunAsync(string path)
    {
        var assembly = Assembly.LoadFrom(Path.GetFullPath(path));
        var health = assembly.GetType("AgentDock.ControlPanel.CoreHealthReader", true)!;
        var cacheType = assembly.GetType("AgentDock.ControlPanel.CoreVersionCache", true)!;
        using (var handler = (HttpClientHandler)health.GetMethod("CreateHandler")!.Invoke(null, null)!)
            Check(!handler.AllowAutoRedirect && !handler.UseProxy && !handler.UseCookies && handler.MaxResponseHeadersLength == 8, "loopback transport boundary");
        foreach (var test in new[] {
            (Good, HttpStatusCode.OK, "application/json", true),
            ("<html>unrelated server</html>", HttpStatusCode.OK, "text/html", false),
            ("{bad", HttpStatusCode.OK, "application/json", false),
            (Good.Replace("true", "false"), HttpStatusCode.OK, "application/json", false),
            (Good.Replace("agentdock", "another-service"), HttpStatusCode.OK, "application/json", false),
            (Good.Replace("123", "\"123\""), HttpStatusCode.OK, "application/json", false),
            (Good.Replace("123", "0"), HttpStatusCode.OK, "application/json", false),
            (Good.Replace("1.1.6", "not-a-version"), HttpStatusCode.OK, "application/json", false),
            (Good.Replace("{", "{\"ok\":false,"), HttpStatusCode.OK, "application/json", false),
            (Good + "{}", HttpStatusCode.OK, "application/json", false),
            ("[1,2]", HttpStatusCode.OK, "application/json", false),
            (new string(' ', 16385) + Good, HttpStatusCode.OK, "application/json", false),
            (Good, HttpStatusCode.Redirect, "application/json", false),
            (Good, HttpStatusCode.NoContent, "application/json", false),
            ("{\"ok\":true,\"version\":\"1.1.6\"}", HttpStatusCode.OK, "application/json", false)
        })
        {
            var handler = new SyntheticHandler((request, _) =>
            {
                Check(request.RequestUri?.AbsoluteUri == "http://127.0.0.1:8765/healthz" && request.Headers.Authorization is null, "health target/no credentials");
                return Task.FromResult(Response(test.Item1, test.Item2, test.Item3));
            });
            using var reader = Construct(health, handler);
            var result = await Read(health, reader, "http://127.0.0.1:8765");
            Check(result.Healthy == test.Item4 && result.Version == (test.Item4 ? "1.1.6" : ""), "strict health response: " + test.Item1[..Math.Min(test.Item1.Length, 90)]);
            Check(handler.Calls == 1, "one HTTP request, no retry");
        }
        {
            var handler = new SyntheticHandler((_, _) => throw new InvalidOperationException("non-loopback origin was contacted"));
            using var reader = Construct(health, handler);
            foreach (var origin in new[] { "http://example.com:8765", "http://192.0.2.1", "https://127.0.0.1", "http://user@127.0.0.1", "http://127.0.0.1/other", "http://127.0.0.1/?a=1", "http://127.0.0.1/#x" })
                Check(!(await Read(health, reader, origin)).Healthy, "reject unsafe health origin");
            Check(handler.Calls == 0, "invalid origins never enter HTTP handler");
        }
        {
            var handler = new SyntheticHandler((_, _) => Task.FromResult(Response(Good)));
            using var reader = Construct(health, handler);
            Check((await Read(health, reader, "http://[::1]:8765")).Healthy, "literal IPv6 loopback");
        }
        {
            var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            var handler = new SyntheticHandler(async (_, token) => { entered.TrySetResult(); await Task.Delay(Timeout.InfiniteTimeSpan, token); return Response(Good); });
            using var reader = Construct(health, handler);
            using var cancel = new CancellationTokenSource();
            var waiting = Read(health, reader, "http://127.0.0.1:8765", cancel.Token);
            await entered.Task; cancel.Cancel();
            await Cancelled(waiting, "caller cancellation must propagate");
        }
        {
            var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            var handler = new SyntheticHandler(async (_, token) => { entered.TrySetResult(); await Task.Delay(Timeout.InfiniteTimeSpan, token); return Response(Good); });
            using var reader = Construct(health, handler);
            var waiting = Read(health, reader, "http://127.0.0.1:8765");
            await entered.Task; reader.Dispose();
            await Cancelled(waiting, "disposing health reader cancels pending HTTP");
        }
        {
            var handler = new SyntheticHandler(async (_, token) => { await Task.Delay(Timeout.InfiniteTimeSpan, token); return Response(Good); });
            using var reader = Construct(health, handler);
            Check(!(await Read(health, reader, "http://127.0.0.1:8765")).Healthy && handler.Calls == 1, "bounded internal timeout");
        }
        // Missing Content-Length must not remove the body budget.
        {
            var handler = new SyntheticHandler((_, _) =>
            {
                var response = Response(Good);
                response.Content = new StreamContent(new MemoryStream(Encoding.UTF8.GetBytes(new string(' ', 16385) + Good)));
                response.Content.Headers.ContentType = new("application/json");
                response.Content.Headers.ContentLength = null;
                return Task.FromResult(response);
            });
            using var reader = Construct(health, handler);
            Check(!(await Read(health, reader, "http://127.0.0.1:8765")).Healthy, "streamed body budget");
        }
        var directory = Path.Combine(Path.GetTempPath(), "agentdock-health-unit-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        try
        {
            var binary = Path.Combine(directory, "dummy.exe"); var pointer = Path.Combine(directory, "active-version.json");
            File.WriteAllText(binary, "inert file, never executed");
            File.WriteAllText(pointer, "first");
            var clock = new TestClock(); var calls = 0;
            Func<string, string, CancellationToken, Task<string>> probe = async (_, _, token) => { Interlocked.Increment(ref calls); await Task.Delay(10, token); return "1.1.6"; };
            using (var cache = Construct(cacheType, probe, clock))
            {
                var values = await Task.WhenAll(Enumerable.Range(0, 12).Select(_ => Cached(cacheType, cache, binary, pointer)));
                Check(calls == 1 && values.All(value => value == "1.1.6"), "single-flight version read");
                Check(await Cached(cacheType, cache, binary, pointer) == "1.1.6" && calls == 1, "successful version cache");
                File.AppendAllText(binary, "changed");
                await Cached(cacheType, cache, binary, pointer); Check(calls == 2, "binary identity invalidates cache");
                File.AppendAllText(pointer, "changed");
                await Cached(cacheType, cache, binary, pointer); Check(calls == 3, "generation pointer invalidates cache");
                clock.Advance(TimeSpan.FromMinutes(6));
                await Cached(cacheType, cache, binary, pointer); Check(calls == 4, "bounded successful cache lifetime");
            }
            calls = 0;
            Func<string, string, CancellationToken, Task<string>> missing = (_, _, _) => { calls++; return Task.FromResult(""); };
            using (var cache = Construct(cacheType, missing, clock))
            {
                for (var i = 0; i < 20; i++) Check(await Cached(cacheType, cache, binary, pointer) == "", "negative cache value");
                Check(calls == 1, "failed version probe not repeated each poll");
                clock.Advance(TimeSpan.FromSeconds(31)); await Cached(cacheType, cache, binary, pointer);
                Check(calls == 2, "bounded negative cache lifetime");
            }
            {
                var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
                Func<string, string, CancellationToken, Task<string>> delayed = async (_, _, token) => { entered.TrySetResult(); await Task.Delay(Timeout.InfiniteTimeSpan, token); return ""; };
                using var cache = Construct(cacheType, delayed, clock);
                var waiting = Cached(cacheType, cache, binary, pointer); await entered.Task;
                cache.Dispose(); await Cancelled(waiting, "cache disposal cancels outstanding work");
            }
            var generation = Path.Combine(directory, "versions", "v1.1.6"); Directory.CreateDirectory(generation);
            File.WriteAllText(Path.Combine(generation, "agentdock-core.exe"), "This is not an executable.");
            File.WriteAllText(pointer, JsonSerializer.Serialize(new { schema_version = 1, active_version = "v1.1.6", state = "committed" }));
            using (var cache = Construct(cacheType, null, clock))
                Check(await Cached(cacheType, cache, binary, pointer) == "1.1.6", "passive pointer read never executes Core or stable shim");
            var generationCore = Path.Combine(generation, "agentdock-core.exe");
            using (var cache = Construct(cacheType, null, clock))
            {
                Check(await Cached(cacheType, cache, binary, pointer) == "1.1.6", "generation initially present");
                File.Delete(generationCore);
                Check(await Cached(cacheType, cache, binary, pointer) == "", "deleted generation immediately invalidates positive cache");
                File.WriteAllText(generationCore, "New inert generation");
                Check(await Cached(cacheType, cache, binary, pointer) == "1.1.6", "created generation immediately invalidates negative cache");
            }
            calls = 0;
            Func<string, string, CancellationToken, Task<string>> counted = (_, _, _) => { calls++; return Task.FromResult("1.1.6"); };
            using (var cache = Construct(cacheType, counted, clock))
            {
                await Cached(cacheType, cache, binary, pointer);
                await Cached(cacheType, cache, binary, pointer);
                Check(calls == 1, "unchanged generation retains cache");
                File.AppendAllText(generationCore, "replacement");
                await Cached(cacheType, cache, binary, pointer);
                Check(calls == 2, "generation replacement invalidates cache");
            }
            Func<string, string, CancellationToken, Task<string>> racing = (_, _, _) =>
            { File.Delete(generationCore); return Task.FromResult("1.1.6"); };
            using (var cache = Construct(cacheType, racing, clock))
                Check(await Cached(cacheType, cache, binary, pointer) == "", "changed dependency during read is not published");
            File.WriteAllText(generationCore, "Restored inert generation");
            File.WriteAllText(pointer, "{\"schema_version\":\"1\",\"active_version\":\"v1.1.6\"}");
            using (var cache = Construct(cacheType, null, clock))
                Check(await Cached(cacheType, cache, binary, pointer) == "", "malformed pointer is unknown, not healthy or a process fallback");
        }
        finally { Directory.Delete(directory, true); }
        Console.WriteLine($"Loopback health/cache: {_assertions} assertions passed; no network, runtime, installer, task or visible UI started.");
    }
    private sealed class SyntheticHandler(Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> response) : HttpMessageHandler
    {
        public int Calls;
        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        { Interlocked.Increment(ref Calls); return response(request, cancellationToken); }
    }
    private sealed class TestClock : TimeProvider
    {
        private DateTimeOffset _now = DateTimeOffset.Parse("2026-09-24T00:00:00Z");
        public override DateTimeOffset GetUtcNow() => _now;
        public void Advance(TimeSpan duration) => _now += duration;
    }
}
