using System.IO;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed record ExecutionStreamMessage(string Kind, ulong Seq, JsonElement Value, string Message = "");

internal sealed partial class ActivityClient
{
    internal Task<JsonElement> ExecutionGetAsync(string path, CancellationToken token) => SendAsync<JsonElement>(HttpMethod.Get, path, null, token);
    internal Task<JsonElement> ExecutionPostAsync(string path, object value, CancellationToken token) => SendAsync<JsonElement>(HttpMethod.Post, path, value, token);
    internal async Task ObserveExecutionsAsync(string query, ulong after, Func<ExecutionStreamMessage, Task> received, CancellationToken token)
    {
        while (!token.IsCancellationRequested)
        {
            try
            {
                using var connect = CancellationTokenSource.CreateLinkedTokenSource(token); connect.CancelAfter(TimeSpan.FromSeconds(10));
                var connection = await runtime.GetActivityConnectionAsync(connect.Token).ConfigureAwait(false);
                using var request = Request(HttpMethod.Get, new Uri(connection.Origin, "/internal/runtime/calls/stream?" + query + "&after=" + after), connection.BearerToken);
                request.Headers.TryAddWithoutValidation("Last-Event-ID", after.ToString(System.Globalization.CultureInfo.InvariantCulture));
                request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("text/event-stream"));
                using var response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, connect.Token).ConfigureAwait(false);
                if (!response.IsSuccessStatusCode) throw ResponseError(response, await ReadBoundedAsync(response.Content, 65536, connect.Token).ConfigureAwait(false));
                if (response.Content.Headers.ContentType?.MediaType != "text/event-stream") throw new InvalidDataException("服务未返回执行记录事件流。");
                await received(new("connected", after, default)).ConfigureAwait(false);
                await using var stream = await response.Content.ReadAsStreamAsync(token).ConfigureAwait(false);
                using var reader = new StreamReader(stream, new UTF8Encoding(false, true), true, 4096, leaveOpen: true);
                var parser = new ExecutionSseReader(reader);
                while (!token.IsCancellationRequested)
                {
                    using var silence = CancellationTokenSource.CreateLinkedTokenSource(token); silence.CancelAfter(TimeSpan.FromSeconds(45));
                    var message = await parser.ReadEventAsync(silence.Token).ConfigureAwait(false);
                    if (message is null) throw new EndOfStreamException("执行流已断开，正在按序号重连。");
                    if (message.Kind == "heartbeat") continue;
                    if (message.Kind is "call" or "cursor" && message.Seq <= after) continue;
                    if (message.Kind == "call" && message.Value.Number("updated_seq") != (long)message.Seq) throw new InvalidDataException("调用序号与事件序号不一致。");
                    await received(message).ConfigureAwait(false);
                    after = message.Kind == "reset" ? message.Seq : Math.Max(after, message.Seq);
                }
            }
            catch (OperationCanceledException) when (token.IsCancellationRequested) { return; }
            catch (Exception ex) when (ex is HttpRequestException or IOException or JsonException or OperationCanceledException or DecoderFallbackException)
            {
                if (token.IsCancellationRequested) return;
                await received(new("disconnected", after, default, ex.Message)).ConfigureAwait(false);
            }
            try { await Task.Delay(TimeSpan.FromSeconds(1), token).ConfigureAwait(false); }
            catch (OperationCanceledException) when (token.IsCancellationRequested) { return; }
        }
    }
}

internal sealed class ExecutionSseReader(TextReader reader)
{
    internal const int MaximumEventCharacters = 1024 * 1024;
    private readonly char[] _buffer = new char[4096];
    private int _offset, _count;
    internal async Task<ExecutionStreamMessage?> ReadEventAsync(CancellationToken token)
    {
        var kind = ""; var id = ""; var characters = 0; var data = new StringBuilder();
        while (true)
        {
            var line = await ReadLineAsync(token).ConfigureAwait(false); if (line is null) return null;
            characters += line.Length; if (characters > MaximumEventCharacters) throw new IOException("执行事件超过大小限制。");
            if (line.Length == 0)
            {
                if (data.Length == 0) return new("heartbeat", 0, default);
                if (!ulong.TryParse(id, out var sequence)) throw new IOException("执行事件序号无效。");
                using var parsed = JsonDocument.Parse(data.ToString());
                return new(kind, sequence, parsed.RootElement.Clone());
            }
            if (line[0] == ':') continue;
            var colon = line.IndexOf(':'); var key = colon < 0 ? line : line[..colon]; var value = colon < 0 ? "" : line[(colon + 1)..]; if (value.StartsWith(' ')) value = value[1..];
            switch (key)
            {
                case "event": kind = value; break;
                case "id": if (value.Contains('\0')) throw new IOException("执行事件 ID 无效。"); id = value; break;
                case "data": if (data.Length > 0) data.Append('\n'); data.Append(value); break;
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
                _count = await reader.ReadAsync(_buffer.AsMemory(), token).ConfigureAwait(false); _offset = 0;
                if (_count == 0) return line.Length == 0 ? null : line.ToString();
            }
            var end = Array.IndexOf(_buffer, '\n', _offset, _count - _offset); var length = (end < 0 ? _count : end) - _offset;
            if (line.Length + length > MaximumEventCharacters) throw new IOException("执行事件行超过大小限制。");
            line.Append(_buffer, _offset, length); _offset += length;
            if (end >= 0) { _offset++; if (line.Length > 0 && line[^1] == '\r') line.Length--; return line.ToString(); }
        }
    }
}
