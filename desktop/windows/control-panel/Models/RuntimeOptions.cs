using System.Text.Json.Serialization;

namespace AgentDock.ControlPanel;

public sealed class RuntimeOptions
{
    [JsonPropertyName("default_dir")]
    public string DefaultDir { get; set; } = "";

    [JsonPropertyName("agents_autoload")]
    public bool AgentsAutoLoad { get; set; } = true;

    [JsonPropertyName("instructions_file")]
    public string InstructionsFile { get; set; } = "";

    [JsonPropertyName("browser_executable_path")]
    public string BrowserExecutablePath { get; set; } = "";

    [JsonPropertyName("trusted_proxy_cidrs")]
    public List<string> TrustedProxyCidrs { get; set; } = [];

    [JsonPropertyName("command_env_from_env")]
    public Dictionary<string, string> CommandEnvFromEnv { get; set; } = [];

    [JsonPropertyName("acp_max_concurrent_prompts")]
    public int AcpMaxConcurrentPrompts { get; set; } = 2;

    [JsonPropertyName("acp_interaction_timeout_ms")]
    public int AcpInteractionTimeoutMs { get; set; } = 300000;
}

public sealed class RuntimeOptionsView
{
    [JsonPropertyName("options")]
    public RuntimeOptions Options { get; set; } = new();

    [JsonPropertyName("agentdock_home")]
    public string AgentDockHome { get; set; } = "";

    [JsonPropertyName("settings_path")]
    public string SettingsPath { get; set; } = "";

    [JsonPropertyName("manifest_path")]
    public string ManifestPath { get; set; } = "";
}
