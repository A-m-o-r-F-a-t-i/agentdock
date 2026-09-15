package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentDockHomeMigrationUsesDirectPlugins(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repository, "scripts", "migrate", "migrate-agentdock-home.ps1")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{".agentdock-plugin", "plugin.json", "state.json", "source_fingerprint", "plugins/cache"} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration script missing %q", required)
		}
	}
	if runtime.GOOS != "windows" {
		return
	}

	root := t.TempDir()
	source := filepath.Join(root, "old")
	destination := filepath.Join(root, "new")
	writeTestSkill(t, filepath.Join(source, "skill-store", "installed", "demo", "1.0.0"), "demo", "1.0.0")
	writeTestSkill(t, filepath.Join(source, "skill-store", "installed", "agentdock-user-guide", "1.1.0"), "agentdock-user-guide", "1.1.0")
	writeJSONFile(t, filepath.Join(source, "skill-store", "state", "demo.json"), map[string]any{"active_version": "1.0.0", "history": []string{}})
	writeJSONFile(t, filepath.Join(source, "skill-store", "state", "agentdock-user-guide.json"), map[string]any{"active_version": "1.1.0", "history": []string{}})
	writeJSONFile(t, filepath.Join(source, "skill-store", "bundled-skills.json"), map[string]any{"skills": []string{"agentdock-user-guide"}})

	implementation := filepath.Join(root, "implementation")
	if err := os.MkdirAll(implementation, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(implementation, "server.mjs"), []byte("console.log('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(source, "mcp", "servers.json"), map[string]any{
		"version": 1,
		"servers": []map[string]any{{
			"name": "demo-mcp", "description": "Demo MCP", "transport": "stdio",
			"command": "node.exe", "args": []string{"server.mjs"}, "cwd": implementation,
			"enabled": false, "timeout_ms": 1000,
		}},
	})
	plan := filepath.Join(root, "plan.json")
	writeJSONFile(t, plan, map[string]any{
		"schema_version": 1,
		"plugins": []map[string]any{{
			"name": "demo-plugin", "description": "Demo plugin.", "version": "1.0.0", "enabled": true,
			"skills": []string{"demo"},
			"mcp_servers": []map[string]any{{
				"name": "demo-mcp", "implementation_source": implementation,
				"implementation_target": "mcp/demo-mcp", "cwd": "mcp/demo-mcp",
				"command": "node.exe", "args": []string{"server.mjs"},
			}},
		}},
		"archive_mcp_servers": []string{},
	})

	command := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script,
		"-SourceHome", source, "-DestinationHome", destination, "-RepositoryRoot", repository,
		"-PluginPlanPath", plan)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("migration failed: %v\n%s", err, output)
	}
	for _, path := range []string{
		filepath.Join(destination, "plugins", "demo-plugin", ".agentdock-plugin", "plugin.json"),
		filepath.Join(destination, "plugins", "demo-plugin", "skills", "demo", "SKILL.md"),
		filepath.Join(destination, "skills", ".system", "agentdock-user-guide", "SKILL.md"),
		filepath.Join(destination, "migration-report.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected migrated path %s: %v", path, err)
		}
	}
	for _, forbidden := range []string{
		filepath.Join(destination, "plugins", "cache"),
		filepath.Join(destination, "plugins", "plugins.json"),
		filepath.Join(destination, "skill-store"),
	} {
		if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
			t.Fatalf("forbidden path exists: %s", forbidden)
		}
	}
	var manifest struct {
		MCPServers map[string]struct {
			Enabled bool `json:"enabled"`
		} `json:"mcpServers"`
	}
	readJSONFile(t, filepath.Join(destination, "plugins", "demo-plugin", ".agentdock-plugin", "plugin.json"), &manifest)
	if !manifest.MCPServers["demo-mcp"].Enabled {
		t.Fatal("manifest MCP capability must remain intrinsically enabled")
	}
	var state struct {
		MCPServers map[string]bool `json:"mcpServers"`
	}
	readJSONFile(t, filepath.Join(destination, "plugins", "demo-plugin", ".agentdock-plugin", "state.json"), &state)
	if state.MCPServers["demo-mcp"] {
		t.Fatal("migration did not preserve the disabled member state")
	}
}

func writeTestSkill(t *testing.T, root, name, version string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: Test Skill.\nversion: " + version + "\n---\n\n# Test\n"
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
