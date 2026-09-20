package installer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/desktopruntime"
	"github.com/uvwt/agentdock/internal/updateengine"
)

func tailscaleInstallFixture(t *testing.T) (Request, desktopruntime.Manifest, stagedInstall) {
	t.Helper()
	root := t.TempDir()
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.EnsureBase(); err != nil {
		t.Fatal(err)
	}
	manifest := desktopruntime.Manifest{SchemaVersion: 1, InstallRoot: root, AgentDockBinary: layout.CoreShim(), Host: "127.0.0.1", Port: 18765, LocalMCPURL: "http://127.0.0.1:18765/mcp", TunnelMode: "none",
		PublicAccessProvider: "tailscale", PublicAccessMode: "funnel", PublicAccessURL: "https://device.example-tailnet.ts.net", TailscaleBinary: filepath.Join(root, "tailscale.exe")}
	if err := desktopruntime.Save(filepath.Join(root, "runtime.json"), manifest); err != nil {
		t.Fatal(err)
	}
	request := Request{InstallRoot: root, RuntimeRoot: root, Version: "1.1.0", TunnelMode: "none"}
	journal := newJournal(root, "tailscale-test")
	return request, manifest, stagedInstall{WindowsLayout: &layout, Journal: journal}
}

func TestWindowsActivatePreservesTailscaleProvider(t *testing.T) {
	request, original, staged := tailscaleInstallFixture(t)
	result, err := activateWindows(context.Background(), request, staged)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := desktopruntime.Load(filepath.Join(request.RuntimeRoot, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if actual.EffectivePublicAccess() != original.EffectivePublicAccess() || actual.TailscaleBinary != original.TailscaleBinary || actual.TunnelMode != "none" || actual.PublicURL != "" || actual.Port != original.Port {
		t.Fatalf("Funnel configuration lost: %+v", actual)
	}
	if result.PublicURL != original.PublicAccessURL {
		t.Fatal("installer result lost public origin")
	}
}

func TestWindowsTailscaleInstallRejectsModeAndPortMutation(t *testing.T) {
	request, original, _ := tailscaleInstallFixture(t)
	for _, edit := range []func(*Request){func(r *Request) { r.TunnelMode = "quick" }, func(r *Request) { r.TunnelMode = "named" }, func(r *Request) { r.Port = 9999 }, func(r *Request) { r.Host = "0.0.0.0" }, func(r *Request) { r.ServerURL = "https://other.example.test" }, func(r *Request) { r.TunnelToken = "test" }} {
		candidate := request
		edit(&candidate)
		if err := validateTailscaleInstallRequest(candidate, original); err == nil {
			t.Errorf("accepted unsafe install change: %+v", candidate)
		}
	}
}

func TestWindowsTailscaleStateParticipatesInRollback(t *testing.T) {
	request, _, staged := tailscaleInstallFixture(t)
	files := map[string]string{"server-url.txt": "https://device.example-tailnet.ts.net", "tailscale-funnel-state.json": "original ownership fixture", "control-panel-settings.json": "original settings fixture", "cloudflared-mode.txt": "none"}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(request.RuntimeRoot, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := snapshotWindowsPublicAccess(request, staged.Journal); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(request.RuntimeRoot, name), []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := staged.Journal.restore(context.Background(), request, false); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(request.RuntimeRoot, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s did not restore: %v", name, err)
		}
	}
}
