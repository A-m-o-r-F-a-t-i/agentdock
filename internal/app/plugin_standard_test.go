package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/config"
)

func TestNormalPluginDirectDisclosureAndHeavySwitch(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{AgentDockHome: filepath.Join(root, "home"), AgentDockDefaultDir: root}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	server := newPluginTestMCPServer(t)
	defer server.Close()
	source := writeAppPluginPackage(t, root, server.URL)
	var manifest map[string]any
	data, err := os.ReadFile(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	delete(manifest, "extensions")
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "pcb-layout", "SKILL.md"), []byte("---\nname: pcb-layout\ndescription: |\n  Plan and verify PCB layout.\n---\n# Layout\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Call(context.Background(), "plugin_manage", map[string]any{"action": "install", "source": source}); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(cfg.AgentDockHome, "plugins", "pcb", "plugin.json")
	original, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	check := func(heavy bool) {
		t.Helper()
		result, err := rt.Call(context.Background(), "agentdock_context", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		var contextView capabilityContext
		if err := remarshal(result, &contextView); err != nil {
			t.Fatal(err)
		}
		if containsCapabilitySkill(contextView.Skills, "pcb-layout") == heavy || containsCapabilityMCP(contextView.DynamicMCP, "easyeda-test") == heavy {
			t.Fatalf("wrong Heavy disclosure: %t %#v", heavy, contextView)
		}
		if (len(contextView.Plugins) == 1) != heavy {
			t.Fatalf("wrong plugin summary: %#v", contextView.Plugins)
		}
		result, err = rt.Call(context.Background(), "mcp_tool_search", map[string]any{"query": "route", "limit": 10})
		if err != nil {
			t.Fatal(err)
		}
		count, _ := result["count"].(int)
		if (count > 0) == heavy {
			t.Fatalf("wrong generic search for Heavy=%t: %#v", heavy, result)
		}
	}
	check(false)
	skillList, err := rt.skills.RuntimeSkills()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(skillList)
	var list struct {
		Skills []map[string]any `json:"skills"`
	}
	if err := json.Unmarshal(encoded, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list.Skills {
		if item["skill"] == "pcb-layout" {
			found = true
		}
	}
	if !found {
		t.Fatalf("versionless portable Skill absent from management: %s", encoded)
	}
	for _, action := range []string{"heavy_enable", "heavy_disable"} {
		if _, err := rt.Call(context.Background(), "plugin_manage", map[string]any{"action": action, "name": "pcb"}); err != nil {
			t.Fatal(err)
		}
		check(action == "heavy_enable")
		current, err := os.ReadFile(installed)
		if err != nil {
			t.Fatal(err)
		}
		if string(original) != string(current) {
			t.Fatal("host Heavy toggle changed portable manifest")
		}
	}
}

func TestPortableFixtureStandardStdioMCPRoundTrip(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required by the portable MCP fixture")
	}
	root := t.TempDir()
	cfg := config.Config{AgentDockHome: filepath.Join(root, "home"), AgentDockDefaultDir: root}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	source, err := filepath.Abs(filepath.Join("..", "plugin", "testdata", "portable-demo"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := time.Now()
	failFixture := func(stage string, err error) {
		t.Helper()
		details, _ := json.Marshal(err)
		t.Fatalf("portable stdio %s failed after %s (node=%q, root=%q): %v; details=%s", stage, time.Since(started), node, root, err, details)
	}
	if _, err := rt.Call(ctx, "plugin_manage", map[string]any{"action": "install", "source": source}); err != nil {
		failFixture("install", err)
	}
	if _, err := rt.Call(ctx, "plugin_load", map[string]any{"name": "portable-demo"}); err != nil {
		failFixture("load", err)
	}
	result, err := rt.Call(ctx, "mcp_tool_call", map[string]any{"name": "portable-echo:echo", "arguments": map[string]any{"text": "portable MCP round trip"}})
	if err != nil {
		failFixture("echo", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "portable MCP round trip") {
		t.Fatalf("wrong standard MCP response: %s", data)
	}
}
