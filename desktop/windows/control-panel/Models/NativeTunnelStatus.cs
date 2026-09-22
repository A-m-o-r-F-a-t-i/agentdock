using System.Text.Json.Serialization;

namespace AgentDock.ControlPanel;

public sealed class NativeTunnelStatus
{
    [JsonPropertyName("phase")]
    public string Phase { get; set; } = "";
    [JsonPropertyName("local_ready")]
    public bool LocalReady { get; set; }
    [JsonPropertyName("verified_at")]
    public DateTimeOffset? VerifiedAt { get; set; }
    [JsonPropertyName("funnel_public_probe_ms")]
    public long? PublicProbeMs { get; set; }
    [JsonPropertyName("provider")]
    public string Provider { get; set; } = "";
    [JsonPropertyName("mode")]
    public string Mode { get; set; } = "";
    [JsonPropertyName("installed")]
    public bool Installed { get; set; }
    [JsonPropertyName("configured")]
    public bool Configured { get; set; }
    [JsonPropertyName("running")]
    public bool Running { get; set; }
    [JsonPropertyName("ready")]
    public bool Ready { get; set; }
    [JsonPropertyName("funnel_enabled")]
    public bool FunnelEnabled { get; set; }
    [JsonPropertyName("backend_state")]
    public string BackendState { get; set; } = "";
    [JsonPropertyName("device_name")]
    public string DeviceName { get; set; } = "";
    [JsonPropertyName("dns_name")]
    public string DnsName { get; set; } = "";
    [JsonPropertyName("public_url")]
    public string PublicUrl { get; set; } = "";
    [JsonPropertyName("local_origin")]
    public string LocalOrigin { get; set; } = "";
    [JsonPropertyName("binary_path")]
    public string BinaryPath { get; set; } = "";
    [JsonPropertyName("key_expiry")]
    public DateTimeOffset? KeyExpiry { get; set; }
    [JsonPropertyName("diagnostic")]
    public string Diagnostic { get; set; } = "";
    [JsonPropertyName("diagnostic_code")]
    public string DiagnosticCode { get; set; } = "";
    [JsonPropertyName("authorization_url")]
    public string AuthorizationUrl { get; set; } = "";
}
