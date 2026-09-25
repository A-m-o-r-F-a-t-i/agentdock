using System.Diagnostics;
using System.IO;
using System.Text.Json;

namespace AgentDock.ControlPanel;

// One bounded display cache. No command, stable shim, recovery entry or task is
// ever launched just to paint a version label. File/pointer replacement changes
// the key immediately; unknown values are cached too, preventing polling loops.
internal sealed class CoreVersionCache : IDisposable
{
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly CancellationTokenSource _lifetime = new();
    private readonly Func<string, string, CancellationToken, Task<string>> _read;
    private readonly TimeProvider _clock;
    private string _key = "", _value = "";
    private DateTimeOffset _expires;
    private int _disposed;

    public CoreVersionCache(Func<string, string, CancellationToken, Task<string>>? read = null, TimeProvider? clock = null)
    { _read = read ?? ReadInstalledAsync; _clock = clock ?? TimeProvider.System; }

    private static string Identity(string path)
    {
        var file = new FileInfo(path);
        return file.FullName + "|" + (file.Exists ? file.Length + "|" + file.LastWriteTimeUtc.Ticks + "|" + file.CreationTimeUtc.Ticks : "missing");
    }
    public async Task<string> ReadAsync(string binaryPath, string pointerPath, CancellationToken cancellationToken)
    {
        ObjectDisposedException.ThrowIf(Volatile.Read(ref _disposed) != 0, this);
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _lifetime.Token);
        linked.Token.ThrowIfCancellationRequested();
        await _gate.WaitAsync(linked.Token).ConfigureAwait(false);
        try
        {
            // Sample inside the single-flight gate, including the actual generation
            // file. A negative dependency must notice creation as well as deletion.
            var key = await DependencyKeyAsync(binaryPath, pointerPath, linked.Token).ConfigureAwait(false);
            if (_key == key && _clock.GetUtcNow() < _expires) return _value;
            string value;
            try { value = await _read(binaryPath, pointerPath, linked.Token).ConfigureAwait(false); }
            catch (Exception error) when (error is IOException or UnauthorizedAccessException or JsonException or System.ComponentModel.Win32Exception) { value = ""; }
            linked.Token.ThrowIfCancellationRequested();
            if (key != await DependencyKeyAsync(binaryPath, pointerPath, linked.Token).ConfigureAwait(false))
            {
                _key = ""; _value = "";
                return ""; // Do not publish a value sampled across a file/pointer replacement.
            }
            _key = key; _value = CoreHealthReader.ValidVersion(value) ? value : "";
            _expires = _clock.GetUtcNow().AddSeconds(_value.Length == 0 ? 30 : 300);
            return _value;
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or ArgumentException) { return ""; }
        finally { _gate.Release(); }
    }

    private static async Task<string> DependencyKeyAsync(string binaryPath, string pointerPath, CancellationToken token)
    {
        var key = Identity(binaryPath) + "|" + Identity(pointerPath);
        string? core = null;
        if (File.Exists(pointerPath))
        {
            try { (_, core) = await ReadGenerationAsync(pointerPath, token).ConfigureAwait(false); }
            catch (JsonException) { } // Invalid pointer remains a cacheable unknown.
        }
        var target = core is null ? "no-generation" : Identity(core);
        if (key != Identity(binaryPath) + "|" + Identity(pointerPath))
            throw new IOException("Installed version dependencies changed while reading.");
        return key + "|" + target;
    }

    public static async Task<string> ReadInstalledAsync(string binaryPath, string pointerPath, CancellationToken cancellationToken)
    {
        if (File.Exists(pointerPath))
        {
            var (version, core) = await ReadGenerationAsync(pointerPath, cancellationToken).ConfigureAwait(false);
            return core is not null && File.Exists(core) ? version : "";
        }
        if (!File.Exists(binaryPath)) return "";
        return await Task.Run(() =>
        {
            cancellationToken.ThrowIfCancellationRequested();
            var metadata = FileVersionInfo.GetVersionInfo(binaryPath);
            var version = metadata.ProductVersion?.Trim();
            return CoreHealthReader.ValidVersion(version) ? version! : "";
        }, cancellationToken).ConfigureAwait(false);
    }
    private static async Task<(string Version, string? Core)> ReadGenerationAsync(string pointerPath, CancellationToken cancellationToken)
    {
        await using var stream = new FileStream(pointerPath, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete, 4096, FileOptions.Asynchronous);
        var bytes = new byte[16 * 1024 + 1]; var length = 0;
        while (length < bytes.Length)
        {
            var read = await stream.ReadAsync(bytes.AsMemory(length), cancellationToken).ConfigureAwait(false);
            if (read == 0) break;
            length += read;
        }
        if (length > 16 * 1024) return ("", null);
        using var document = JsonDocument.Parse(bytes.AsMemory(0, length), new JsonDocumentOptions { MaxDepth = 8 });
        var root = document.RootElement;
        if (root.ValueKind != JsonValueKind.Object || !root.TryGetProperty("schema_version", out var schema) || schema.ValueKind != JsonValueKind.Number || !schema.TryGetInt32(out var versionSchema) || versionSchema != 1 ||
            !root.TryGetProperty("active_version", out var active) || active.ValueKind != JsonValueKind.String) return ("", null);
        var names = new HashSet<string>(StringComparer.Ordinal);
        foreach (var property in root.EnumerateObject()) if (!names.Add(property.Name) || names.Count > 32) return ("", null);
        var version = active.GetString();
        if (!CoreHealthReader.ValidVersion(version) || version == "dev") return ("", null);
        var generation = "v" + version!.TrimStart('v');
        var core = Path.Combine(Path.GetDirectoryName(pointerPath)!, "versions", generation, "agentdock-core.exe");
        return (version.TrimStart('v'), core);
    }
    public void Dispose()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        _lifetime.Cancel();
    }
}
