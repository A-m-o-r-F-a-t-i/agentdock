package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	registry "github.com/uvwt/agentdock/internal/plugin"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
)

func TestManageInstallsAndLoadsDirectPluginMembers(t *testing.T) {
	home := t.TempDir()
	store, err := registry.New(home)
	if err != nil {
		t.Fatal(err)
	}
	source := writeToolPluginPackage(t, t.TempDir(), "pcb", []string{"layout"}, []string{"easyeda"})
	service := testPluginService(store, false)

	result, err := service.Manage(context.Background(), ManageRequest{Action: "install", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := result["plugin"].(registry.Definition)
	if !ok || stored.Name != "pcb" || !stored.Enabled || stored.Path != filepath.Join(home, "plugins", "pcb") {
		t.Fatalf("stored plugin = %#v", result["plugin"])
	}
	if _, err := os.Stat(filepath.Join(stored.Path, registry.ManifestDirectory, registry.ManifestFilename)); err != nil {
		t.Fatalf("direct manifest missing: %v", err)
	}

	loaded, err := service.Load(context.Background(), LoadRequest{Name: "pcb"})
	if err != nil {
		t.Fatal(err)
	}
	skills, ok := loaded["skills"].([]SkillItem)
	if !ok || len(skills) != 1 || skills[0].File != "skill://layout/SKILL.md" {
		t.Fatalf("loaded skills = %#v", loaded["skills"])
	}
	servers, ok := loaded["mcp_servers"].([]MCPItem)
	if !ok || len(servers) != 1 || servers[0].Name != "easyeda" || len(servers[0].Tools) != 1 || servers[0].Tools[0].QualifiedName != "easyeda:route" {
		t.Fatalf("loaded MCP servers = %#v", loaded["mcp_servers"])
	}
	unavailable, ok := loaded["unavailable_members"].([]map[string]any)
	if !ok || len(unavailable) != 0 {
		t.Fatalf("unavailable members = %#v", loaded["unavailable_members"])
	}
}

func TestLoadReportsDisabledDirectPluginMembers(t *testing.T) {
	store, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := writeToolPluginPackage(t, t.TempDir(), "domain", []string{"workflow"}, []string{"remote"})
	service := testPluginService(store, false)
	if _, err := service.Manage(context.Background(), ManageRequest{Action: "install", Source: source}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Manage(context.Background(), ManageRequest{Action: "member_disable", Name: "domain", MemberType: "skill", Member: "workflow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Manage(context.Background(), ManageRequest{Action: "member_disable", Name: "domain", MemberType: "mcp_server", Member: "remote"}); err != nil {
		t.Fatal(err)
	}

	loaded, err := service.Load(context.Background(), LoadRequest{Name: "domain"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(loaded["skills"].([]SkillItem)); got != 0 {
		t.Fatalf("enabled skills = %d, want 0", got)
	}
	if got := len(loaded["mcp_servers"].([]MCPItem)); got != 0 {
		t.Fatalf("enabled MCP servers = %d, want 0", got)
	}
	unavailable := loaded["unavailable_members"].([]map[string]any)
	if len(unavailable) != 2 || unavailable[0]["reason"] != "disabled" || unavailable[1]["reason"] != "disabled" {
		t.Fatalf("unavailable = %#v", unavailable)
	}
}

func TestLoadKeepsSkillsWhenPluginMCPToolDiscoveryFails(t *testing.T) {
	store, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := writeToolPluginPackage(t, t.TempDir(), "domain", []string{"workflow"}, []string{"remote"})
	service := testPluginService(store, true)
	if _, err := service.Manage(context.Background(), ManageRequest{Action: "install", Source: source}); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Load(context.Background(), LoadRequest{Name: "domain"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(loaded["skills"].([]SkillItem)); got != 1 {
		t.Fatalf("loaded Skills = %d, want 1", got)
	}
	servers := loaded["mcp_servers"].([]MCPItem)
	if len(servers) != 1 || servers[0].ToolLoadError == "" {
		t.Fatalf("loaded MCP servers = %#v", servers)
	}
	unavailable := loaded["unavailable_members"].([]map[string]any)
	if len(unavailable) != 1 || unavailable[0]["reason"] != "tool_discovery_failed" || unavailable[0]["code"] != "MCP_TIMEOUT" {
		t.Fatalf("unavailable members = %#v", unavailable)
	}
}

func TestDisabledDirectPluginCannotLoad(t *testing.T) {
	store, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := writeToolPluginPackage(t, t.TempDir(), "domain", []string{"workflow"}, nil)
	service := testPluginService(store, false)
	if _, err := service.Manage(context.Background(), ManageRequest{Action: "install", Source: source}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Manage(context.Background(), ManageRequest{Action: "disable", Name: "domain"}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Load(context.Background(), LoadRequest{Name: "domain"})
	var toolErr *toolcore.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "PLUGIN_DISABLED" {
		t.Fatalf("Load disabled plugin error = %#v", err)
	}
}

func TestManageValidatesAndUpdatesDirectPlugin(t *testing.T) {
	store, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := testPluginService(store, false)
	v1 := writeToolPluginPackageVersion(t, t.TempDir(), "domain", "1.0.0", []string{"workflow"}, nil)
	validated, err := service.Manage(context.Background(), ManageRequest{Action: "validate", Source: v1})
	if err != nil || validated["valid"] != true {
		t.Fatalf("validate result=%#v err=%v", validated, err)
	}
	if _, err := service.Manage(context.Background(), ManageRequest{Action: "install", Source: v1}); err != nil {
		t.Fatal(err)
	}
	v2 := writeToolPluginPackageVersion(t, t.TempDir(), "domain", "2.0.0", []string{"workflow", "review"}, nil)
	updated, err := service.Manage(context.Background(), ManageRequest{Action: "update", Name: "domain", Source: v2})
	if err != nil {
		t.Fatal(err)
	}
	definition := updated["plugin"].(registry.Definition)
	if definition.Version != "2.0.0" || len(definition.Skills) != 2 {
		t.Fatalf("updated plugin = %#v", definition)
	}
	other := writeToolPluginPackageVersion(t, t.TempDir(), "other", "2.0.0", []string{"other-workflow"}, nil)
	_, err = service.Manage(context.Background(), ManageRequest{Action: "update", Name: "domain", Source: other})
	var toolErr *toolcore.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "PLUGIN_UPDATE_NAME_MISMATCH" {
		t.Fatalf("mismatched update error = %#v", err)
	}
}

func testPluginService(store *registry.Store, discoveryFailure bool) *Service {
	return New(
		store,
		func(name string) (SkillItem, bool, error) {
			member, found, err := store.Skill(name)
			if err != nil || !found {
				return SkillItem{}, found, err
			}
			return SkillItem{
				Name: name, Description: "Plugin workflow", File: "skill://" + name + "/SKILL.md",
				Enabled: member.Enabled, Plugin: member.Plugin,
			}, true, nil
		},
		func(_ context.Context, name string, expand bool) (MCPItem, bool, error) {
			membership, found, err := store.MCPMembership(name)
			if err != nil || !found {
				return MCPItem{}, found, err
			}
			item := MCPItem{Name: name, Description: "Plugin MCP", Plugin: membership.Plugin, Status: "ready", ToolCount: 1, Enabled: membership.Enabled}
			if expand && membership.Enabled {
				if discoveryFailure {
					item.Status = "error"
					item.LastErrorCode = "MCP_TIMEOUT"
					item.ToolLoadError = "tools/list timed out"
				} else {
					item.Tools = []MCPToolItem{{Name: "route", QualifiedName: name + ":route", Description: "Route PCB traces", Server: name}}
				}
			}
			return item, true, nil
		},
	)
}

func writeToolPluginPackage(t *testing.T, parent, name string, skills, servers []string) string {
	t.Helper()
	return writeToolPluginPackageVersion(t, parent, name, "1.0.0", skills, servers)
}

func writeToolPluginPackageVersion(t *testing.T, parent, name, version string, skillNames, serverNames []string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(root, registry.ManifestDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, skillName := range skillNames {
		dir := filepath.Join(root, "skills", skillName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		doc := fmt.Sprintf("---\nname: %s\ndescription: Plugin Skill %s.\nversion: 1.0.0\n---\n\n# %s\n", skillName, skillName, skillName)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mcpServers := map[string]registry.MCPServer{}
	for _, serverName := range serverNames {
		mcpServers[serverName] = registry.MCPServer{Type: "stdio", Command: "test-server"}
	}
	mcpData, err := json.Marshal(registry.MCPConfig{Schema: registry.MCPSchema, MCPServers: mcpServers})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, registry.MCPFilename), mcpData, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := registry.Manifest{Schema: registry.ManifestSchema, Name: name, Description: "Plugin " + name + ".", Version: version,
		Extensions: map[string]any{registry.ExtensionNamespace: map[string]any{"heavy": true}}}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, registry.ManifestDirectory, registry.ManifestFilename), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
