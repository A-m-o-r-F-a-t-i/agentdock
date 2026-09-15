package installer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/plugin"
)

func legacyMigrationFixture(t *testing.T, home, command string) (string, []byte) {
	t.Helper()
	root := filepath.Join(home, "plugins", "demo")
	meta := filepath.Join(root, ".agentdock-plugin")
	if err := os.MkdirAll(meta, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"schema_version": 1, "name": "demo", "description": "Migration test", "version": "1.0.0", "mcpServers": map[string]any{
		"demo-server": map[string]any{"name": "demo-server", "description": "Test server", "transport": "stdio", "command": command, "enabled": true, "env_from_env": map[string]string{"API_TOKEN": "HOST_TEST_TOKEN"}},
	}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meta, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meta, "state.json"), []byte(`{"enabled":false,"skills":{"demo-skill":false},"mcpServers":{"demo-server":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(root, "skills", "demo-skill")
	if err := os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: demo-skill\ndescription: Test migration.\nversion: 1.0.0\n---\nBody stays unchanged.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, data
}

func TestLegacyPluginMigrationPreservesSwitchesSecretsAndRollback(t *testing.T) {
	home := t.TempDir()
	root, original := legacyMigrationFixture(t, home, "node")
	envPath := filepath.Join(home, "env", "mcp", "demo-server.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("API_TOKEN=synthetic-test-value\n")
	if err := os.WriteFile(envPath, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := plugin.PlanLegacyMigration(home)
	if err != nil {
		t.Fatal(err)
	}
	journal := newJournal(home, "migration-test")
	if err := plan.Apply(context.Background(), journal.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agentdock-plugin")); !os.IsNotExist(err) {
		t.Fatal("retired format survived migration")
	}
	store, err := plugin.New(home)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := store.Get("demo")
	if err != nil {
		t.Fatal(err)
	}
	if definition.Enabled || !definition.Heavy || len(definition.Skills) != 1 || len(definition.MCPServers) != 1 {
		t.Fatalf("migration changed plugin semantics: %#v", definition)
	}
	membership, _, err := store.SkillMembership("demo-skill")
	if err != nil || membership.Enabled {
		t.Fatal("disabled Skill state lost")
	}
	data, err := os.ReadFile(filepath.Join(root, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "env_from_env") || strings.Contains(string(data), "HOST_TEST_TOKEN") || strings.Contains(string(data), "enabled") {
		t.Fatal("host configuration leaked into portable package")
	}
	data, err = os.ReadFile(envPath)
	if err != nil || string(data) != string(sentinel) {
		t.Fatal("credential file changed")
	}
	if err := journal.Restore(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(root, ".agentdock-plugin", "plugin.json"))
	if err != nil || string(data) != string(original) {
		t.Fatal("rollback did not restore original manifest")
	}
	for _, name := range []string{"plugin.json", "mcp.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("rollback left generated package file")
		}
	}
	migrated, err := MigratePluginHome(context.Background(), home)
	if err != nil || len(migrated) != 1 {
		t.Fatalf("retry failed: %#v %v", migrated, err)
	}
	migrated, err = MigratePluginHome(context.Background(), home)
	if err != nil || len(migrated) != 0 {
		t.Fatalf("migration not idempotent: %#v %v", migrated, err)
	}
}

func TestLegacyMigrationFailureRestoresBeforeRetry(t *testing.T) {
	home := t.TempDir()
	root, original := legacyMigrationFixture(t, home, "../escape")
	if _, err := MigratePluginHome(context.Background(), home); err == nil {
		t.Fatal("migration accepted escaping executable")
	}
	data, err := os.ReadFile(filepath.Join(root, ".agentdock-plugin", "plugin.json"))
	if err != nil || string(data) != string(original) {
		t.Fatal("failed migration lost original manifest")
	}
	for _, path := range []string{filepath.Join(root, "plugin.json"), filepath.Join(root, "mcp.json"), filepath.Join(home, "plugins", ".state", "demo.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed migration left %s", path)
		}
	}
	legacyMigrationFixture(t, home, "node")
	if _, err := MigratePluginHome(context.Background(), home); err != nil {
		t.Fatal(err)
	}
}
