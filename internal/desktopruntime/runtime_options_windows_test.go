//go:build windows

package desktopruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopStartupIsolatesStaleInheritedRuntimeParameters(t *testing.T) {
	root := t.TempDir()
	for _, name := range append(append([]string{}, managedCoreEnvironment...), "AGENTDOCK_HOME", "AGENTDOCK_RUNTIME_ROOT") {
		t.Setenv(name, os.Getenv(name))
	}
	for key, value := range map[string]string{"AGENTDOCK_INSTRUCTIONS_FILE": filepath.Join(root, "removed", "AGENTS.md"), "AGENTDOCK_PORT": "invalid", "AGENTDOCK_STDIO": "true", "AGENTDOCK_BROWSER_ENABLED": "invalid", "AGENTDOCK_ACP_ENABLED": "invalid", "AGENTDOCK_DEFAULT_DIR": "relative-obsolete", "AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL": "broken"} {
		t.Setenv(key, value)
	}
	manifest := Manifest{SchemaVersion: SchemaVersion, InstallRoot: root, AgentDockBinary: filepath.Join(root, "agentdock.exe"), AgentDockHome: filepath.Join(root, "home"), AgentDockDefaultDir: filepath.Join(root, "workspace"), Host: "127.0.0.1", Port: 19879, LocalMCPURL: "http://127.0.0.1:19879/mcp", TunnelMode: "none", InstallChannel: "test"}
	if err := Save(filepath.Join(root, "runtime.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := platformPrepareCoreEnvironment(root); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("AGENTDOCK_INSTRUCTIONS_FILE") != "" || os.Getenv("AGENTDOCK_STDIO") != "" || os.Getenv("AGENTDOCK_DEFAULT_DIR") != manifest.AgentDockDefaultDir {
		t.Fatal("stale parameters remained effective")
	}
	options := defaultRuntimeOptions()
	options.DefaultDir = manifest.AgentDockDefaultDir
	options.InstructionsFile = filepath.Join(root, "missing-explicit.md")
	raw, _ := json.Marshal(controlPanelSettings{Port: 19879, LogLevel: "info", MCPAppsEnabled: true, RuntimeOptions: &options})
	if err := os.WriteFile(filepath.Join(root, "control-panel-settings.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := platformPrepareCoreEnvironment(root); err == nil {
		t.Fatal("explicit invalid instruction path was silently ignored")
	}
	view, err := platformReadRuntimeOptions(root)
	if err != nil || view.Options.InstructionsFile != options.InstructionsFile {
		t.Fatal("invalid saved option cannot be read for repair")
	}
}
