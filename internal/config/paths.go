package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/fs/securepath"
)

// SkillStateDir returns AgentDock's Codex-style document Skill root.
// Active user Skills live directly under skills/<name>; AgentDock-only state
// and version archives use hidden children of this directory.
func SkillStateDir(cfg Config) (string, error) {
	return filepath.Join(cfg.AgentDockHome, "skills"), nil
}

// StorageConfig resolves only the storage root. Install/bootstrap commands do
// not start the server and must not validate inherited browser, ACP, listener,
// or instruction-file settings from a previous installation.
func StorageConfig(home string) (Config, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("resolve AgentDock home: %w", err)
		}
		if userHome == "" {
			return Config{}, fmt.Errorf("user home is empty")
		}
		home = filepath.Join(userHome, ".agentdock")
	}
	home = filepath.Clean(home)
	if !filepath.IsAbs(home) {
		return Config{}, fmt.Errorf("AgentDockHome must be absolute: %s", home)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Config{}, fmt.Errorf("create AgentDockHome: %w", err)
	}
	if err := securepath.EnsurePrivate(home); err != nil {
		return Config{}, fmt.Errorf("secure AgentDockHome: %w", err)
	}
	return Config{AgentDockHome: home}, nil
}
