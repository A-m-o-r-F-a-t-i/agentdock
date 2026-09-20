using System.Collections.Concurrent;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text;
using System.Text.Encodings.Web;
using System.Text.Json;
using AgentDock.ControlPanel;

internal static partial class Program
{
    private sealed class LocalFixture : IDisposable
    {
        public const string TaskId = "tsk_1111111111111111";
        public const string BranchId = "thr_2222222222222222";
        private const string Credential = "isolated-test-credential";
        private readonly HttpListener _listener = new();
        private readonly CancellationTokenSource _stop = new();
        private readonly string _root;
        private int _streams;
        private int _requestCount, _authenticated, _unauthorized, _controlCount, _activeStreams;
        public ConcurrentQueue<string> Cursors { get; } = new();
        public int Port { get; }
        public string Origin => $"http://127.0.0.1:{Port}/";
        public string? RedirectTarget { get; set; }
        public bool WrongThread { get; set; }
        public int RequestCount => Volatile.Read(ref _requestCount);
        public int AuthenticatedRequests => Volatile.Read(ref _authenticated);
        public int UnauthorizedRequests => Volatile.Read(ref _unauthorized);
        public int ControlCount => Volatile.Read(ref _controlCount);
        public int ActiveStreams => Volatile.Read(ref _activeStreams);

        public LocalFixture(string root)
        {
            _root = root;
            Directory.CreateDirectory(root);
            var probe = new TcpListener(IPAddress.Loopback, 0);
            probe.Start(); Port = ((IPEndPoint)probe.LocalEndpoint).Port; probe.Stop();
            _listener.Prefixes.Add(Origin); _listener.Start();
            _ = Task.Run(async () =>
            {
                try { while (_listener.IsListening) { var context = await _listener.GetContextAsync(); _ = RespondAsync(context); } }
                catch (HttpListenerException) { }
                catch (ObjectDisposedException) { }
            });
        }

        public void WriteRuntime(string root)
        {
            File.WriteAllText(Path.Combine(root, "runtime.json"), JsonSerializer.Serialize(new { port = Port, public_url = "https://never-use-for-activity.example" }));
            var encoded = ProtectedData.Protect(Encoding.UTF8.GetBytes(Credential), Encoding.UTF8.GetBytes("agentdock.startup.v1"), DataProtectionScope.CurrentUser);
            File.WriteAllText(Path.Combine(root, "auth-token.dpapi"), Convert.ToBase64String(encoded));
        }

        private async Task RespondAsync(HttpListenerContext context)
        {
            var streaming = false;
            try
            {
                Interlocked.Increment(ref _requestCount);
                if (context.Request.Headers["Authorization"] != "Bearer " + Credential)
                {
                    Interlocked.Increment(ref _unauthorized); context.Response.StatusCode = 401;
                    await JsonAsync(context, new { error = new { message = "fixture authentication failed" } }); return;
                }
                Interlocked.Increment(ref _authenticated);
                var path = context.Request.Url!.AbsolutePath;
                if (path == "/internal/runtime/activity/tasks" && RedirectTarget is not null)
                {
                    context.Response.StatusCode = 302; context.Response.RedirectLocation = RedirectTarget; return;
                }
                if (path == "/internal/runtime/activity/stream")
                {
                    streaming = true; Interlocked.Increment(ref _activeStreams);
                    var number = Interlocked.Increment(ref _streams);
                    Cursors.Enqueue(context.Request.Headers["Last-Event-ID"] ?? "");
                    context.Response.ContentType = "text/event-stream; charset=utf-8";
                    context.Response.SendChunked = true;
                    await WireAsync(context, "retry: 1000\n\n");
                    if (number == 1 && !WrongThread)
                    {
                        await EventAsync(context, Sample(1, "command.started"));
                        var output = Sample(2); output.OutputPreview = "中文输出🙂 / incremental output\n"; output.ElapsedMs = 250;
                        await EventAsync(context, output); return; // Exercise a real transport reconnect.
                    }
                    var complete = Sample(3, "command.completed"); complete.Status = "success"; complete.ExitCode = 0; complete.CommandOk = true; complete.ElapsedMs = 500;
                    if (WrongThread) complete.ThreadId = BranchId;
                    await EventAsync(context, complete);
                    while (!_stop.IsCancellationRequested)
                    {
                        await Task.Delay(150, _stop.Token);
                        await WireAsync(context, ": heartbeat\n\n");
                    }
                    return;
                }
                if (path == "/internal/runtime/activity/tasks") { await JsonAsync(context, new { tasks = new[] { TaskRecord() }, count = 1 }); return; }
                if (path == $"/internal/runtime/tasks/{TaskId}/threads") { await JsonAsync(context, new { threads = Threads(), count = 2 }); return; }
                if (path == $"/internal/runtime/tasks/{TaskId}") { await JsonAsync(context, new { task = TaskRecord() }); return; }
                if (path == "/internal/runtime/activity/control")
                {
                    Require(context.Request.HttpMethod == "POST" && context.Request.ContentType?.StartsWith("application/json", StringComparison.Ordinal) == true, "UI control did not use JSON POST.");
                    using var parsed = await JsonDocument.ParseAsync(context.Request.InputStream, cancellationToken: _stop.Token);
                    Require(parsed.RootElement.TryGetProperty("action", out _), "UI control lacks action.");
                    Interlocked.Increment(ref _controlCount);
                    await JsonAsync(context, new { ok = true }); return;
                }
                if (path == "/internal/runtime/activity/diff") { await JsonAsync(context, new { diff = "diff --git a/activity.go b/activity.go\n+// isolated rendering fixture\n", truncated = false }); return; }
                context.Response.StatusCode = 404;
                await JsonAsync(context, new { error = new { message = "unknown fixture route" } });
            }
            catch (Exception ex) when (ex is IOException or HttpListenerException or OperationCanceledException or ObjectDisposedException) { }
            finally
            {
                if (streaming) Interlocked.Decrement(ref _activeStreams);
                try { context.Response.Close(); } catch (Exception ex) when (ex is ObjectDisposedException or HttpListenerException) { }
            }
        }

