using System.IO;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Windows;
using Microsoft.Win32;
using Application = System.Windows.Application;

namespace AgentDock.ControlPanel;

internal static class DesktopTheme
{
    private const long MaximumPreferenceBytes = 131072;
    private static string _path = "";
    private static ResourceDictionary? _colors;
    private static bool _subscribed;
    internal static string Preference { get; private set; } = "system";
    private static string _warningKey = "";
    private static string _warningDetail = "";
    internal static string LoadWarning => _warningKey.Length == 0 ? "" : UiText.Format(_warningKey, _warningDetail);
    private static void SetWarning(string key = "", string detail = "") { _warningKey = key; _warningDetail = detail; }
    internal static event EventHandler? Changed;

    internal static void Initialize(string runtimeRoot)
    {
        var path = Path.Combine(runtimeRoot, "execution-center-settings.json");
        if (string.Equals(_path, path, StringComparison.OrdinalIgnoreCase)) return;
        _path = path;
        try { Preference = Normalize(ReadPreferences()["theme"]?.GetValue<string>()); SetWarning(); }
        catch (Exception error) when (error is IOException or JsonException or UnauthorizedAccessException or InvalidOperationException)
        { Preference = "system"; SetWarning("ThemeLoadWarning", error.Message); }
        if (!_subscribed) { SystemEvents.UserPreferenceChanged += SystemThemeChanged; _subscribed = true; }
        Apply();
    }

    internal static void Save(string selection)
    {
        if (selection is not ("system" or "light" or "dark")) throw new ArgumentException(UiText.Get("ThemeSelectionInvalid"));
        if (_path.Length == 0) throw new InvalidOperationException(UiText.Get("ThemeNotInitialized"));
        var preferences = ReadPreferences();
        preferences["theme"] = selection;
        var temporary = _path + "." + Guid.NewGuid().ToString("N") + ".tmp";
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(_path)!);
            File.WriteAllText(temporary, preferences.ToJsonString(ActivityClient.JsonOptions));
            File.Move(temporary, _path, true);
        }
        finally
        {
            if (File.Exists(temporary))
                try { File.Delete(temporary); }
                catch (Exception error) when (error is IOException or UnauthorizedAccessException) { SetWarning("ThemeCleanupWarning", error.Message); }
        }
        Preference = selection;
        SetWarning();
        Apply();
    }

    private static JsonObject ReadPreferences()
    {
        if (!File.Exists(_path)) return new JsonObject();
        if (new FileInfo(_path).Length > MaximumPreferenceBytes) throw new IOException(UiText.Get("ThemePreferencesTooLarge"));
        return JsonNode.Parse(File.ReadAllText(_path), documentOptions: new JsonDocumentOptions { MaxDepth = 48 }) as JsonObject
            ?? throw new JsonException(UiText.Get("ThemePreferencesObjectRequired"));
    }

    private static string Normalize(string? selection) => selection is "light" or "dark" ? selection : "system";

    private static bool IsDark()
    {
        if (Preference != "system") return Preference == "dark";
        try
        {
            using var key = Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize");
            return key?.GetValue("AppsUseLightTheme") is int value && value == 0;
        }
        catch (Exception error) when (error is UnauthorizedAccessException or System.Security.SecurityException) { return false; }
    }

    private static void Apply()
    {
        var application = Application.Current;
        if (application is null) return;
        var dictionaries = application.Resources.MergedDictionaries;
        var colors = new ResourceDictionary { Source = new Uri("/agentdock-tray;component/Themes/" + (IsDark() ? "Dark" : "Light") + ".xaml", UriKind.Relative) };
        var previous = _colors is null ? -1 : dictionaries.IndexOf(_colors);
        if (previous < 0)
        {
            previous = Enumerable.Range(0, dictionaries.Count).FirstOrDefault(index => dictionaries[index].Source?.OriginalString.EndsWith("/Light.xaml", StringComparison.OrdinalIgnoreCase) == true || dictionaries[index].Source?.OriginalString.EndsWith("/Dark.xaml", StringComparison.OrdinalIgnoreCase) == true, -1);
        }
        if (previous >= 0) dictionaries[previous] = colors; else dictionaries.Add(colors);
        _colors = colors;
        Changed?.Invoke(null, EventArgs.Empty);
    }

    private static void SystemThemeChanged(object sender, UserPreferenceChangedEventArgs args)
    {
        if (Preference == "system") Application.Current?.Dispatcher.BeginInvoke(Apply);
    }

    internal static void Dispose()
    {
        if (_subscribed) SystemEvents.UserPreferenceChanged -= SystemThemeChanged;
        _subscribed = false;
    }
}
