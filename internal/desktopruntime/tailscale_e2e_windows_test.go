//go:build windows

package desktopruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/publicartifacts"
)

func buildTailscaleE2EBinary(t *testing.T, target, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(goruntime.GOROOT(), "bin", "go.exe"), "build", "-o", binary, target)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", target, err, output)
	}
	return binary
}

func writeTailscaleFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func reserveTailscaleE2EPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func loopbackFunnelHTTPClient(t *testing.T, localOrigin string) *http.Client {
	t.Helper()
	target, err := url.Parse(localOrigin)
	if err != nil {
		t.Fatal(err)
	}
	client := newTailscaleHTTPClient()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	client.Transport = tailscaleTestTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "device.example-tailnet.ts.net" {
			return nil, errors.New("E2E refused an unexpected public host")
		}
		clone := request.Clone(request.Context())
		copiedURL := *request.URL
		clone.URL = &copiedURL
		clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
		return transport.RoundTrip(clone)
	})
	return client
}

// The Tailscale process is a separate fake executable. Core, DPAPI credentials,
// HTTP routes, OAuth origin, startup adapter and file transactions are real.
// TLS termination is simulated by loopback transport; no public Funnel or user
// runtime is modified by this test.
func TestTailscaleRealCoreAndFakeCLILifecycle(t *testing.T) {
	fakeBinary := buildTailscaleE2EBinary(t, "./scripts/test/testdata/fake-tailscale", "tailscale.exe")
	coreBinary := buildTailscaleE2EBinary(t, "./cmd/agentdock", "agentdock.exe")
	root := t.TempDir()
	fixturePath := filepath.Join(root, "fake-tailscale.json")
	original := testServeWithHandlers(true, map[string]*tailscaleHTTPHandler{"/other": {Proxy: "http://127.0.0.1:9000"}})
	writeTailscaleFixture(t, fixturePath, map[string]any{"config": original})
	t.Setenv("FAKE_TAILSCALE_STATE", fixturePath)
	port := reserveTailscaleE2EPort(t)
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: 1, InstallRoot: root, AgentDockBinary: coreBinary, AgentDockHome: home, AgentDockDefaultDir: workspace, CloudflaredBinary: filepath.Join(root, "missing-cloudflared.exe"),
		Host: "127.0.0.1", Port: port, LocalMCPURL: localTailscaleOrigin(port) + "/mcp", TunnelMode: "none", PrivilegeMode: "standard", TailscaleBinary: fakeBinary,
		CloudflaredStartupValueName: "AgentDockFunnelE2E-" + filepath.Base(root)}
	if err := Save(filepath.Join(root, "runtime.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeText(filepath.Join(root, "cloudflared-mode.txt"), "none"); err != nil {
		t.Fatal(err)
	}
	if err := bindCredentialOwnerSID(filepath.Join(root, "auth-token.dpapi")); err != nil {
		t.Fatal(err)
	}
	runtime, err := loadTunnelRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := platformServiceAction(ctx, root, "stop"); err != nil {
			t.Errorf("Core cleanup: %v", err)
		}
	})
	hooks := defaultPublicAccessHooks()
	hooks.verifyOrigin = func(ctx context.Context, rt tunnelRuntime, origin string) error {
		token, err := readProtectedText(filepath.Join(root, "auth-token.dpapi"), "agentdock.startup.v1")
		if err != nil {
			return err
		}
		return waitTailscalePublicOrigin(ctx, origin, token, loopbackFunnelHTTPClient(t, rt.localOrigin()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := configureTailscaleAccess(ctx, runtime, fakeBinary, true, hooks); err != nil {
		t.Fatal(err)
	}
	runtime, err = loadTunnelRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	status := inspectTailscaleRuntime(ctx, runtime, fakeBinary, newWindowsTailscaleClient(fakeBinary))
	if status.Ready || !status.LocalReady || status.Phase != "VerifyingPublic" {
		t.Fatalf("local commit was confused with public readiness: %+v", status)
	}
	status, err = verifyConfiguredTailscale(ctx, root, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || !status.Running || status.Provider != "tailscale" {
		t.Fatalf("not ready: %+v", status)
	}
	client := loopbackFunnelHTTPClient(t, runtime.localOrigin())
	if _, err := tailscaleGet(ctx, client, status.PublicURL, "/context", http.StatusUnauthorized); err != nil {
		t.Fatal(err)
	}
	artifactStore := publicartifacts.New(home, status.PublicURL, port)
	artifact, err := artifactStore.PublishBytes(publicartifacts.PublishBytesRequest{Filename: "funnel-probe.txt", Data: []byte("signed artifact probe"), RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(body) != "signed artifact probe" {
		t.Fatalf("signed artifact: HTTP %d %v", response.StatusCode, err)
	}
	unsigned := *request.URL
	unsigned.RawQuery = ""
	request.URL = &unsigned
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode < 400 {
		t.Fatal("unsigned artifact was accepted")
	}
	if err := hooks.restartCore(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := configureTailscaleAccess(ctx, runtime, fakeBinary, false, hooks); err != nil {
		t.Fatal(err)
	}
	if err := stopTailscaleAccess(ctx, runtime, fakeBinary, hooks); err != nil {
		t.Fatal(err)
	}
	_, config, err := newWindowsTailscaleClient(fakeBinary).observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sameTailscaleServe(config, original) {
		t.Fatal("stop changed unrelated mappings")
	}
	if err := configureTailscaleAccess(ctx, runtime, fakeBinary, false, hooks); err != nil {
		t.Fatal(err)
	}
	requestConfig := TunnelConfigureRequest{RuntimeRoot: root, Provider: "none", Mode: "none"}
	if err := leaveTailscaleAccess(ctx, runtime, fakeBinary, requestConfig, hooks); err != nil {
		t.Fatal(err)
	}
	localManifest, err := Load(filepath.Join(root, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if localManifest.EffectivePublicAccess().Provider != "none" {
		t.Fatal("local mode not committed")
	}
	if url, err := readTrimmedText(filepath.Join(root, "server-url.txt")); err != nil || url != "" {
		t.Fatal("old OAuth origin survived local mode")
	}
	_, config, err = newWindowsTailscaleClient(fakeBinary).observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sameTailscaleServe(config, original) {
		t.Fatal("mode switch changed unrelated mappings")
	}
	t.Log("PASS: real Core restart, OAuth/challenge, auth initialize, protected context/runtime, signed artifacts, Funnel stop/start and local mode; no production Tailscale changes")
}

func TestTailscaleProcessLimitsAndPartialFailure(t *testing.T) {
	binary := buildTailscaleE2EBinary(t, "./scripts/test/testdata/fake-tailscale", "tailscale.exe")
	path := filepath.Join(t.TempDir(), "fixture.json")
	t.Setenv("FAKE_TAILSCALE_STATE", path)
	client := newWindowsTailscaleClient(binary)
	writeTailscaleFixture(t, path, map[string]any{"delay_ms": 5000, "config": map[string]any{}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	start := time.Now()
	_, err := client.readNode(ctx)
	cancel()
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not enforced: %v", err)
	}
	writeTailscaleFixture(t, path, map[string]any{"output_bytes": tailscaleJSONLimit + 1, "config": map[string]any{}})
	if _, err := client.readNode(context.Background()); tailscaleDiagnosticCode(err) != "output_limit" {
		t.Fatal(err)
	}
	writeTailscaleFixture(t, path, map[string]any{"fail_write": 1, "partial_write": true, "config": map[string]any{}})
	node, config, err := client.observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	change, err := prepareTailscaleMapping(node, config, "http://127.0.0.1:"+strconv.Itoa(18765), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	err = change.apply(context.Background(), client)
	if err == nil || strings.Contains(err.Error(), "private-test-secret") {
		t.Fatalf("failure or redaction not enforced: %v", err)
	}
	if err := change.rollback(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	_, config, err = client.observe(context.Background())
	if err != nil || len(config.Web) != 0 {
		t.Fatal("partial process failure did not roll back")
	}
}