        private ActivityTask TaskRecord() => new()
        {
            Id = TaskId, Title = "AgentDock 1.1.0 任务活动中心 · 隔离测试", Goal = "验证命令、线程与检查点的展示", Project = "AgentDock", Status = "active",
            ActiveThreadId = "main", WorkspaceId = "wsp_fixture", UpdatedAt = DateTimeOffset.UtcNow,
            CompletedStepCount = 1, StepCount = 2, ActiveThread = Threads()[0], Steps = Threads()[0].Steps,
            Conditions = [new ActivityCondition { Id = "cond_01", Text = "输出与退出码按真实事件展示，断线后能够续传。" }, new ActivityCondition { Id = "cond_02", Text = "原有任务与其他线程不受影响。" }],
            FinalReview = new ActivityReview { Status = "failed", Summary = "界面测试记录示例，剩余检查待完成。", VerifiedFacts = ["后端事件接口已经验证。"], MissingChecks = ["检查小窗口和缩放比例。"] }
        };

        private static List<ActivityThread> Threads() =>
        [
            new ActivityThread
            {
                Id = "main", TaskId = TaskId, Title = "主线 / Main", Status = "open", WorkspaceId = "wsp_fixture", CurrentStepId = "verify",
                Summary = "事件、线程和 SSE 已验证，正在检查本地活动窗口。", NextAction = "核对中文输出、退出状态与断线重连。",
                Steps = [new ActivityStep { Id = "events", Title = "建立活动事件层", Status = "completed" }, new ActivityStep { Id = "verify", Title = "验证任务活动窗口", Status = "in_progress" }]
            },
            new ActivityThread { Id = BranchId, TaskId = TaskId, Title = "修复分支 / Fix", Status = "blocked", ParentThreadId = "main", Summary = "等待外部测试设备。", BlockReason = "测试设备尚未连接。", WorkspaceId = "wsp_fixture" }
        ];

        private static async Task EventAsync(HttpListenerContext context, ActivityEvent value)
        {
            var json = JsonSerializer.Serialize(value, new JsonSerializerOptions(ActivityClient.JsonOptions) { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping });
            await WireAsync(context, $"id: {value.Seq}\nevent: activity\ndata: {json}\n\n");
        }

        private static async Task WireAsync(HttpListenerContext context, string text)
        {
            var bytes = Encoding.UTF8.GetBytes(text);
            var firstMultibyte = Array.FindIndex(bytes, value => value >= 0xE0);
            if (firstMultibyte >= 0 && firstMultibyte + 1 < bytes.Length)
            {
                await context.Response.OutputStream.WriteAsync(bytes.AsMemory(0, firstMultibyte + 1));
                await context.Response.OutputStream.FlushAsync();
                await context.Response.OutputStream.WriteAsync(bytes.AsMemory(firstMultibyte + 1));
            }
            else await context.Response.OutputStream.WriteAsync(bytes);
            await context.Response.OutputStream.FlushAsync();
        }

        private static async Task JsonAsync(HttpListenerContext context, object value)
        {
            var bytes = JsonSerializer.SerializeToUtf8Bytes(value, ActivityClient.JsonOptions);
            context.Response.ContentType = "application/json"; context.Response.ContentLength64 = bytes.Length;
            await context.Response.OutputStream.WriteAsync(bytes);
        }

        public void Dispose() { _stop.Cancel(); _listener.Close(); _stop.Dispose(); }
    }
}
