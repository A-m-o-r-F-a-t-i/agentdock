//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"path/filepath"
	goruntime "runtime"
	"time"
)

func platformVerifyTailscale(ctx context.Context, root string) (TunnelStatus, error) {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return verifyConfiguredTailscale(probe, root, defaultPublicAccessHooks())
}

// Verification never resets a mapping or restarts Core. Network waiting holds
// no operation lock, so a mode change can finish while DNS is propagating.
func verifyConfiguredTailscale(ctx context.Context, root string, hooks publicAccessHooks) (TunnelStatus, error) {
	started := time.Now()
	absRoot, pathErr := filepath.Abs(root)
	if pathErr != nil {
		return TunnelStatus{}, pathErr
	}
	root = absRoot
	// Serialize verification attempts, not configuration. No file is created;
	// the separate mutex lets stop/mode changes finish during public HTTP.
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	releaseProbe, err := acquireTunnelOperation(ctx, filepath.Join(root, ".tailscale-verification"))
	if err != nil {
		return TunnelStatus{}, err
	}
	defer releaseProbe()
	runtime, err := loadTunnelRuntime(root)
	if err != nil {
		return TunnelStatus{}, err
	}
	status := TunnelStatus{Provider: PublicAccessProviderTailscale, Mode: "funnel", Phase: "CheckingLocal", LocalOrigin: runtime.localOrigin()}
	finish := func(failure error) (TunnelStatus, error) {
		elapsed := time.Since(started).Milliseconds()
		status.PublicProbeMS = &elapsed
		if failure != nil {
			var transient *tailscaleTransientProbe
			if errors.As(failure, &transient) || errors.Is(failure, context.DeadlineExceeded) {
				failure = tailscaleProblem("public_unreachable", "公网 DNS/TLS/HTTP 尚未就绪，本地配置保留，可继续使用软件并重试验证。")
			}
			status = tailscaleStatusError(status, failure)
		}
		return status, nil
	}
	if runtime.mode != "funnel" {
		return finish(tailscaleProblem("mode_changed", "当前不是 Funnel 模式，已停止旧验证"))
	}
	state, err := loadTailscaleState(root)
	if err != nil {
		return finish(err)
	}
	if state == nil || !state.Enabled {
		return finish(tailscaleProblem("disabled", "Funnel 未启用，未执行公网验证"))
	}
	binary, err := hooks.findBinary(runtime.manifest.TailscaleBinary)
	if err != nil {
		return finish(err)
	}
	withCurrent := func(operation func(*tailscaleFunnelState) error) error {
		return withTailscaleVerificationCommit(ctx, root, binary, func() error {
			currentRuntime, err := loadTunnelRuntime(root)
			if err != nil {
				return err
			}
			current, err := loadTailscaleState(root)
			if err != nil {
				return err
			}
			if current == nil || !current.Enabled || currentRuntime.mode != "funnel" || !current.ConfiguredAt.Equal(state.ConfiguredAt) || current.DeviceID != state.DeviceID || current.PublicOrigin != state.PublicOrigin || current.LocalOrigin != state.LocalOrigin || currentRuntime.localOrigin() != state.LocalOrigin || currentRuntime.manifest.EffectivePublicAccess().URL != state.PublicOrigin {
				return tailscaleProblem("mode_changed", "配置已变化，旧公网验证结果已丢弃")
			}
			return operation(current)
		})
	}
	// Persist uncertainty before any blocking query: cancellation, timeout and
	// a killed verifier must never leave yesterday's Ready as today's evidence.
	if err := withCurrent(func(current *tailscaleFunnelState) error {
		current.Pending, current.VerifiedAt = true, nil
		return hooks.saveState(root, current)
	}); err != nil {
		return finish(err)
	}
	client := hooks.client(binary)
	node, config, err := client.observe(ctx)
	if err != nil {
		return finish(err)
	}
	change, err := prepareTailscaleMapping(node, config, runtime.localOrigin(), state, false)
	if err != nil {
		return finish(err)
	}
	if len(change.mutations) != 0 {
		return finish(tailscaleProblem("mapping_missing", "本地映射未完成，公网验证不会修改它"))
	}
	origin, err := readTrimmedText(runtime.files.serverURL)
	if err != nil {
		return finish(err)
	}
	if origin != state.PublicOrigin || runtime.manifest.EffectivePublicAccess().URL != state.PublicOrigin || !hooks.localHealthy(ctx, runtime.localOrigin()+"/healthz") {
		return finish(tailscaleProblem("origin_mismatch", "本地服务或 Origin 已变化，未发送公网凭据"))
	}
	status.Installed, status.Configured, status.Running, status.FunnelEnabled, status.LocalReady = true, true, true, true, true
	status.PublicURL, status.DNSName, status.DeviceName, status.BackendState = state.PublicOrigin, node.DNSName, node.HostName, node.BackendState
	status.Phase = "VerifyingPublic"
	probeErr := hooks.verifyOrigin(ctx, runtime, state.PublicOrigin)
	if ctx.Err() != nil {
		return finish(errors.Join(probeErr, ctx.Err()))
	}
	// Use the same root-before-client lock order as configuration. Re-read all
	// identities before persisting so stale probes cannot resurrect a stopped or
	// replaced configuration.
	commitErr := withCurrent(func(current *tailscaleFunnelState) error {
		if probeErr != nil {
			return nil // The begin record already says pending.
		}
		freshNode, freshConfig, err := client.observe(ctx)
		if err != nil {
			return err
		}
		check, err := prepareTailscaleMapping(freshNode, freshConfig, current.LocalOrigin, current, false)
		if err != nil {
			return err
		}
		if len(check.mutations) != 0 {
			return tailscaleProblem("mapping_changed", "公网验证期间本地映射发生变化，未报告就绪")
		}
		now := time.Now().UTC()
		current.Pending, current.VerifiedAt = false, &now
		if err := hooks.saveState(root, current); err != nil {
			return err
		}
		status.Ready, status.Phase, status.VerifiedAt = true, "Ready", &now
		return nil
	})
	return finish(errors.Join(probeErr, commitErr))
}

func withTailscaleVerificationCommit(ctx context.Context, root, binary string, operation func() error) error {
	// acquireTunnelOperation uses a thread-owned Windows mutex.
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	release, err := acquireTunnelOperation(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	return withTailscaleMutationLock(ctx, binary, operation)
}
