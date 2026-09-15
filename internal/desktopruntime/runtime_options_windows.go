//go:build windows

package desktopruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

func effectiveRuntimeOptions(manifest Manifest, settings controlPanelSettings) RuntimeOptions {
	options := defaultRuntimeOptions()
	if settings.RuntimeOptions != nil {
		options = *settings.RuntimeOptions
	}
	if strings.TrimSpace(options.DefaultDir) == "" {
		options.DefaultDir = manifest.AgentDockDefaultDir
	}
	if strings.TrimSpace(options.DefaultDir) == "" && settings.RuntimeOptions == nil {
		// Preserve a usable legacy workspace on upgrade. Invalid inherited paths
		// and implicit instruction files are never adopted by a fresh desktop host.
		legacy := strings.TrimSpace(os.Getenv("AGENTDOCK_DEFAULT_DIR"))
		if filepath.IsAbs(legacy) {
			if info, err := os.Stat(legacy); err == nil && info.IsDir() {
				options.DefaultDir = legacy
			}
		}
	}
	if strings.TrimSpace(options.DefaultDir) == "" {
		if home, err := os.UserHomeDir(); err == nil {
			options.DefaultDir = filepath.Join(home, "AgentDock")
		}
	}
	return options
}

func platformReadRuntimeOptions(runtimeRoot string) (RuntimeOptionsView, error) {
	manifest, root, err := loadDesktopManifest(runtimeRoot)
	if err != nil {
		return RuntimeOptionsView{}, err
	}
	settings, err := loadControlPanelSettings(root, manifest.Port)
	if err != nil {
		return RuntimeOptionsView{}, err
	}
	home := manifest.AgentDockHome
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".agentdock")
		}
	}
	// Return saved values even if an explicitly configured file was later
	// removed. The configuration page must remain usable to repair that setting.
	return RuntimeOptionsView{Options: effectiveRuntimeOptions(manifest, settings), AgentDockHome: home,
		SettingsPath: filepath.Join(root, "control-panel-settings.json"), ManifestPath: filepath.Join(root, "runtime.json")}, nil
}

func platformSaveRuntimeOptions(ctx context.Context, root string, options RuntimeOptions) error {
	runtime, err := loadTunnelRuntime(root)
	if err != nil {
		return err
	}
	settings := runtime.settings
	request := ConfigUpdateRequest{
		RuntimeRoot: runtime.root, Port: settings.Port, LogLevel: settings.LogLevel,
		OAuthAccessTokenTTL: settings.OAuthAccessTokenTTL, MCPAppsEnabled: settings.MCPAppsEnabled,
		BrowserEnabled: settings.BrowserEnabled, BrowserCDPURL: settings.BrowserCDPURL,
		BrowserReuseExistingCDP: settings.BrowserReuseExistingCDP, ACPEnabled: settings.ACPEnabled,
		ACPProfiles: settings.ACPProfiles, ACPDefaultProfile: settings.ACPDefaultProfile, RuntimeOptions: &options,
	}
	if err := validateConfigUpdate(request); err != nil {
		return err
	}
	return platformUpdateConfig(ctx, request)
}
