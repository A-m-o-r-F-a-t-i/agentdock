//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestFunnelLocalCommitRetainsPendingWithoutPublicWait(t *testing.T) {
	runtime, system := newPublicAccessTestSystem(t, "none")
	hooks := system.hooks
	hooks.waitForPublic = false
	probes := 0
	hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error {
		probes++
		return errors.New("unexpected public wait")
	}
	binary := filepath.Join(runtime.root, "tailscale.exe")
	if err := configureTailscaleAccess(t.Context(), runtime, binary, true, hooks); err != nil {
		t.Fatal(err)
	}
	state, err := loadTailscaleState(runtime.root)
	if err != nil || state == nil || !state.Pending || state.VerifiedAt != nil || probes != 0 {
		t.Fatalf("local commit made a public claim: %+v %v probes=%d", state, err, probes)
	}
	current, err := loadTunnelRuntime(runtime.root)
	if err != nil {
		t.Fatal(err)
	}
	beforeRestart := system.restarts
	beforeConfig := cloneTailscaleServe(system.fake.config)
	if err := configureTailscaleAccess(t.Context(), current, binary, true, hooks); err != nil {
		t.Fatal(err)
	}
	if probes != 0 || system.restarts != beforeRestart || !sameTailscaleServe(beforeConfig, system.fake.config) {
		t.Fatal("reusing pending configuration repeated writes or restarted Core")
	}
	hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error {
		return &tailscaleTransientProbe{message: "DNS not ready"}
	}
	status, err := verifyConfiguredTailscale(t.Context(), runtime.root, hooks)
	if err != nil || status.Ready || !status.LocalReady || status.Phase != "Degraded" {
		t.Fatalf("propagation was reported as ready or destructive failure: %+v %v", status, err)
	}
	if !sameTailscaleServe(beforeConfig, system.fake.config) || system.restarts != beforeRestart {
		t.Fatal("public verification changed mapping or restarted Core")
	}
	hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error { return nil }
	status, err = verifyConfiguredTailscale(t.Context(), runtime.root, hooks)
	if err != nil || !status.Ready || status.VerifiedAt == nil {
		t.Fatalf("verified configuration was not committed: %+v %v", status, err)
	}
}
