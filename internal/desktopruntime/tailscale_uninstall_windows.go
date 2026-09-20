//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
)

// CleanupTailscaleFunnel removes only this runtime's still-matching mappings.
// Changed ownership is reported as a warning; unknown state fails the uninstall
// before its recovery record or the installed client can be removed.
func CleanupTailscaleFunnel(ctx context.Context, root string) (bool, string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return true, "", err
	}
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	release, err := acquireTunnelOperation(ctx, root)
	if err != nil {
		return true, "", err
	}
	defer release()
	return cleanupTailscaleFunnel(ctx, root, defaultPublicAccessHooks())
}

func cleanupTailscaleFunnel(ctx context.Context, root string, hooks publicAccessHooks) (bool, string, error) {
	state, err := loadTailscaleState(root)
	if err != nil {
		return true, "", err
	}
	manifest, manifestErr := Load(filepath.Join(root, "runtime.json"))
	usesTailscale := manifestErr == nil && manifest.EffectivePublicAccess().Provider == PublicAccessProviderTailscale
	if state == nil && !usesTailscale {
		return false, "", nil
	}
	if state == nil {
		return true, "缺少 AgentDock Funnel 所有权记录，未修改任何 Tailscale 配置", nil
	}
	if manifestErr != nil {
		return true, "", manifestErr
	}
	runtime, err := loadTunnelRuntime(root)
	if err != nil {
		return true, "", err
	}
	binary, err := hooks.findBinary(manifest.TailscaleBinary)
	if err != nil {
		return true, "", err
	}
	warning := ""
	err = withTailscaleMutationLock(ctx, binary, func() error {
		node, config, err := hooks.client(binary).observe(ctx)
		if err != nil {
			return err
		}
		_, err = prepareTailscaleStop(node, config, state)
		if err != nil {
			code := tailscaleDiagnosticCode(err)
			if code != "ownership_conflict" && code != "device_changed" && code != "mapping_conflict" {
				return err
			}
			warning = "当前 Tailscale 映射已由其他配置接管，卸载时保留这些映射：" + err.Error()
		} else if state.LocalOrigin != runtime.localOrigin() {
			warning = "Funnel 所有权记录与当前 AgentDock 端口不同，卸载时保留映射"
		} else if err := stopTailscaleAccess(ctx, runtime, binary, hooks); err != nil {
			// A conflict after a write may mean rollback is incomplete. Keep the
			// recovery record; only pre-write conflicts can become a warning.
			return err
		}
		if err := os.Remove(filepath.Join(root, tailscaleStateFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	return true, warning, err
}
