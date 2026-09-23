package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
)

func TestContextMetadataDoesNotPrepareUnselectedPluginData(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	seedContextPerfPlugins(t, home, []contextPerfPlugin{{Skills: 2, Servers: 1, Heavy: true}}, "http://127.0.0.1:9/mcp")
	cfg := config.Config{AgentDockHome: home, AgentDockDefaultDir: root}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := newUnrestrictedTestRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for i := 0; i < 3; i++ {
		if _, err := rt.Call(t.Context(), "agentdock_context", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(home, "plugins", ".data", "synthetic-00")
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata prepared unrelated data directory: %v", err)
	}
	members, err := rt.pluginStore.MCPServersContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	server := members["synthetic-00-mcp-00"].Config
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := rt.pluginStore.PrepareMCPData(ctx, server); err == nil {
		t.Fatal("preparation ignored cancellation")
	}
	out := server
	out.PluginData = t.TempDir()
	if err := rt.pluginStore.PrepareMCPData(t.Context(), out); err == nil {
		t.Fatal("external data path was accepted")
	}
	if err := rt.pluginStore.PrepareMCPData(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(data); err != nil || !info.IsDir() {
		t.Fatalf("selected plugin data was not prepared: %v", err)
	}
}
