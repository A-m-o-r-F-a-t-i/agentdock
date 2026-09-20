//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"path/filepath"
)

func rollbackTailscaleConfigUpdate(ctx context.Context, root string, snapshots []fileSnapshot, coreWasRunning bool, cause error) error {
	current, cleanupErr := loadTunnelRuntime(root)
	if cleanupErr == nil {
		cleanupErr = stopTunnel(ctx, current)
	}
	toRestore := snapshots
	if cleanupErr != nil {
		// Preserve the current target in its ownership record when a partial
		// port change could not be cleaned up. Never replace it with a stale port.
		toRestore = nil
		for _, snapshot := range snapshots {
			if snapshot.path != filepath.Join(root, tailscaleStateFile) {
				toRestore = append(toRestore, snapshot)
			}
		}
		if state, err := loadTailscaleState(root); err == nil && state != nil {
			state.Pending, state.VerifiedAt = true, nil
			cleanupErr = errors.Join(cleanupErr, saveTailscaleState(root, state))
		}
	}
	restoreErr := restoreSnapshots(toRestore)
	oldRuntime, loadErr := loadTunnelRuntime(root)
	if loadErr == nil && restoreErr == nil {
		if cleanupErr == nil {
			restoreErr = restoreTailscaleMappingOnly(ctx, oldRuntime)
		}
		if coreWasRunning {
			restoreErr = errors.Join(restoreErr, platformServiceAction(ctx, root, "restart"))
		} else {
			restoreErr = errors.Join(restoreErr, platformServiceAction(ctx, root, "stop"))
		}
	}
	return errors.Join(cause, cleanupErr, restoreErr, loadErr)
}

// Rollback restores the previous mapping without starting a previously stopped
// Core or claiming that a fresh public HTTP verification has taken place.
func restoreTailscaleMappingOnly(ctx context.Context, runtime tunnelRuntime) error {
	state, err := loadTailscaleState(runtime.root)
	if err != nil {
		return err
	}
	if state == nil {
		return tailscaleProblem("ownership_required", "无法恢复缺少所有权记录的 Funnel 映射")
	}
	if !state.Enabled {
		return nil
	}
	binary, err := findTailscaleBinary(runtime.manifest.TailscaleBinary)
	if err != nil {
		return err
	}
	return withTailscaleMutationLock(ctx, binary, func() error {
		client := newWindowsTailscaleClient(binary)
		node, config, err := client.observe(ctx)
		if err != nil {
			return err
		}
		change, err := prepareTailscaleMapping(node, config, runtime.localOrigin(), state, false)
		if err != nil {
			return err
		}
		if err := change.apply(ctx, client); err != nil {
			rollbackErr := change.rollback(ctx, client)
			state.Pending, state.VerifiedAt = true, nil
			return errors.Join(err, rollbackErr, saveTailscaleState(runtime.root, state))
		}
		return nil
	})
}
