package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageConfigIgnoresUnrelatedStaleEnvironment(t *testing.T) {
	for name, value := range map[string]string{
		"AGENTDOCK_INSTRUCTIONS_FILE": filepath.Join(t.TempDir(), "missing", "AGENTS.md"),
		"AGENTDOCK_PORT":              "not-an-integer",
		"AGENTDOCK_ACP_ENABLED":       "invalid",
		"AGENTDOCK_BROWSER_CDP_URL":   "invalid",
		"AGENTDOCK_DEFAULT_DIR":       "relative-old-workspace",
	} {
		t.Setenv(name, value)
	}
	home := filepath.Join(t.TempDir(), "fresh-home")
	cfg, err := StorageConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentDockHome != home || cfg.InstructionsFile != "" || cfg.AgentDockDefaultDir != "" {
		t.Fatalf("unexpected storage config: %#v", cfg)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatal(err)
	}
}

func TestStorageConfigStillRejectsInvalidStorage(t *testing.T) {
	if _, err := StorageConfig("relative"); err == nil {
		t.Fatal("accepted relative home")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := StorageConfig(file); err == nil {
		t.Fatal("accepted file as storage")
	}
}
