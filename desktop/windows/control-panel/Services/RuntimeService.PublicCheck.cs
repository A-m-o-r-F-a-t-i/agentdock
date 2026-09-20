using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;

namespace AgentDock.ControlPanel;

public sealed partial class RuntimeService
{
    private static async Task<UrlTestResult> TestPublicDiscoveryAsync(string value, CancellationToken cancellationToken)
    {
        if (!Uri.TryCreate(value, UriKind.Absolute, out var uri) || uri.UserInfo.Length > 0 ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)))
            return new UrlTestResult(false, null, TimeSpan.Zero, UiText.Get("InvalidPublicAddress"));
        using var client = new HttpClient(new SocketsHttpHandler { AllowAutoRedirect = false, ConnectTimeout = TimeSpan.FromSeconds(6) });
        return await CheckPublicDiscoveryAsync(client, uri.GetLeftPart(UriPartial.Authority), cancellationToken);
    }

    // This check is anonymous and never attaches local credentials. OAuth consent
    // and actual client tool discovery remain separate acceptance steps.
    internal static async Task<UrlTestResult> CheckPublicDiscoveryAsync(HttpClient client, string origin, CancellationToken cancellationToken)
    {
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        deadline.CancelAfter(TimeSpan.FromSeconds(24));
        var clock = Stopwatch.StartNew();
        var stage = UiText.Get("PublicNetwork");
        var path = "/healthz";
        int? status = null;
        try
        {
            async Task<HttpResponseMessage> Get(string endpoint, HttpStatusCode expected)
            {
                path = endpoint; status = null;
                using var request = new HttpRequestMessage(HttpMethod.Get, origin + path);
                request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/json"));
                var response = await client.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, deadline.Token);
                status = (int)response.StatusCode;
                if (response.StatusCode != expected)
                {
                    response.Dispose();
                    throw new HttpRequestException($"HTTP {status}; expected {(int)expected}", null, (HttpStatusCode)status);
                }
                return response;
            }
            using (var health = await Get("/healthz", HttpStatusCode.OK)) { }
            stage = UiText.Get("PublicDiscovery");
            string resourceMetadata;
            using (var challenge = await Get("/mcp", HttpStatusCode.Unauthorized))
            {
                var bearer = challenge.Headers.WwwAuthenticate.Where(item => item.Scheme.Equals("Bearer", StringComparison.OrdinalIgnoreCase)).ToList();
                if (bearer.Count != 1 || !TryResourceMetadata(bearer[0].Parameter, out resourceMetadata) ||
                    resourceMetadata != origin + "/.well-known/oauth-protected-resource/mcp")
                    throw new InvalidDataException(UiText.Get("PublicMetadataMismatch"));
            }
            using (var resource = await Get("/.well-known/oauth-protected-resource/mcp", HttpStatusCode.OK))
            using (var document = await ReadPublicJsonAsync(resource, deadline.Token))
            {
                var root = document.RootElement;
                if (!StringEquals(root, "resource", origin + "/mcp") || !root.TryGetProperty("authorization_servers", out var servers) ||
                    servers.ValueKind != JsonValueKind.Array || servers.GetArrayLength() != 1 || servers[0].GetString() != origin)
                    throw new InvalidDataException(UiText.Get("PublicMetadataMismatch"));
            }
            using (var authorization = await Get("/.well-known/oauth-authorization-server", HttpStatusCode.OK))
            using (var document = await ReadPublicJsonAsync(authorization, deadline.Token))
            {
                var root = document.RootElement;
                foreach (var (key, suffix) in new[] { ("issuer", ""), ("authorization_endpoint", "/oauth/authorize"),
                    ("token_endpoint", "/oauth/token"), ("registration_endpoint", "/register") })
                    if (!StringEquals(root, key, origin + suffix)) throw new InvalidDataException(UiText.Get("PublicMetadataMismatch") + ": " + key);
                if (!Contains(root, "code_challenge_methods_supported", "S256") || !Contains(root, "grant_types_supported", "authorization_code") ||
                    !Contains(root, "grant_types_supported", "refresh_token"))
                    throw new InvalidDataException(UiText.Get("PublicOAuthCapabilitiesMissing"));
            }
            return new UrlTestResult(true, status, clock.Elapsed, UiText.Get("PublicDiscoveryAuthorizationRequired") +
                "\n" + UiText.Format("PublicCheckStage", stage, origin + path, status, DateTimeOffset.Now));
        }
        catch (Exception ex) when (ex is HttpRequestException or OperationCanceledException or IOException or InvalidDataException or JsonException or InvalidOperationException or FormatException)
        {
            var detail = ex is OperationCanceledException ? UiText.Get("AccessTimeout") : ex.Message;
            return new UrlTestResult(false, status, clock.Elapsed, UiText.Format("PublicCheckError", stage, origin + path,
                (status is null ? "" : $"HTTP {status} · ") + detail, DateTimeOffset.Now));
        }
    }

    private static bool StringEquals(JsonElement item, string name, string value) => item.TryGetProperty(name, out var field) &&
        field.ValueKind == JsonValueKind.String && field.GetString() == value;
    private static bool Contains(JsonElement item, string name, string value) => item.TryGetProperty(name, out var field) &&
        field.ValueKind == JsonValueKind.Array && field.EnumerateArray().Any(element => element.ValueKind == JsonValueKind.String && element.GetString() == value);

    private static async Task<JsonDocument> ReadPublicJsonAsync(HttpResponseMessage response, CancellationToken cancellationToken)
    {
        const int limit = 256 * 1024;
        if (response.Content.Headers.ContentLength > limit) throw new InvalidDataException("Metadata exceeds 256 KiB.");
        await using var input = await response.Content.ReadAsStreamAsync(cancellationToken);
        using var output = new MemoryStream();
        var buffer = new byte[8192];
        while (true)
        {
            var count = await input.ReadAsync(buffer, cancellationToken);
            if (count == 0) break;
            if (output.Length + count > limit) throw new InvalidDataException("Metadata exceeds 256 KiB.");
            output.Write(buffer, 0, count);
        }
        return JsonDocument.Parse(output.ToArray(), new JsonDocumentOptions { MaxDepth = 32 });
    }

    internal static bool TryResourceMetadata(string? parameters, out string resource)
    {
        resource = "";
        if (string.IsNullOrWhiteSpace(parameters) || parameters.Length > 8192) return false;
        var parts = new List<string>();
        var quoted = false; var escaped = false; var start = 0;
        for (var i = 0; i < parameters.Length; i++)
        {
            var ch = parameters[i];
            if (ch < 32 && ch != '\t' || ch == 127) return false;
            if (escaped) { escaped = false; continue; }
            if (quoted && ch == '\\') { escaped = true; continue; }
            if (ch == '"') quoted = !quoted;
            if (ch == ',' && !quoted) { parts.Add(parameters[start..i]); start = i + 1; }
        }
        if (quoted || escaped) return false;
        parts.Add(parameters[start..]);
        var seen = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        foreach (var part in parts)
        {
            if (!NameValueHeaderValue.TryParse(part.Trim(), out var parsed) || !seen.Add(parsed.Name) || parsed.Value is null) return false;
            if (!parsed.Name.Equals("resource_metadata", StringComparison.OrdinalIgnoreCase)) continue;
            var value = parsed.Value;
            if (value.StartsWith('"') && value.EndsWith('"'))
            {
                var decoded = new StringBuilder();
                for (var i = 1; i < value.Length - 1; i++) { if (value[i] == '\\') i++; decoded.Append(value[i]); }
                resource = decoded.ToString();
            }
            else resource = value;
        }
        return resource.Length > 0;
    }
}
