//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func configureCloudflareTunnel(ctx context.Context, request TunnelConfigureRequest) error {
	runtime, err := loadTunnelRuntime(request.RuntimeRoot)
	if err != nil {
		return err
	}
	namedServerURL := ""
	var providedToken string
	if request.Mode == "named" {
		candidate := strings.TrimSpace(request.ServerURL)
		if candidate == "" {
			candidate, err = readTrimmedText(runtime.files.namedServerURL)
			if err != nil {
				return err
			}
		}
		namedServerURL, err = normalizeHTTPSOrigin(candidate)
		if err != nil {
			return err
		}

		providedToken, err = readSecretFile(request.TokenFile)
		if err != nil {
			return err
		}
		storedToken := providedToken
		if storedToken == "" {
			storedToken, err = readProtectedText(runtime.files.token, tunnelTokenEntropy)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errors.New("固定域名模式需要 Cloudflare Tunnel Token")
			}
			return fmt.Errorf("读取 Cloudflare Tunnel Token 失败: %w", err)
		}
		if strings.TrimSpace(storedToken) == "" {
			return errors.New("固定域名模式需要 Cloudflare Tunnel Token")
		}
	}

	if request.Mode != "none" && request.Mode != "quick" && request.Mode != "named" {
		return fmt.Errorf("unsupported tunnel mode %q", request.Mode)
	}
	if runtime.manifest.UsesScheduledTask() {
		if _, err := nativeRuntimeTaskAction(ctx, runtime.root, runtime.manifest, "validate"); err != nil {
			return err
		}
	}
	paths := []string{runtime.files.manifest, runtime.files.mode, runtime.files.serverURL, runtime.files.namedServerURL, runtime.files.quickURL, runtime.files.token}
	for _, name := range []string{"auth-token.dpapi", "oauth-password.dpapi", "oauth-token-secret.dpapi", credentialOwnerSIDFile} {
		paths = append(paths, filepath.Join(runtime.root, name))
	}
	var snapshots []fileSnapshot
	for _, path := range paths {
		value, err := snapshotFile(path)
		if err != nil {
			return err
		}
		snapshots = append(snapshots, value)
	}
	// The caller owns the operation gate. Every mutation below is restored on failure.
	apply := func() error {
		if err := ensureDesktopCredentials(runtime.root); err != nil {
			return err
		}
		if err := preserveNamedServerURL(runtime); err != nil {
			return err
		}
		if providedToken != "" {
			if err := writeProtectedText(runtime.files.token, providedToken, tunnelTokenEntropy); err != nil {
				return err
			}
		}
		if !runtime.manifest.UsesScheduledTask() {
			if err := stopCloudflareTunnel(ctx, runtime); err != nil {
				return err
			}
		}
		switch request.Mode {
		case "none":
			if err := writeRuntimeText(runtime.files.mode, "none"); err != nil {
				return err
			}
			if err := clearActivePublicURL(runtime.files); err != nil {
				return err
			}
			if err := runtime.updateManifest("none", ""); err != nil {
				return err
			}
			if err := platformSetTunnelAutostart(ctx, runtime.root, false); err != nil {
				return err
			}
			return restartConfiguredCore(ctx, runtime)
		case "quick":
			if err := writeRuntimeText(runtime.files.mode, "quick"); err != nil {
				return err
			}
			if err := clearActivePublicURL(runtime.files); err != nil {
				return err
			}
			if err := runtime.updateManifest("quick", ""); err != nil {
				return err
			}
			if err := platformSetTunnelAutostart(ctx, runtime.root, true); err != nil {
				return err
			}
			if err := restartConfiguredCore(ctx, runtime); err != nil {
				return err
			}
			runtime.mode = "quick"
			if runtime.manifest.UsesScheduledTask() {
				return nil
			}
			return startTunnel(ctx, runtime)
		case "named":
			if err := writeRuntimeText(runtime.files.namedServerURL, namedServerURL); err != nil {
				return err
			}
			if err := writeRuntimeText(runtime.files.serverURL, namedServerURL); err != nil {
				return err
			}
			if err := writeRuntimeText(runtime.files.mode, "named"); err != nil {
				return err
			}
			if err := os.Remove(runtime.files.quickURL); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("删除 Quick Tunnel ready 文件失败: %w", err)
			}
			if err := runtime.updateManifest("named", namedServerURL); err != nil {
				return err
			}
			if err := platformSetTunnelAutostart(ctx, runtime.root, true); err != nil {
				return err
			}
			if err := restartConfiguredCore(ctx, runtime); err != nil {
				return err
			}
			runtime.mode = "named"
			if runtime.manifest.UsesScheduledTask() {
				return nil
			}
			return startTunnel(ctx, runtime)
		default:
			return fmt.Errorf("不支持的公网模式：%s", request.Mode)
		}
	}
	if err := apply(); err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		restoreErr := restoreSnapshots(snapshots)
		old, loadErr := loadTunnelRuntime(runtime.root)
		if restoreErr == nil && loadErr == nil {
			restoreErr = restartConfiguredCore(recovery, old)
		}
		return errors.Join(err, restoreErr, loadErr)
	}
	return nil

}
