package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

const (
	systemSkillsMarker = ".agentdock-system-skills.marker"
	bundledSkillsLock  = "_bundled_skills"
)

// BundledSkills returns active Skill directories under skills/.system. The
// directory class is the source of truth, matching Codex's visible layout.
func (s *Store) BundledSkills() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, systemDirectory))
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read system skills: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if validateIdentifier("skill", entry.Name()) != nil {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) IsBundled(skill string) (bool, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return false, err
	}
	selection, err := s.load(skill)
	if err != nil {
		return false, err
	}
	if selection.ActiveVersion != "" && selection.System {
		return true, nil
	}
	path, err := s.SystemPath(skill)
	if err != nil {
		return false, err
	}
	return regularDirectoryExists(path)
}

// ReplaceBundledSkills reconciles the exact system-Skill set. Existing system
// Skills removed from a release become ordinary user-visible Skills instead of
// being deleted. The operation rolls back directory classification changes if
// any move or marker update fails.
func (s *Store) ReplaceBundledSkills(ctx context.Context, skills []string) error {
	desired, err := normalizeBundledSkills(skills)
	if err != nil {
		return err
	}
	release, err := s.acquire(ctx, bundledSkillsLock)
	if err != nil {
		return err
	}
	defer release()

	current, err := s.BundledSkills()
	if err != nil {
		return err
	}
	currentSet := make(map[string]struct{}, len(current))
	desiredSet := make(map[string]struct{}, len(desired))
	for _, name := range current {
		currentSet[name] = struct{}{}
	}
	for _, name := range desired {
		desiredSet[name] = struct{}{}
	}

	type classificationChange struct {
		name string
		from bool
	}
	changes := make([]classificationChange, 0)
	rollback := func(cause error) error {
		errs := []error{cause}
		for index := len(changes) - 1; index >= 0; index-- {
			change := changes[index]
			if revertErr := s.SetBundled(context.WithoutCancel(ctx), change.name, change.from); revertErr != nil {
				errs = append(errs, fmt.Errorf("restore bundled classification for %s: %w", change.name, revertErr))
			}
		}
		return errors.Join(errs...)
	}

	for _, name := range desired {
		if _, exists := currentSet[name]; exists {
			continue
		}
		if err := s.SetBundled(ctx, name, true); err != nil {
			return rollback(fmt.Errorf("classify bundled skill %s: %w", name, err))
		}
		changes = append(changes, classificationChange{name: name, from: false})
	}
	for _, name := range current {
		if _, exists := desiredSet[name]; exists {
			continue
		}
		if err := s.SetBundled(ctx, name, false); err != nil {
			return rollback(fmt.Errorf("unclassify removed bundled skill %s: %w", name, err))
		}
		changes = append(changes, classificationChange{name: name, from: true})
	}

	marker := []byte("managed by AgentDock\n")
	markerPath := filepath.Join(s.root, systemDirectory, systemSkillsMarker)
	if err := atomicfile.Write(markerPath, marker, 0o600); err != nil {
		return rollback(fmt.Errorf("write system Skill marker: %w", err))
	}
	return nil
}

func normalizeBundledSkills(skills []string) ([]string, error) {
	seen := make(map[string]struct{}, len(skills))
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		if err := validateIdentifier("skill", skill); err != nil {
			return nil, err
		}
		if _, exists := seen[skill]; exists {
			continue
		}
		seen[skill] = struct{}{}
		names = append(names, skill)
	}
	sort.Strings(names)
	return names, nil
}
