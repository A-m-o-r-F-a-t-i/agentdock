using System.IO;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace AgentDock.ControlPanel;

internal sealed record ActivityConnection(Uri Origin, string BearerToken);

public sealed partial class RuntimeService
{
    internal async Task<ActivityConnection> GetActivityConnectionAsync(CancellationToken cancellationToken)
    {
        var origin = new Uri(await ResolveLocalRuntimeOriginAsync(cancellationToken).ConfigureAwait(false));
        if (origin.Scheme != "http" || origin.Host != "127.0.0.1")
            throw new InvalidOperationException("Activity connections require the local loopback runtime.");
        return new ActivityConnection(origin, ReadBearerToken());
    }
}

internal sealed partial class ActivityClient : IDisposable
{
    internal static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
        PropertyNameCaseInsensitive = true,
        MaxDepth = 48
    };

    private readonly Func<CancellationToken, Task<ActivityConnection>> _connection;
    private readonly HttpClient _http;

    internal ActivityClient(RuntimeService runtime) : this(runtime.GetActivityConnectionAsync, new SocketsHttpHandler
    {
        UseProxy = false,
        AllowAutoRedirect = false,
        ConnectTimeout = TimeSpan.FromSeconds(5),
        PooledConnectionLifetime = TimeSpan.FromMinutes(5)
    }) { }

    // Keep transport ownership explicit. Isolated contract tests inject an
    // in-memory handler; production retains the loopback connection resolver.
    internal ActivityClient(Func<CancellationToken, Task<ActivityConnection>> connection, HttpMessageHandler handler)
    {
        _connection = connection;
        _http = new HttpClient(handler) { Timeout = Timeout.InfiniteTimeSpan };
    }

    [GeneratedRegex("^[A-Za-z0-9_-]{1,80}$", RegexOptions.CultureInvariant)]
    private static partial Regex Identifier();

    private static string Id(string value)
    {
        if (!Identifier().IsMatch(value)) throw new ArgumentException("Invalid activity identifier.");
        return Uri.EscapeDataString(value);
    }

    internal Task<ActivityTaskList> TasksAsync(string status, bool includeArchived, CancellationToken token) =>
        SendAsync<ActivityTaskList>(HttpMethod.Get,
            $"/internal/runtime/activity/tasks?limit=200&status={Uri.EscapeDataString(status)}&include_archived={includeArchived.ToString().ToLowerInvariant()}", null, token);

    internal Task<ActivityTaskDetail> TaskAsync(string taskId, CancellationToken token) =>
        SendAsync<ActivityTaskDetail>(HttpMethod.Get, $"/internal/runtime/tasks/{Id(taskId)}", null, token);

    internal Task<ActivityThreadList> ThreadsAsync(string taskId, CancellationToken token) =>
        SendAsync<ActivityThreadList>(HttpMethod.Get, $"/internal/runtime/tasks/{Id(taskId)}/threads", null, token);

    internal Task<ActivityLive> LiveAsync(string taskId, string threadId, CancellationToken token) =>
        SendAsync<ActivityLive>(HttpMethod.Get, "/internal/runtime/activity/live" +
            (taskId.Length == 0 ? "" : $"?task_id={Id(taskId)}" + (threadId.Length > 0 ? $"&thread_id={Id(threadId)}" : "")), null, token);

    internal Task<JsonElement> ControlAsync(object body, CancellationToken token) =>
        SendAsync<JsonElement>(HttpMethod.Post, "/internal/runtime/activity/control", body, token);

    internal Task<JsonElement> DiffAsync(string taskId, string threadId, ulong eventSequence, CancellationToken token) =>
        SendAsync<JsonElement>(HttpMethod.Get, $"/internal/runtime/activity/diff?task_id={Id(taskId)}&thread_id={Id(threadId)}&seq={eventSequence}", null, token);

    private async Task<T> SendAsync<T>(HttpMethod method, string path, object? body, CancellationToken token)
    {
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(token);
        deadline.CancelAfter(TimeSpan.FromSeconds(12));
        var connection = await _connection(deadline.Token).ConfigureAwait(false);
        using var request = Request(method, new Uri(connection.Origin, path), connection.BearerToken);
        if (body is not null) request.Content = new StringContent(JsonSerializer.Serialize(body, JsonOptions), Encoding.UTF8, "application/json");
        using var response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, deadline.Token).ConfigureAwait(false);
        var data = await ReadBoundedAsync(response.Content, 8 * 1024 * 1024, deadline.Token).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode) throw ResponseError(response, data);
        return JsonSerializer.Deserialize<T>(data, JsonOptions) ?? throw new JsonException("Empty activity response.");
    }

    private static HttpRequestMessage Request(HttpMethod method, Uri uri, string token)
    {
        var request = new HttpRequestMessage(method, uri);
        if (!string.IsNullOrWhiteSpace(token)) request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token);
        return request;
    }

    private static async Task<byte[]> ReadBoundedAsync(HttpContent content, int maximum, CancellationToken token)
    {
        if (content.Headers.ContentLength > maximum) throw new IOException("Activity response exceeded its size limit.");
        await using var input = await content.ReadAsStreamAsync(token).ConfigureAwait(false);
        using var output = new MemoryStream();
        var buffer = new byte[8192];
        while (true)
        {
            var count = await input.ReadAsync(buffer, token).ConfigureAwait(false);
            if (count == 0) break;
            if (output.Length + count > maximum) throw new IOException("Activity response exceeded its size limit.");
            output.Write(buffer, 0, count);
        }
        return output.ToArray();
    }

    private static Exception ResponseError(HttpResponseMessage response, byte[] body)
    {
        var message = $"{ActivityText.Get("Unavailable")} (HTTP {(int)response.StatusCode})";
        try
        {
            using var parsed = JsonDocument.Parse(body);
            if (parsed.RootElement.TryGetProperty("error", out var error) && error.ValueKind == JsonValueKind.Object && error.TryGetProperty("message", out var text))
                message += Environment.NewLine + text.GetString();
        }
        catch (JsonException) { }
        return new HttpRequestException(message, null, response.StatusCode);
    }

    internal async Task ObserveAsync(string taskId, string threadId, ulong initialCursor,
        Func<ActivityStreamMessage, CancellationToken, ValueTask> receive, CancellationToken token)
    {
        var path = "/internal/runtime/activity/stream";
        if (taskId.Length > 0) path += $"?task_id={Id(taskId)}&thread_id={Id(threadId)}";
        var cursor = initialCursor;
        var failures = 0;
        while (!token.IsCancellationRequested)
        {
            try
            {
                using var connectionDeadline = CancellationTokenSource.CreateLinkedTokenSource(token);
                connectionDeadline.CancelAfter(TimeSpan.FromSeconds(10));
                var connection = await _connection(connectionDeadline.Token).ConfigureAwait(false);
                using var request = Request(HttpMethod.Get, new Uri(connection.Origin, path), connection.BearerToken);
                request.Headers.TryAddWithoutValidation("Last-Event-ID", cursor.ToString(System.Globalization.CultureInfo.InvariantCulture));
                request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("text/event-stream"));
                using var response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, connectionDeadline.Token).ConfigureAwait(false);
                if (!response.IsSuccessStatusCode)
                    throw ResponseError(response, await ReadBoundedAsync(response.Content, 64 * 1024, connectionDeadline.Token).ConfigureAwait(false));
                if (response.Content.Headers.ContentType?.MediaType != "text/event-stream") throw new IOException("The local service returned an invalid event stream.");
                await using var stream = await response.Content.ReadAsStreamAsync(token).ConfigureAwait(false);
                using var reader = new StreamReader(stream, new UTF8Encoding(false, true), true, 4096, leaveOpen: true);
                var lines = new ActivitySseReader(reader);
                await receive(new ActivityStreamMessage("connected", cursor), token).ConfigureAwait(false);
                failures = 0;
                while (!token.IsCancellationRequested)
                {
                    using var silence = CancellationTokenSource.CreateLinkedTokenSource(token);
                    silence.CancelAfter(TimeSpan.FromSeconds(45));
                    var message = await lines.ReadEventAsync(silence.Token).ConfigureAwait(false);
                    if (message is null) throw new IOException("The local activity stream ended.");
                    if (message.Type is "activity" or "cursor" && message.Sequence <= cursor) continue;
                    if (message.Type == "activity" && taskId.Length > 0 &&
                        (message.Event?.TaskId != taskId || message.Event.ThreadId != threadId)) throw new IOException("Activity stream task/thread binding mismatch.");
                    await receive(message, token).ConfigureAwait(false);
                    if (message.Type is "activity" or "cursor" or "gap" or "reset") cursor = message.Sequence;
                }
            }
            catch (OperationCanceledException) when (token.IsCancellationRequested) { return; }
            catch (Exception ex) when (ex is IOException or HttpRequestException or JsonException or OperationCanceledException or DecoderFallbackException)
            {
                if (token.IsCancellationRequested) return;
                await receive(new ActivityStreamMessage("disconnected", cursor, Message: ActivityText.Get("Reconnecting") + Environment.NewLine + ex.Message), token).ConfigureAwait(false);
                failures = Math.Min(failures + 1, 5);
                await Task.Delay(TimeSpan.FromSeconds(Math.Min(10, failures * 2)), token).ConfigureAwait(false);
            }
        }
    }

    public void Dispose() => _http.Dispose();
}

