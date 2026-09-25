//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"strings"
)

func platformSetTunnelAutostart(_ context.Context, runtimeRoot string, enabled bool) error {
	runtime, err := loadTunnelRuntime(runtimeRoot)
	if err != nil {
		return err
	}
	name := defaultString(runtime.manifest.CloudflaredStartupValueName, "AgentDockCloudflared")
	if runtime.manifest.UsesScheduledTask() || !enabled {
		return removeRunValue(name)
	}
	if runtime.mode == "funnel" {
		return errors.New("Tailscale Funnel 使用官方 Windows 服务和后台配置恢复，不创建 AgentDock Cloudflared 自启动项")
	}
	command, err := tunnelStartupCommand(runtime.manifest, runtime.root)
	if err != nil {
		return err
	}
	return setRunValue(name, command)
}

func tunnelAutostartEnabled(manifest Manifest) (bool, error) {
	if manifest.UsesScheduledTask() {
		state, err := nativeRuntimeTaskAction(context.Background(), manifest.InstallRoot, manifest, "status")
		return state.Enabled, err
	}
	return runValuePresent(defaultString(manifest.CloudflaredStartupValueName, "AgentDockCloudflared"))
}

func tunnelStartupCommand(manifest Manifest, runtimeRoot string) (string, error) {
	if strings.TrimSpace(manifest.TrayBinary) != "" {
		return quoteWindowsArgument(manifest.TrayBinary) + " --start-tunnel --runtime-root " + quoteWindowsArgument(runtimeRoot), nil
	}
	if strings.TrimSpace(manifest.AgentDockBinary) == "" {
		return "", errors.New("Windows runtime manifest 缺少 agentdock_binary")
	}
	return quoteWindowsArgument(manifest.AgentDockBinary) + " tunnel start --runtime-root " + quoteWindowsArgument(runtimeRoot), nil
}
