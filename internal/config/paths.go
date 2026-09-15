package config

import "path/filepath"

// SkillStateDir returns AgentDock's Codex-style document Skill root.
// Active user Skills live directly under skills/<name>; AgentDock-only state
// and version archives use hidden children of this directory.
func SkillStateDir(cfg Config) (string, error) {
	return filepath.Join(cfg.AgentDockHome, "skills"), nil
}
