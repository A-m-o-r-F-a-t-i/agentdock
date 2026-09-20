package installer

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/desktopruntime"
)

func validateTailscaleInstallRequest(request Request, existing desktopruntime.Manifest) error {
	if request.TunnelMode != "" && request.TunnelMode != "none" || strings.TrimSpace(request.ServerURL) != "" || request.TunnelToken != "" || request.TunnelTokenFile != "" {
		return fmt.Errorf("安装期间保留 Tailscale Funnel；请在控制面板切换公网访问方式后再安装")
	}
	if request.Port != 0 && request.Port != existing.Port || request.Host != "" && request.Host != existing.Host {
		return fmt.Errorf("安装期间不能更改已配置 Funnel 的监听地址；请先通过控制面板修改端口")
	}
	return nil
}

func preserveTailscaleManifest(target *desktopruntime.Manifest, existing desktopruntime.Manifest) {
	target.PublicAccessProvider = existing.PublicAccessProvider
	target.PublicAccessMode = existing.PublicAccessMode
	target.PublicAccessURL = existing.PublicAccessURL
	target.TailscaleBinary = existing.TailscaleBinary
	target.TunnelMode, target.PublicURL = "none", ""
}

func snapshotWindowsPublicAccess(request Request, journal *rollbackJournal) error {
	if existing, err := desktopruntime.Load(filepath.Join(request.RuntimeRoot, "runtime.json")); err == nil && existing.EffectivePublicAccess().Provider == desktopruntime.PublicAccessProviderTailscale {
		if err := validateTailscaleInstallRequest(request, existing); err != nil {
			return err
		}
	}
	for _, name := range []string{"runtime.json", "server-url.txt", "tailscale-funnel-state.json", "control-panel-settings.json", "cloudflared-mode.txt"} {
		if err := journal.Snapshot(filepath.Join(request.RuntimeRoot, name)); err != nil {
			return err
		}
	}
	return nil
}
