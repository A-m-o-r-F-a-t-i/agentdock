package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/securepath"
)

const (
	systemDirectory             = ".system"
	versionsDirectory           = ".versions"
	stateDirectory              = ".state"
	locksDirectory              = ".locks"
	cacheDirectory              = ".cache"
	tempDirectory               = ".tmp"
	lockOwnerPrefix             = "owner-"
	lockRetryInterval           = 25 * time.Millisecond
	transientLockErrorRetryTime = 500 * time.Millisecond
)

type Selection struct {
	ActiveVersion string    `json:"active_version,omitempty"`
	History       []string  `json:"history,omitempty"`
	Disabled      bool      `json:"disabled,omitempty"`
	System        bool      `json:"system,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Store manages the Codex-style Skill tree rooted at ~/.agentdock/skills.
// Active user Skills live directly under <root>/<name>, active bundled Skills
// under <root>/.system/<name>, and inactive versions under .versions.
type Store struct{ root string }

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("skill root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve skill root: %w", err)
	}
	s := &Store{root: abs}
	if err := s.EnsureLayout(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) EnsureLayout() error {
	for _, path := range []string{
		s.root,
		filepath.Join(s.root, systemDirectory),
		filepath.Join(s.root, versionsDirectory),
		filepath.Join(s.root, stateDirectory),
		filepath.Join(s.root, locksDirectory),
		filepath.Join(s.root, cacheDirectory),
		filepath.Join(s.root, tempDirectory),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create skill directory %q: %w", path, err)
		}
		if err := securepath.EnsurePrivate(path); err != nil {
			return fmt.Errorf("secure skill directory %q: %w", path, err)
		}
	}
	return nil
}

func (s *Store) UserPath(skill string) (string, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return "", err
	}
	return filepath.Join(s.root, skill), nil
}

func (s *Store) SystemPath(skill string) (string, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return "", err
	}
	return filepath.Join(s.root, systemDirectory, skill), nil
}

func (s *Store) archivePath(skill, version string) (string, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return "", err
	}
	if err := validateIdentifier("version", version); err != nil {
		return "", err
	}
	return filepath.Join(s.root, versionsDirectory, skill, version), nil
}

func (s *Store) activePath(skill string, system bool) (string, error) {
	if system {
		return s.SystemPath(skill)
	}
	return s.UserPath(skill)
}

// InstalledPath returns the physical directory for a particular installed
// version. The active version resolves to its Codex-style visible directory;
// inactive versions resolve to the hidden version archive.
func (s *Store) InstalledPath(skill, version string) (string, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return "", err
	}
	if err := validateIdentifier("version", version); err != nil {
		return "", err
	}
	selection, err := s.load(skill)
	if err != nil {
		return "", err
	}
	if selection.ActiveVersion == version {
		return s.activePath(skill, selection.System)
	}
	return s.archivePath(skill, version)
}

func (s *Store) CachePath(name string) (string, error) {
	if err := validateIdentifier("cache name", name); err != nil {
		return "", err
	}
	return filepath.Join(s.root, cacheDirectory, name), nil
}

func (s *Store) TempPath(prefix string) (string, error) {
	if err := validateIdentifier("temporary prefix", prefix); err != nil {
		return "", err
	}
	return os.MkdirTemp(filepath.Join(s.root, tempDirectory), prefix+"-")
}

func (s *Store) IsInstalled(skill, version string) (bool, error) {
	path, err := s.InstalledPath(skill, version)
	if err != nil {
		return false, err
	}
	return regularDirectoryExists(path)
}

func (s *Store) ListVersions(skill string) ([]string, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	archiveRoot := filepath.Join(s.root, versionsDirectory, skill)
	entries, err := os.ReadDir(archiveRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || validateIdentifier("version", entry.Name()) != nil {
			continue
		}
		seen[entry.Name()] = struct{}{}
	}
	selection, err := s.load(skill)
	if err != nil {
		return nil, err
	}
	if selection.ActiveVersion != "" {
		activePath, pathErr := s.activePath(skill, selection.System)
		if pathErr != nil {
			return nil, pathErr
		}
		exists, statErr := regularDirectoryExists(activePath)
		if statErr != nil {
			return nil, statErr
		}
		if exists {
			seen[selection.ActiveVersion] = struct{}{}
		}
	}
	versions := make([]string, 0, len(seen))
	for version := range seen {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions, nil
}

func (s *Store) ListSkills() ([]string, error) {
	seen := map[string]struct{}{}
	if err := collectSkillDirectories(s.root, true, seen); err != nil {
		return nil, err
	}
	if err := collectSkillDirectories(filepath.Join(s.root, systemDirectory), false, seen); err != nil {
		return nil, err
	}
	if err := collectSkillDirectories(filepath.Join(s.root, versionsDirectory), false, seen); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func collectSkillDirectories(root string, skipHidden bool, out map[string]struct{}) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if skipHidden && strings.HasPrefix(name, ".") {
			continue
		}
		if validateIdentifier("skill", name) != nil {
			continue
		}
		out[name] = struct{}{}
	}
	return nil
}

func (s *Store) ActiveVersion(skill string) (string, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return "", err
	}
	selection, err := s.load(skill)
	if err != nil {
		return "", err
	}
	return selection.ActiveVersion, nil
}

func (s *Store) Snapshot(skill string) (Selection, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return Selection{}, err
	}
	return s.load(skill)
}

// RestoreSelection restores both selection metadata and the visible active
// directory. It is used by install/bootstrap transactions, not normal users.
func (s *Store) RestoreSelection(ctx context.Context, skill string, target Selection) error {
	if err := validateIdentifier("skill", skill); err != nil {
		return err
	}
	if err := validateSelection(target); err != nil {
		return err
	}
	release, err := s.acquire(ctx, skill)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.load(skill)
	if err != nil {
		return err
	}
	return s.applySelectionLocked(skill, current, target)
}

func (s *Store) Resolve(skill, version string) (string, error) {
	if version != "" {
		installed, err := s.IsInstalled(skill, version)
		if err != nil {
			return "", err
		}
		if !installed {
			return "", fmt.Errorf("skill %s version %s is not installed", skill, version)
		}
		return s.InstalledPath(skill, version)
	}
	selection, err := s.Snapshot(skill)
	if err != nil {
		return "", err
	}
	if selection.ActiveVersion == "" {
		return "", fmt.Errorf("skill %s has no active version", skill)
	}
	path, err := s.activePath(skill, selection.System)
	if err != nil {
		return "", err
	}
	exists, err := regularDirectoryExists(path)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("skill %s active directory is missing", skill)
	}
	return path, nil
}

func (s *Store) Activate(ctx context.Context, skill, version string) error {
	return s.activate(ctx, skill, version, nil)
}

// ActivateBundled activates a release-bundled Skill under skills/.system.
func (s *Store) ActivateBundled(ctx context.Context, skill, version string) error {
	system := true
	return s.activate(ctx, skill, version, &system)
}

func (s *Store) activate(ctx context.Context, skill, version string, systemOverride *bool) error {
	if err := validateIdentifier("skill", skill); err != nil {
		return err
	}
	if err := validateIdentifier("version", version); err != nil {
		return err
	}
	release, err := s.acquire(ctx, skill)
	if err != nil {
		return err
	}
	defer release()

	current, err := s.load(skill)
	if err != nil {
		return err
	}
	system := current.System
	if systemOverride != nil {
		system = *systemOverride
	}
	target := current
	if current.ActiveVersion != "" && current.ActiveVersion != version {
		target.History = prependUnique(target.History, current.ActiveVersion, 20)
	}
	target.ActiveVersion = version
	target.System = system
	target.UpdatedAt = time.Now().UTC()
	return s.applySelectionLocked(skill, current, target)
}

// SetBundled changes only the active directory class. The package and version
// remain unchanged while moving between skills/<name> and skills/.system/<name>.
func (s *Store) SetBundled(ctx context.Context, skill string, bundled bool) error {
	if err := validateIdentifier("skill", skill); err != nil {
		return err
	}
	release, err := s.acquire(ctx, skill)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.load(skill)
	if err != nil {
		return err
	}
	if current.ActiveVersion == "" {
		return fmt.Errorf("skill %s has no active version", skill)
	}
	if current.System == bundled {
		return nil
	}
	target := current
	target.System = bundled
	target.UpdatedAt = time.Now().UTC()
	return s.applySelectionLocked(skill, current, target)
}

func (s *Store) applySelectionLocked(skill string, current, target Selection) error {
	if err := validateSelection(target); err != nil {
		return err
	}

	if current.ActiveVersion == target.ActiveVersion && target.ActiveVersion != "" {
		currentPath, err := s.activePath(skill, current.System)
		if err != nil {
			return err
		}
		exists, err := regularDirectoryExists(currentPath)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("skill %s active version %s is not installed", skill, current.ActiveVersion)
		}
		targetPath, err := s.activePath(skill, target.System)
		if err != nil {
			return err
		}
		moved := false
		if !samePath(currentPath, targetPath) {
			if exists, err := pathExists(targetPath); err != nil {
				return err
			} else if exists {
				return fmt.Errorf("skill %s target active directory already exists", skill)
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
				return err
			}
			if err := os.Rename(currentPath, targetPath); err != nil {
				return fmt.Errorf("move active skill directory: %w", err)
			}
			moved = true
		}
		if err := s.persistSelection(skill, target); err != nil {
			if moved {
				return errors.Join(err, renameRollback(targetPath, currentPath))
			}
			return err
		}
		return nil
	}

	var currentPath, currentArchive string
	if current.ActiveVersion != "" {
		var err error
		currentPath, err = s.activePath(skill, current.System)
		if err != nil {
			return err
		}
		exists, err := regularDirectoryExists(currentPath)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("skill %s active version %s is not installed", skill, current.ActiveVersion)
		}
		currentArchive, err = s.archivePath(skill, current.ActiveVersion)
		if err != nil {
			return err
		}
		if exists, err := pathExists(currentArchive); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("skill %s archived version %s already exists", skill, current.ActiveVersion)
		}
	}

	var targetArchive, targetPath string
	if target.ActiveVersion != "" {
		var err error
		targetArchive, err = s.archivePath(skill, target.ActiveVersion)
		if err != nil {
			return err
		}
		exists, err := regularDirectoryExists(targetArchive)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("skill %s version %s is not installed", skill, target.ActiveVersion)
		}
		targetPath, err = s.activePath(skill, target.System)
		if err != nil {
			return err
		}
		if !samePath(targetPath, currentPath) {
			if exists, err := pathExists(targetPath); err != nil {
				return err
			} else if exists {
				return fmt.Errorf("skill %s target active directory already exists", skill)
			}
		}
	}

	currentMoved := false
	if current.ActiveVersion != "" {
		if err := os.MkdirAll(filepath.Dir(currentArchive), 0o700); err != nil {
			return err
		}
		if err := os.Rename(currentPath, currentArchive); err != nil {
			return fmt.Errorf("archive current skill version: %w", err)
		}
		currentMoved = true
	}

	targetMoved := false
	if target.ActiveVersion != "" {
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			if currentMoved {
				return errors.Join(err, renameRollback(currentArchive, currentPath))
			}
			return err
		}
		if err := os.Rename(targetArchive, targetPath); err != nil {
			if currentMoved {
				return errors.Join(fmt.Errorf("activate skill version: %w", err), renameRollback(currentArchive, currentPath))
			}
			return fmt.Errorf("activate skill version: %w", err)
		}
		targetMoved = true
	}

	if err := s.persistSelection(skill, target); err != nil {
		var rollbackErrors []error
		if targetMoved {
			rollbackErrors = append(rollbackErrors, renameRollback(targetPath, targetArchive))
		}
		if currentMoved {
			rollbackErrors = append(rollbackErrors, renameRollback(currentArchive, currentPath))
		}
		return errors.Join(append([]error{err}, rollbackErrors...)...)
	}
	return nil
}

func validateSelection(selection Selection) error {
	if selection.ActiveVersion != "" {
		if err := validateIdentifier("version", selection.ActiveVersion); err != nil {
			return err
		}
	}
	for _, version := range selection.History {
		if err := validateIdentifier("version", version); err != nil {
			return err
		}
	}
	return nil
}

func renameRollback(source, destination string) error {
	if source == "" || destination == "" {
		return nil
	}
	if err := os.Rename(source, destination); err != nil {
		return fmt.Errorf("rollback directory move %q to %q: %w", source, destination, err)
	}
	return nil
}

func regularDirectoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("skill path %q is not a regular directory", path)
	}
	return true, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// SetEnabled persists the Skill's base availability without changing its active
// version. Plugin-level availability is an independent runtime overlay.
func (s *Store) SetEnabled(ctx context.Context, skill string, enabled bool) (Selection, error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return Selection{}, err
	}
	release, err := s.acquire(ctx, skill)
	if err != nil {
		return Selection{}, err
	}
	defer release()

	versions, err := s.ListVersions(skill)
	if err != nil {
		return Selection{}, err
	}
	if len(versions) == 0 {
		return Selection{}, fmt.Errorf("skill %s is not installed", skill)
	}
	selection, err := s.load(skill)
	if err != nil {
		return Selection{}, err
	}
	if selection.Disabled == !enabled {
		return selection, nil
	}
	selection.Disabled = !enabled
	selection.UpdatedAt = time.Now().UTC()
	if err := s.save(skill, selection); err != nil {
		return Selection{}, err
	}
	return selection, nil
}

func (s *Store) Enabled(skill string) (bool, error) {
	selection, err := s.Snapshot(skill)
	if err != nil {
		return false, err
	}
	return !selection.Disabled, nil
}

func (s *Store) PreviousVersion(skill string) (string, error) {
	selection, err := s.Snapshot(skill)
	if err != nil {
		return "", err
	}
	for _, version := range selection.History {
		installed, err := s.IsInstalled(skill, version)
		if err != nil {
			return "", err
		}
		if installed {
			return version, nil
		}
	}
	return "", fmt.Errorf("skill %s has no rollback version", skill)
}

func (s *Store) RemoveVersion(ctx context.Context, skill, version string) error {
	if err := validateIdentifier("skill", skill); err != nil {
		return err
	}
	if err := validateIdentifier("version", version); err != nil {
		return err
	}
	release, err := s.acquire(ctx, skill)
	if err != nil {
		return err
	}
	defer release()
	selection, err := s.load(skill)
	if err != nil {
		return err
	}
	if selection.ActiveVersion == version {
		return fmt.Errorf("cannot remove active skill version %s", version)
	}
	path, err := s.archivePath(skill, version)
	if err != nil {
		return err
	}
	exists, err := regularDirectoryExists(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("skill %s version %s is not installed", skill, version)
	}
	return os.RemoveAll(path)
}

func (s *Store) load(skill string) (Selection, error) {
	path := filepath.Join(s.root, stateDirectory, skill+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Selection{}, nil
	}
	if err != nil {
		return Selection{}, fmt.Errorf("read skill state: %w", err)
	}
	var selection Selection
	if err := json.Unmarshal(data, &selection); err != nil {
		return Selection{}, fmt.Errorf("decode skill state: %w", err)
	}
	return selection, nil
}

func (s *Store) persistSelection(skill string, selection Selection) error {
	if selection.ActiveVersion == "" && len(selection.History) == 0 && !selection.Disabled && !selection.System && selection.UpdatedAt.IsZero() {
		path := filepath.Join(s.root, stateDirectory, skill+".json")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove skill state: %w", err)
		}
		return nil
	}
	return s.save(skill, selection)
}

func (s *Store) save(skill string, selection Selection) error {
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return fmt.Errorf("encode skill state: %w", err)
	}
	path := filepath.Join(s.root, stateDirectory, skill+".json")
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return fmt.Errorf("replace skill state: %w", err)
	}
	return nil
}

func (s *Store) acquire(ctx context.Context, skill string) (func(), error) {
	return s.acquireWrite(ctx, skill)
}

func newLockOwner() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func cleanupOwnedLockInitialization(lockPath, ownerPath string) error {
	var cleanupErrs []error
	if err := os.Remove(ownerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove incomplete skill lock owner: %w", err))
	}
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove incomplete skill lock directory: %w", err))
	}
	return errors.Join(cleanupErrs...)
}

func releaseOwnedLock(lockPath, owner string) {
	ownerPath := filepath.Join(lockPath, lockOwnerPrefix+owner)
	if err := os.Remove(ownerPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("remove skill lock owner failed", "path", ownerPath, "error", err)
		}
		return
	}
	if err := os.Remove(lockPath); err != nil &&
		!errors.Is(err, os.ErrNotExist) &&
		!isDirectoryBusy(err) {
		slog.Warn("release skill lock failed", "path", lockPath, "error", err)
	}
}

func removeStaleOwnedLock(lockPath string) bool {
	entries, err := os.ReadDir(lockPath)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	if len(entries) == 0 {
		err := os.Remove(lockPath)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return true
		}
		if !isDirectoryBusy(err) {
			slog.Warn("remove empty stale skill lock failed", "path", lockPath, "error", err)
		}
		return false
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), lockOwnerPrefix) {
		return false
	}
	ownerPath := filepath.Join(lockPath, entries[0].Name())
	if err := os.Remove(ownerPath); err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	if err := os.Remove(lockPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		if !isDirectoryBusy(err) {
			slog.Warn("remove stale skill lock failed", "path", lockPath, "error", err)
		}
		return false
	}
	return true
}

func validateIdentifier(label, value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("invalid %s %q", label, value)
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._+-", r) {
			continue
		}
		return fmt.Errorf("invalid %s %q", label, value)
	}
	return nil
}

func prependUnique(values []string, value string, max int) []string {
	out := []string{value}
	for _, existing := range values {
		if existing != value {
			out = append(out, existing)
		}
		if len(out) == max {
			break
		}
	}
	return out
}