// A bounded SSE parser: partial lines, comments, fragmented UTF-8 and multiline data
// are handled without allocating an unbounded line from the transport.
internal sealed class ActivitySseReader(TextReader reader)
{
    internal const int MaximumEventCharacters = 64 * 1024;
    private readonly char[] _buffer = new char[4096];
    private int _offset;
    private int _count;

    internal async Task<ActivityStreamMessage?> ReadEventAsync(CancellationToken token)
    {
        string kind = "", id = "";
        var data = new StringBuilder();
        var eventCharacters = 0;
        while (true)
        {
            var line = await ReadLineAsync(token).ConfigureAwait(false);
            if (line is null) return null;
            eventCharacters += line.Length;
            if (eventCharacters > MaximumEventCharacters) throw new IOException("Activity SSE event exceeded its size limit.");
            if (line.Length == 0)
            {
                if (data.Length > 0)
                {
                    if (!ulong.TryParse(id, out var sequence)) throw new IOException("Activity SSE sequence is invalid.");
                    var text = data.ToString().TrimEnd('\n');
                    if (kind == "activity")
                    {
                        var value = JsonSerializer.Deserialize<ActivityEvent>(text, ActivityClient.JsonOptions) ?? throw new JsonException("Empty activity event.");
                        if (value.SchemaVersion != 1 || value.Seq != sequence) throw new IOException("Unsupported or inconsistent activity event.");
                        return new ActivityStreamMessage(kind, sequence, value);
                    }
                    if (kind is "cursor" or "gap" or "reset" or "warning")
                        return new ActivityStreamMessage(kind, sequence, Message: text);
                }
                // Return a heartbeat so the caller can reset its silence deadline.
                return new ActivityStreamMessage("heartbeat", 0);
            }
            if (line[0] == ':') continue;
            var colon = line.IndexOf(':');
            var field = colon < 0 ? line : line[..colon];
            var valueText = colon < 0 ? "" : line[(colon + 1)..];
            if (valueText.StartsWith(' ')) valueText = valueText[1..];
            switch (field)
            {
                case "event": kind = valueText; break;
                case "id": if (valueText.Contains('\0')) throw new IOException("Invalid SSE event ID."); id = valueText; break;
                case "data": data.Append(valueText).Append('\n'); break;
            }
        }
    }

    private async Task<string?> ReadLineAsync(CancellationToken token)
    {
        var line = new StringBuilder();
        while (true)
        {
            if (_offset == _count)
            {
                _count = await reader.ReadAsync(_buffer.AsMemory(), token).ConfigureAwait(false);
                _offset = 0;
                if (_count == 0) return line.Length == 0 ? null : line.ToString();
            }
            var end = Array.IndexOf(_buffer, '\n', _offset, _count - _offset);
            var length = (end < 0 ? _count : end) - _offset;
            if (line.Length + length > MaximumEventCharacters) throw new IOException("Activity SSE line exceeded its size limit.");
            line.Append(_buffer, _offset, length);
            _offset += length;
            if (end >= 0)
            {
                _offset++;
                if (line.Length > 0 && line[^1] == '\r') line.Length--;
                return line.ToString();
            }
        }
    }
}
