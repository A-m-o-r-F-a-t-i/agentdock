package desktopruntime

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPublicAccessLegacyProjection(t *testing.T) {
	for _, mode := range []string{"none", "quick", "named"} {
		manifest := Manifest{TunnelMode: mode, PublicURL: "https://example.test"}
		got := manifest.EffectivePublicAccess()
		if mode == "none" {
			if got.Provider != "none" || got.Mode != "local" || got.URL != "" {
				t.Fatalf("local: %+v", got)
			}
		} else if got.Provider != "cloudflare" || got.Mode != mode || got.URL != manifest.PublicURL {
			t.Fatalf("%s: %+v", mode, got)
		}
	}
}

func TestPublicAccessTailscaleRoundTripAndDowngrade(t *testing.T) {
	root := t.TempDir()
	manifest := Manifest{SchemaVersion: 1, AgentDockBinary: filepath.Join(root, "agentdock.exe"),
		Host: "127.0.0.1", Port: 8765, LocalMCPURL: "http://127.0.0.1:8765/mcp", TunnelMode: "none"}
	manifest.setPublicAccess("tailscale", "funnel", "https://device.example-tailnet.ts.net")
	manifest.TailscaleBinary = filepath.Join(root, "tailscale.exe")
	path := filepath.Join(root, "runtime.json")
	if err := Save(path, manifest); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectivePublicAccess() != manifest.EffectivePublicAccess() || got.TailscaleBinary != manifest.TailscaleBinary {
		t.Fatalf("round trip: %+v", got)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		SchemaVersion int    `json:"schema_version"`
		TunnelMode    string `json:"tunnel_mode"`
		PublicURL     string `json:"public_url"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.SchemaVersion != 1 || legacy.TunnelMode != "none" || legacy.PublicURL != "" {
		t.Fatalf("unsafe downgrade projection: %+v", legacy)
	}
}

func TestPublicAccessRejectsInvalidCombinations(t *testing.T) {
	for _, access := range []PublicAccess{
		{"tailscale", "quick", "https://device.example-tailnet.ts.net"},
		{"tailscale", "funnel", "https://example.com"},
		{"tailscale", "funnel", "https://device.example-tailnet.ts.net/mcp"},
		{"none", "local", "https://example.com"},
		{"cloudflare", "funnel", "https://example.com"},
		{"unknown", "local", ""},
		{"", "funnel", ""},
	} {
		m := Manifest{TunnelMode: "none", PublicAccessProvider: access.Provider,
			PublicAccessMode: access.Mode, PublicAccessURL: access.URL}
		if err := m.validatePublicAccess(); err == nil {
			t.Errorf("accepted invalid access %+v", access)
		}
	}
}

func TestPublicAccessConfigureValidation(t *testing.T) {
	for _, request := range []TunnelConfigureRequest{
		{Mode: "none"}, {Mode: "quick"}, {Mode: "named"},
		{Provider: "none", Mode: "local"}, {Provider: "cloudflare", Mode: "quick"},
		{Provider: "tailscale", Mode: "funnel"},
	} {
		got, err := normalizeTunnelConfigureRequest(request)
		if err != nil || got.Provider == "" {
			t.Errorf("%+v: %+v, %v", request, got, err)
		}
	}
	for _, request := range []TunnelConfigureRequest{
		{Mode: "funnel"}, {Provider: "tailscale", Mode: "named"},
		{Provider: "tailscale", Mode: "funnel", TokenFile: "secret"},
		{Provider: "tailscale", Mode: "funnel", ServerURL: "https://fake.example.ts.net"},
		{Mode: "quick", TokenFile: "secret"}, {Mode: "none", TailscaleBinary: "test"},
	} {
		if _, err := normalizeTunnelConfigureRequest(request); err == nil {
			t.Errorf("accepted %+v", request)
		}
	}
}

func TestTailscaleDNSValidation(t *testing.T) {
	got, err := normalizeTailscaleDNSName("Device.Example-Tailnet.ts.net.")
	if err != nil || got != "device.example-tailnet.ts.net" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"", "ts.net", "x.ts.net", "device..ts.net", "device.example.ts.net:443", "-device.example.ts.net", "device.example.ts.net/", "evil@example.ts.net", "device.example.ts.net.evil.test"} {
		if _, err := normalizeTailscaleDNSName(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, bad := range []string{"http://device.example.ts.net", "https://u:p@device.example.ts.net", "https://device.example.ts.net:443", "https://device.example.ts.net?", "https://device.example.ts.net/#x"} {
		if _, err := normalizeTailscaleOrigin(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
