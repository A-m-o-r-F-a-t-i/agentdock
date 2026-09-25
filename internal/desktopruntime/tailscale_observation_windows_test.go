//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestFunnelTimeoutDoesNotResurrectReady(t *testing.T) {
	// Every write stays inside the existing t.TempDir fixture. The CLI is the
	// repository's in-memory fake; only this private loopback health server runs.
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"service":"agentdock","process_id":1,"version":"1.1.6"}`)
	}))
	defer health.Close()
	parsed, err := url.Parse(health.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	rt, system := newPublicAccessTestSystem(t, "none")
	rt.manifest.Port = port
	rt.manifest.LocalMCPURL = health.URL + "/mcp"
	if err := Save(filepath.Join(rt.root, "runtime.json"), rt.manifest); err != nil {
		t.Fatal(err)
	}
	rt, err = loadTunnelRuntime(rt.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.configure(rt, true); err != nil {
		t.Fatal(err)
	}
	rt, err = loadTunnelRuntime(rt.root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := loadTailscaleState(rt.root)
	if err != nil || before == nil || before.Pending || before.VerifiedAt == nil {
		t.Fatalf("fixture not verified: %+v %v", before, err)
	}
	restarts := system.restarts
	mapping := cloneTailscaleServe(system.fake.config)
	hooks := system.hooks
	probes := 0
	hooks.verifyOrigin = func(ctx context.Context, _ tunnelRuntime, _ string) error {
		probes++
		<-ctx.Done()
		return ctx.Err()
	}
	deadline, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	failed, err := verifyConfiguredTailscale(deadline, rt.root, hooks)
	cancel()
	if err != nil || probes != 1 || failed.Ready || failed.DiagnosticCode != "public_unreachable" {
		t.Fatalf("timeout fixture failed: %+v err=%v probes=%d", failed, err, probes)
	}
	after, err := loadTailscaleState(rt.root)
	if err != nil {
		t.Fatal(err)
	}
	next := inspectTailscaleRuntime(t.Context(), rt, filepath.Join(rt.root, "tailscale.exe"), system.fake.client())
	if system.restarts != restarts || !sameTailscaleServe(mapping, system.fake.config) {
		t.Fatal("audit unexpectedly changed lifecycle or mapping")
	}
	t.Logf("timeout: ready=%t code=%s; persisted: pending=%t verified_at=%v; next status: ready=%t phase=%s code=%s", failed.Ready, failed.DiagnosticCode, after.Pending, after.VerifiedAt, next.Ready, next.Phase, next.DiagnosticCode)
	if next.Ready || !after.Pending || after.VerifiedAt != nil {
		t.Fatal("timeout resurrected previous readiness")
	}
	hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error { return nil }
	recovered, err := verifyConfiguredTailscale(t.Context(), rt.root, hooks)
	if err != nil || !recovered.Ready {
		t.Fatalf("successful reverification did not recover: %+v %v", recovered, err)
	}
}

func TestFunnelCancelledProbeKeepsPending(t *testing.T) {
	rt, system := newPublicAccessTestSystem(t, "none")
	if err := system.configure(rt, true); err != nil {
		t.Fatal(err)
	}
	hooks := system.hooks
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error {
		state, err := loadTailscaleState(rt.root)
		if err != nil || !state.Pending || state.VerifiedAt != nil {
			t.Fatalf("probe began without durable pending: %+v %v", state, err)
		}
		cancel()
		return context.Canceled
	}
	status, err := verifyConfiguredTailscale(ctx, rt.root, hooks)
	state, readErr := loadTailscaleState(rt.root)
	if err != nil || readErr != nil || status.Ready || !state.Pending || state.VerifiedAt != nil {
		t.Fatalf("cancel retained readiness: %+v %+v %v %v", status, state, err, readErr)
	}
}

func TestFunnelVerifierDoesNotBlockModeChangeOrCommitOldSuccess(t *testing.T) {
	rt, system := newPublicAccessTestSystem(t, "none")
	if err := system.configure(rt, true); err != nil {
		t.Fatal(err)
	}
	hooks := system.hooks
	hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error {
		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done <- withTailscaleVerificationCommit(ctx, rt.root, filepath.Join(rt.root, "tailscale.exe"), func() error {
				state, err := loadTailscaleState(rt.root)
				if err != nil {
					return err
				}
				state.Enabled = false
				return saveTailscaleState(rt.root, state)
			})
		}()
		if err := <-done; err != nil {
			t.Fatalf("verification blocked configuration: %v", err)
		}
		return nil
	}
	status, err := verifyConfiguredTailscale(t.Context(), rt.root, hooks)
	state, readErr := loadTailscaleState(rt.root)
	if err != nil || readErr != nil || status.Ready || state.Enabled || state.VerifiedAt != nil || status.DiagnosticCode != "mode_changed" {
		t.Fatalf("old success changed stopped configuration: %+v %+v %v %v", status, state, err, readErr)
	}
}

func TestFunnelVerificationPendingWriteFailurePreventsProbe(t *testing.T) {
	rt, system := newPublicAccessTestSystem(t, "none")
	if err := system.configure(rt, true); err != nil {
		t.Fatal(err)
	}
	hooks := system.hooks
	hooks.saveState = func(string, *tailscaleFunnelState) error { return errors.New("fixture storage failure") }
	probes := 0
	hooks.verifyOrigin = func(context.Context, tunnelRuntime, string) error { probes++; return nil }
	status, _ := verifyConfiguredTailscale(t.Context(), rt.root, hooks)
	if probes != 0 || status.Ready {
		t.Fatalf("probe ran without recording uncertainty: %d %+v", probes, status)
	}
}

func TestFunnelVerificationSerializesConcurrentAttempts(t *testing.T) {
	rt, system := newPublicAccessTestSystem(t, "none")
	if err := system.configure(rt, true); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	first := system.hooks
	first.verifyOrigin = func(context.Context, tunnelRuntime, string) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() { _, err := verifyConfiguredTailscale(t.Context(), rt.root, first); done <- err }()
	<-entered
	second := system.hooks
	second.verifyOrigin = func(context.Context, tunnelRuntime, string) error {
		t.Error("concurrent verifier entered HTTP")
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, err := verifyConfiguredTailscale(ctx, rt.root, second)
	cancel()
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second verifier did not honor bounded gate: %v", err)
	}
	// A genuinely newer failed attempt invalidates the first success.
	second.verifyOrigin = func(context.Context, tunnelRuntime, string) error {
		return &tailscaleTransientProbe{message: "fixture unreachable"}
	}
	status, err := verifyConfiguredTailscale(t.Context(), rt.root, second)
	state, readErr := loadTailscaleState(rt.root)
	if err != nil || readErr != nil || status.Ready || !state.Pending || state.VerifiedAt != nil {
		t.Fatalf("latest failure lost: %+v %+v %v", status, state, err)
	}
}
