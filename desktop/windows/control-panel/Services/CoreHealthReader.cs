using System.IO;
using System.Net;
using System.Net.Http;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace AgentDock.ControlPanel;

// Dedicated loopback-only reader. It does not share the public URL checker or
// any runtime/task control, proxy, cookie, redirect or authentication state.
internal sealed class CoreHealthReader : IDisposable
{
    internal const int MaximumBodyBytes = 16 * 1024;
    private readonly HttpClient _client;
    private readonly CancellationTokenSource _lifetime = new();
    private int _disposed;
    private static readonly Regex VersionPattern = new(@"\Av?[0-9]{1,5}\.[0-9]{1,5}\.[0-9]{1,8}(?:[-+][A-Za-z0-9.-]{1,48})?\z", RegexOptions.CultureInvariant);

    public CoreHealthReader(HttpMessageHandler? handler = null)
    {
        _client = new HttpClient(handler ?? CreateHandler()) { Timeout = Timeout.InfiniteTimeSpan };
    }
    public static HttpClientHandler CreateHandler() => new() { AllowAutoRedirect = false, UseProxy = false, UseCookies = false, MaxResponseHeadersLength = 8 };
    public static bool ValidVersion(string? version) => version is not null && version.Length <= 80 && (version == "dev" || VersionPattern.IsMatch(version));

    public async Task<(bool Healthy, string Version)> ReadAsync(string origin, CancellationToken cancellationToken)
    {
        ObjectDisposedException.ThrowIf(Volatile.Read(ref _disposed) != 0, this);
        cancellationToken.ThrowIfCancellationRequested();
        if (!Uri.TryCreate(origin, UriKind.Absolute, out var uri) || uri.Scheme != "http" || uri.UserInfo.Length != 0 ||
            uri.AbsolutePath != "/" || uri.Query.Length != 0 || uri.Fragment.Length != 0 ||
            !IPAddress.TryParse(uri.IdnHost.Trim('[', ']'), out var address) || !IPAddress.IsLoopback(address)) return (false, "");
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _lifetime.Token);
        timeout.CancelAfter(TimeSpan.FromSeconds(2));
        try
        {
            using var request = new HttpRequestMessage(HttpMethod.Get, new Uri(uri, "/healthz"));
            using var response = await _client.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, timeout.Token).ConfigureAwait(false);
            if (response.StatusCode != HttpStatusCode.OK || response.Content.Headers.ContentLength > MaximumBodyBytes ||
                !string.Equals(response.Content.Headers.ContentType?.MediaType, "application/json", StringComparison.OrdinalIgnoreCase)) return (false, "");
            await using var stream = await response.Content.ReadAsStreamAsync(timeout.Token).ConfigureAwait(false);
            var bytes = new byte[MaximumBodyBytes + 1]; var length = 0;
            while (length < bytes.Length)
            {
                var read = await stream.ReadAsync(bytes.AsMemory(length), timeout.Token).ConfigureAwait(false);
                if (read == 0) break;
                length += read;
            }
            if (length > MaximumBodyBytes) return (false, "");
            using var document = JsonDocument.Parse(bytes.AsMemory(0, length), new JsonDocumentOptions { MaxDepth = 8 });
            var root = document.RootElement;
            if (root.ValueKind != JsonValueKind.Object) return (false, "");
            var names = new HashSet<string>(StringComparer.Ordinal);
            foreach (var property in root.EnumerateObject()) if (!names.Add(property.Name) || names.Count > 32) return (false, "");
            if (!root.TryGetProperty("ok", out var ok) || ok.ValueKind != JsonValueKind.True ||
                !root.TryGetProperty("service", out var service) || service.ValueKind != JsonValueKind.String || service.GetString() != "agentdock" ||
                !root.TryGetProperty("process_id", out var pid) || pid.ValueKind != JsonValueKind.Number || !pid.TryGetInt32(out var processID) || processID <= 0 ||
                !root.TryGetProperty("version", out var version) || version.ValueKind != JsonValueKind.String || !ValidVersion(version.GetString())) return (false, "");
            return (true, version.GetString()!);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested && !_lifetime.IsCancellationRequested) { return (false, ""); }
        catch (Exception error) when (error is HttpRequestException or IOException or JsonException) { return (false, ""); }
    }
    public void Dispose()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        _lifetime.Cancel(); _client.Dispose();
    }
}
