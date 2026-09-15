package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type UninstallResult struct {
	RemovedVersions []string
	ActiveVersion   string
}

// Uninstall shares the per-Skill lock with activation. Removing one inactive
// version prunes rollback history; removing the whole Skill removes the active
// Codex-style directory, archived versions, and AgentDock selection metadata.
func (s *Store) Uninstall(ctx context.Context, skill, version string) (UninstallResult, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return UninstallResult{}, err
	}
	if version != "" {
		if err := validateIdentifier("version", version); err != nil {
			return UninstallResult{}, err
		}
	}

	release, err := s.acquire(ctx, skill)
	if err != nil {
		return UninstallResult{}, err
	}
	defer release()

	selection, err := s.load(skill)
	if err != nil {
		return UninstallResult{}, err
	}
	if version == "" {
		return s.uninstallSkillLocked(skill, selection)
	}
	return s.uninstallVersionLocked(skill, version, selection)
}

func (s *Store) uninstallVersionLocked(skill, version string, selection Selection) (UninstallResult, error) {
	if selection.ActiveVersion == version {
		return UninstallResult{}, fmt.Errorf("cannot uninstall active skill version %s", version)
	}
	packagePath, err := s.archivePath(skill, version)
	if err != nil {
		return UninstallResult{}, err
	}
	exists, err := regularDirectoryExists(packagePath)
	if err != nil {
		return UninstallResult{}, err
	}
	if !exists {
		return UninstallResult{}, fmt.Errorf("skill %s version %s is not installed", skill, version)
	}

	tombstone, err := s.uninstallTombstone("version")
	if err != nil {
		return UninstallResult{}, err
	}
	if err := os.Rename(packagePath, tombstone); err != nil {
		return UninstallResult{}, fmt.Errorf("stage skill version removal: %w", err)
	}

	next := selection
	var historyChanged bool
	next.History, historyChanged = removeVersion(next.History, version)
	if historyChanged {
		next.UpdatedAt = time.Now().UTC()
		if err := s.persistSelection(skill, next); err != nil {
			return UninstallResult{}, errors.Join(err, renameRollback(tombstone, packagePath))
		}
	}

	cleanupUninstallTombstone(tombstone)
	_ = os.Remove(filepath.Dir(packagePath))
	return UninstallResult{RemovedVersions: []string{version}, ActiveVersion: selection.ActiveVersion}, nil
}

func (s *Store) uninstallSkillLocked(skill string, selection Selection) (UninstallResult, error) {
	versions, err := s.ListVersions(skill)
	if err != nil {
		return UninstallResult{}, err
	}
	if len(versions) == 0 {
		return UninstallResult{}, fmt.Errorf("skill %s is not installed", skill)
	}
	sort.Strings(versions)

	type stagedMove struct {
		source    string
		tombstone string
	}
	moves := make([]stagedMove, 0, 3)
	stage := func(source, kind string) error {
		exists, statErr := pathExists(source)
		if statErr != nil {
			return statErr
		}
		if !exists {
			return nil
		}
		tombstone, tombstoneErr := s.uninstallTombstone(kind)
		if tombstoneErr != nil {
			return tombstoneErr
		}
		if renameErr := os.Rename(source, tombstone); renameErr != nil {
			return renameErr
		}
		moves = append(moves, stagedMove{source: source, tombstone: tombstone})
		return nil
	}
	rollback := func(cause error) error {
		errs := []error{cause}
		for index := len(moves) - 1; index >= 0; index-- {
			errs = append(errs, renameRollback(moves[index].tombstone, moves[index].source))
		}
		return errors.Join(errs...)
	}

	statePath := filepath.Join(s.root, stateDirectory, skill+".json")
	if err := stage(statePath, "state"); err != nil {
		return UninstallResult{}, rollback(fmt.Errorf("stage skill state removal: %w", err))
	}
	if selection.ActiveVersion != "" {
		activePath, pathErr := s.activePath(skill, selection.System)
		if pathErr != nil {
			return UninstallResult{}, rollback(pathErr)
		}
		if err := stage(activePath, "active"); err != nil {
			return UninstallResult{}, rollback(fmt.Errorf("stage active skill removal: %w", err))
		}
	}
	archiveRoot := filepath.Join(s.root, versionsDirectory, skill)
	if err := stage(archiveRoot, "versions"); err != nil {
		return UninstallResult{}, rollback(fmt.Errorf("stage archived skill removal: %w", err))
	}

	for _, move := range moves {
		cleanupUninstallTombstone(move.tombstone)
	}
	return UninstallResult{RemovedVersions: versions, ActiveVersion: selection.ActiveVersion}, nil
}

func (s *Store) uninstallTombstone(kind string) (string, error) {
	owner, err := newLockOwner()
	if err != nil {
		return "", fmt.Errorf("create uninstall tombstone: %w", err)
	}
	return filepath.Join(s.root, tempDirectory, "uninstall-"+kind+"-"+owner), nil
}

func cleanupUninstallTombstone(path string) {
	if err := os.RemoveAll(path); err != nil {
		slog.Warn("cleanup Skill uninstall tombstone failed", "path", path, "error", err)
	}
}

func removeVersion(history []string, version string) ([]string, bool) {
	filtered := make([]string, 0, len(history))
	changed := false
	for _, item := range history {
		if item == version {
			changed = true
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, changed
}
