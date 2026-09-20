//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type publicAccessTestSystem struct {
	t                *testing.T
	fake             *memoryTailscaleCLI
	hooks            publicAccessHooks
	core             bool
	cloudflare       bool
	startup          bool
	restarts         int
	coreStops        int
	cloudflareStarts int
	verified         int
}

func newPublicAccessTestSystem(t *testing.T, mode string) (tunnelRuntime, *publicAccessTestSystem) {
	t.Helper()
	root := t.TempDir()
	manifest := Manifest{SchemaVersion: 1, AgentDockBinary: filepath.Join(root, "agentdock.exe"), CloudflaredBinary: filepath.Join(root, "cloudflared.exe"), Host: "127.0.0.1", Port: 8765, LocalMCPURL: "http://127.0.0.1:8765/mcp", TunnelMode: mode}
	if mode == "quick" || mode == "named" {
		manifest.PublicURL = "https://old.example.test"
	}
	if err := Save(filepath.Join(root, "runtime.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeText(filepath.Join(root, "cloudflared-mode.txt"), mode); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeText(filepath.Join(root, "server-url.txt"), manifest.PublicURL); err != nil {
		t.Fatal(err)
	}
	runtime, err := loadTunnelRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	system := &publicAccessTestSystem{t: t, fake: newMemoryTailscale(&tailscaleServeConfig{}), core: true, cloudflare: mode != "none", startup: mode != "none"}
	hooks := defaultPublicAccessHooks()
	hooks.findBinary = func(string) (string, error) { return filepath.Join(root, "tailscale.exe"), nil }
	hooks.client = func(string) tailscaleClient { return system.fake.client() }
	hooks.ensureCredentials = func(string) error { return nil }
	hooks.coreRunning = func(context.Context, string) (bool, error) { return system.core, nil }
	hooks.restartCore = func(context.Context, string) error { system.restarts++; system.core = true; return nil }
	hooks.stopCore = func(context.Context, string) error { system.coreStops++; system.core = false; return nil }
	hooks.localHealthy = func(context.Context, string) bool { return system.core }
	hooks.cloudflareRunning = func(string) (bool, error) { return system.cloudflare, nil }
	hooks.stopCloudflare = func(context.Context, tunnelRuntime) error { system.cloudflare = false; return nil }
	hooks.startCloudflare = func(context.Context, tunnelRuntime) error {
		system.cloudflareStarts++
		system.cloudflare = true
		return nil
	}
	hooks.startupEnabled = func(Manifest) (bool, error) { return system.startup, nil }
	hooks.setStartup = func(_ context.Context, _ string, enabled bool) error { system.startup = enabled; return nil }
	hooks.verifyOrigin = func(_ context.Context, runtime tunnelRuntime, origin string) error {
		system.verified++
		if system.cloudflare || system.startup {
			t.Error("Cloudflare remained active during Tailscale verification")
		}
		if actual, _ := readTrimmedText(runtime.files.serverURL); actual != origin {
			t.Error("Core origin not staged before verification")
		}
		state, err := loadTailscaleState(runtime.root)
		if err != nil || state == nil || !state.Pending || state.VerifiedAt != nil {
			t.Errorf("ready state published early: %+v %v", state, err)
		}
		return nil
	}
	hooks.configureCloudflare = func(ctx context.Context, request TunnelConfigureRequest) error {
		rt, err := loadTunnelRuntime(request.RuntimeRoot)
		if err != nil {
			return err
		}
		origin := ""
		if request.Mode != "none" {
			origin = "https://new.example.test"
		}
		if err := writeRuntimeText(rt.files.mode, request.Mode); err != nil {
			return err
		}
		if err := writeRuntimeText(rt.files.serverURL, origin); err != nil {
			return err
		}
		if err := rt.updateManifest(request.Mode, origin); err != nil {
			return err
		}
		system.cloudflare, system.startup = request.Mode != "none", request.Mode != "none"
		return system.hooks.restartCore(ctx, rt.root)
	}
	system.hooks = hooks
	return runtime, system
}

func (system *publicAccessTestSystem) configure(runtime tunnelRuntime, adopt bool) error {
	return configureTailscaleAccess(context.Background(), runtime, filepath.Join(runtime.root, "tailscale.exe"), adopt, system.hooks)
}

func requireRestoredPublicFiles(t *testing.T, snapshots []fileSnapshot) {
	t.Helper()
	for _, before := range snapshots {
		after, err := snapshotFile(before.path)
		if err != nil {
			t.Fatal(err)
		}
		if before.exists != after.exists || string(before.data) != string(after.data) {
			t.Errorf("not restored: %s", filepath.Base(before.path))
		}
	}
}

func TestTailscaleRuntimeConfigureStopRestartAndPortChange(t *testing.T) {
	runtime, system := newPublicAccessTestSystem(t, "named")
	if err := system.configure(runtime, true); err != nil {
		t.Fatal(err)
	}
	runtime, err := loadTunnelRuntime(runtime.root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.mode != "funnel" || runtime.manifest.TunnelMode != "none" || runtime.manifest.PublicURL != "" {
		t.Fatalf("unsafe projection: %+v", runtime.manifest)
	}
	if runtime.manifest.EffectivePublicAccess().URL != "https://device.example-tailnet.ts.net" || system.cloudflare || system.startup || system.cloudflareStarts != 0 {
		t.Fatal("incorrect provider state")
	}
	if old, _ := readTrimmedText(runtime.files.namedServerURL); old != "https://old.example.test" {
		t.Fatal("named origin lost")
	}
	state, err := loadTailscaleState(runtime.root)
	if err != nil || state == nil || state.Pending || !state.Enabled || state.VerifiedAt == nil {
		t.Fatalf("not ready: %+v %v", state, err)
	}
	if err := system.configure(runtime, false); err != nil {
		t.Fatal(err)
	}
	if system.restarts != 1 || system.fake.writeCount != 1 {
		t.Fatal("ready start was not idempotent")
	}
	if err := stopTailscaleAccess(context.Background(), runtime, runtime.manifest.TailscaleBinary, system.hooks); err != nil {
		t.Fatal(err)
	}
	state, err = loadTailscaleState(runtime.root)
	if err != nil || state.Enabled {
		t.Fatal("stop not persisted")
	}
	if len(system.fake.config.Web) != 0 {
		t.Fatal("stop left root mapped")
	}
	if err := system.configure(runtime, false); err != nil {
		t.Fatal(err)
	}
	runtime.settings.Port = 8766
	if err := system.configure(runtime, false); err != nil {
		t.Fatal(err)
	}
	state, err = loadTailscaleState(runtime.root)
	if err != nil || state.LocalOrigin != "http://127.0.0.1:8766" || !system.fake.config.handler(system.fake.node.DNSName+":443", "/").isProxy(state.LocalOrigin) {
		t.Fatal("port target not synchronized")
	}
}

func TestTailscaleRuntimeRollbackMatrix(t *testing.T) {
	for _, stage := range []string{"credentials", "pending_state", "cloudflare_stop", "startup", "cli", "partial_cli", "core", "public", "ready_state", "manifest"} {
		t.Run(stage, func(t *testing.T) {
			runtime, system := newPublicAccessTestSystem(t, "named")
			before, err := capturePublicAccessSnapshot(context.Background(), runtime, system.hooks)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected " + stage)
			switch stage {
			case "credentials":
				system.hooks.ensureCredentials = func(string) error { return failure }
			case "pending_state", "ready_state":
				calls := 0
				system.hooks.saveState = func(root string, state *tailscaleFunnelState) error {
					calls++
					if stage == "pending_state" && calls == 1 || stage == "ready_state" && calls == 2 {
						return failure
					}
					return saveTailscaleState(root, state)
				}
			case "cloudflare_stop":
				calls := 0
				original := system.hooks.stopCloudflare
				system.hooks.stopCloudflare = func(ctx context.Context, rt tunnelRuntime) error {
					calls++
					if calls == 1 {
						return failure
					}
					return original(ctx, rt)
				}
			case "startup":
				original := system.hooks.setStartup
				system.hooks.setStartup = func(ctx context.Context, root string, enabled bool) error {
					if !enabled {
						return failure
					}
					return original(ctx, root, enabled)
				}
			case "cli", "partial_cli":
				system.fake.failWrite = 1
				system.fake.partialWrite = stage == "partial_cli"
			case "core":
				calls := 0
				original := system.hooks.restartCore
				system.hooks.restartCore = func(ctx context.Context, root string) error {
					calls++
					if calls == 1 {
						return failure
					}
					return original(ctx, root)
				}
			case "public":
				system.hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error { return failure }
			case "manifest":
				system.hooks.saveManifest = func(tunnelRuntime, string, string) error { return failure }
			}
			if err := system.configure(runtime, true); err == nil {
				t.Fatal("expected failure")
			}
			requireRestoredPublicFiles(t, before.files)
			if !system.core || !system.cloudflare || !system.startup || len(system.fake.config.Web) != 0 {
				t.Fatalf("system not restored: %+v", system)
			}
		})
	}
}

func TestTailscaleRuntimeDoesNotLeaveNewCoreRunningAfterFailure(t *testing.T) {
	runtime, system := newPublicAccessTestSystem(t, "none")
	system.core = false
	system.hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error { return errors.New("public failed") }
	if err := system.configure(runtime, true); err == nil {
		t.Fatal("expected failure")
	}
	if system.core || system.coreStops != 1 || system.cloudflare || system.startup {
		t.Fatal("failed configure changed initial stopped state")
	}
}

func TestTailscaleRuntimeModeSwitchRollback(t *testing.T) {
	runtime, system := newPublicAccessTestSystem(t, "none")
	if err := system.configure(runtime, true); err != nil {
		t.Fatal(err)
	}
	runtime, err := loadTunnelRuntime(runtime.root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := capturePublicAccessSnapshot(context.Background(), runtime, system.hooks)
	if err != nil {
		t.Fatal(err)
	}
	originalConfig := cloneTailscaleServe(system.fake.config)
	configure := system.hooks.configureCloudflare
	system.hooks.configureCloudflare = func(ctx context.Context, request TunnelConfigureRequest) error {
		if err := configure(ctx, request); err != nil {
			return err
		}
		return errors.New("new provider failed")
	}
	request := TunnelConfigureRequest{RuntimeRoot: runtime.root, Provider: "cloudflare", Mode: "named"}
	if err := leaveTailscaleAccess(context.Background(), runtime, runtime.manifest.TailscaleBinary, request, system.hooks); err == nil {
		t.Fatal("expected failure")
	}
	requireRestoredPublicFiles(t, before.files)
	if !sameTailscaleServe(originalConfig, system.fake.config) || system.cloudflare || system.startup {
		t.Fatal("previous Funnel was not restored")
	}
	system.hooks.configureCloudflare = configure
	request.Provider, request.Mode = "none", "none"
	if err := leaveTailscaleAccess(context.Background(), runtime, runtime.manifest.TailscaleBinary, request, system.hooks); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtime.root, tailscaleStateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ownership not cleared after successful switch")
	}
	manifest, err := Load(runtime.files.manifest)
	if err != nil || manifest.EffectivePublicAccess().Provider != "none" {
		t.Fatal("local mode not committed")
	}
	if len(system.fake.config.Web) != 0 {
		t.Fatal("local mode left a Funnel mapping")
	}
}

func TestTailscaleRuntimeConflictBeforeAnySideEffects(t *testing.T) {
	runtime, system := newPublicAccessTestSystem(t, "named")
	system.fake.config = testServeWithHandlers(false, map[string]*tailscaleHTTPHandler{"/private": {Proxy: "http://127.0.0.1:9000"}})
	before, err := capturePublicAccessSnapshot(context.Background(), runtime, system.hooks)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.configure(runtime, true); tailscaleDiagnosticCode(err) != "private_serve_conflict" {
		t.Fatal(err)
	}
	requireRestoredPublicFiles(t, before.files)
	if !system.cloudflare || !system.startup || system.restarts != 0 || system.fake.writeCount != 0 {
		t.Fatal("conflict caused side effects")
	}
}

func TestTailscaleRuntimePreservesRecoveryOwnershipAfterConcurrentChange(t *testing.T) {
	runtime, system := newPublicAccessTestSystem(t, "none")
	system.fake.afterWrite = func(fake *memoryTailscaleCLI) {
		fake.config.Web[fake.node.DNSName+":443"].Handlers["/"] = &tailscaleHTTPHandler{Proxy: "http://127.0.0.1:9000"}
	}
	if err := system.configure(runtime, true); err == nil {
		t.Fatal("expected conflict")
	}
	state, err := loadTailscaleState(runtime.root)
	if err != nil || state == nil || !state.Pending || state.VerifiedAt != nil {
		t.Fatal("recovery ownership lost")
	}
	if !system.fake.config.handler(system.fake.node.DNSName+":443", "/").isProxy("http://127.0.0.1:9000") {
		t.Fatal("foreign root was overwritten")
	}
}
