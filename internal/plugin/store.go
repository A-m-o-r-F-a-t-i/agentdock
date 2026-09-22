package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
	"github.com/uvwt/agentdock/internal/fs/securepath"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

const (
	maxManifestBytes = 1 << 20
	maxStateBytes    = 1 << 20
	maxPluginFiles   = 20000
	maxPluginBytes   = int64(512 << 20)
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

type packageRecord struct {
	root       string
	manifest   Manifest
	state      State
	definition Definition
	skillPaths map[string]string
	mcpConfigs map[string]mcpclient.ServerConfig
}

type Store struct {
	root     string
	lockPath string
	tempRoot string
}

func New(agentDockHome string) (*Store, error) {
	if strings.TrimSpace(agentDockHome) == "" {
		return nil, newError("PLUGIN_STORE_INVALID", "AgentDock home is required for the plugin store", nil, nil)
	}
	root := filepath.Join(agentDockHome, "plugins")
	store := &Store{
		root:     root,
		lockPath: filepath.Join(root, ".locks", "store.lock"),
		tempRoot: filepath.Join(root, ".tmp"),
	}
	if err := store.EnsureLayout(); err != nil {
		return nil, err
	}
	if _, err := store.scan(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Root() string { return s.root }
func (s *Store) Path() string { return s.root }

func (s *Store) EnsureLayout() error {
	for _, path := range []string{s.root, filepath.Dir(s.lockPath), s.tempRoot, filepath.Join(s.root, ".state"), filepath.Join(s.root, ".config"), filepath.Join(s.root, ".data")} {
		if _, err := containedPath(s.root, path, true); err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return newError("PLUGIN_STORE_WRITE_FAILED", "create plugin store directory", map[string]any{"path": path}, err)
		}
		if err := securepath.EnsurePrivate(path); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) List() ([]Definition, error) {
	packages, err := s.scan()
	if err != nil {
		return nil, err
	}
	names := sortedPackageNames(packages)
	items := make([]Definition, 0, len(names))
	for _, name := range names {
		items = append(items, cloneDefinition(packages[name].definition))
	}
	return items, nil
}

func (s *Store) Get(name string) (Definition, error) {
	record, err := s.getRecord(name)
	if err != nil {
		return Definition{}, err
	}
	return cloneDefinition(record.definition), nil
}

func (s *Store) Validate(source string) (Definition, error) {
	root, cleanup, err := s.prepareSource(source)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return Definition{}, err
	}
	record, err := readPackage(root, false)
	if err != nil {
		return Definition{}, err
	}
	return cloneDefinition(record.definition), nil
}

// Install copies one complete standard plugin package into plugins/<name>. The
// destination directory is the runtime source of truth; no cache or central
// member registry is created. replace=true updates an existing plugin while
// preserving compatible enable/disable state.
func (s *Store) Install(source string, replace bool) (Definition, error) {
	root, cleanup, err := s.prepareSource(source)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return Definition{}, err
	}
	sourceRecord, err := readPackage(root, false)
	if err != nil {
		return Definition{}, err
	}

	release, err := s.acquire()
	if err != nil {
		return Definition{}, err
	}
	defer release()
	installed, err := s.scanUnlocked()
	if err != nil {
		return Definition{}, err
	}
	if _, ok := installed[sourceRecord.manifest.Name]; !ok && replace {
		return Definition{}, newError("PLUGIN_NOT_FOUND", "plugin update requires an existing installed plugin", map[string]any{"name": sourceRecord.manifest.Name}, nil)
	}
	if existing, ok := installed[sourceRecord.manifest.Name]; ok && !replace {
		return Definition{}, newError("PLUGIN_ALREADY_INSTALLED", "plugin is already installed", map[string]any{"name": existing.manifest.Name, "version": existing.manifest.Version}, nil)
	}
	if err := ensureUniqueOwnership(installed, sourceRecord, sourceRecord.manifest.Name); err != nil {
		return Definition{}, err
	}

	work, err := os.MkdirTemp(s.tempRoot, "install-")
	if err != nil {
		return Definition{}, newError("PLUGIN_INSTALL_FAILED", "create plugin staging directory", nil, err)
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			_ = os.RemoveAll(work)
		}
	}()
	staged := filepath.Join(work, sourceRecord.manifest.Name)
	if err := copyPackageTree(root, staged); err != nil {
		return Definition{}, err
	}

	state := defaultState(sourceRecord)
	destination := filepath.Join(s.root, sourceRecord.manifest.Name)
	var oldRecord packageRecord
	var hadOld bool
	if existing, ok := installed[sourceRecord.manifest.Name]; ok {
		oldRecord, hadOld = existing, true
		state = mergeState(existing.state, sourceRecord)
	}
	if _, err := readPackage(staged, false); err != nil {
		return Definition{}, err
	}

	backup := ""
	if hadOld {
		backup = filepath.Join(work, sourceRecord.manifest.Name+".previous")
		if err := os.Rename(destination, backup); err != nil {
			return Definition{}, newError("PLUGIN_INSTALL_FAILED", "stage existing plugin for replacement", map[string]any{"name": sourceRecord.manifest.Name}, err)
		}
	}
	if err := os.Rename(staged, destination); err != nil {
		if hadOld {
			if restoreErr := os.Rename(backup, destination); restoreErr != nil {
				keepBackup = true
				return Definition{}, newError("PLUGIN_RECOVERY_REQUIRED", "restore previous plugin directory failed; backup retained", map[string]any{"backup": backup}, errors.Join(err, restoreErr))
			}
		}
		return Definition{}, newError("PLUGIN_INSTALL_FAILED", "activate installed plugin directory", map[string]any{"name": sourceRecord.manifest.Name}, err)
	}
	rollback := func(cause error) (Definition, error) {
		removeErr := os.RemoveAll(destination)
		if hadOld {
			renameErr := os.Rename(backup, destination)
			stateErr := writeState(destination, oldRecord.state)
			if renameErr != nil || stateErr != nil {
				keepBackup = true
				return Definition{}, newError("PLUGIN_RECOVERY_REQUIRED", "plugin update rollback failed; recovery files retained", map[string]any{"backup": backup}, errors.Join(cause, removeErr, renameErr, stateErr))
			}
			return Definition{}, errors.Join(cause, removeErr)
		}
		stateErr := os.Remove(hostStatePath(destination))
		if errors.Is(stateErr, os.ErrNotExist) {
			stateErr = nil
		}
		return Definition{}, errors.Join(cause, removeErr, stateErr)
	}
	if err := writeState(destination, state); err != nil {
		return rollback(err)
	}
	installedRecord, err := readPackage(destination, true)
	if err != nil {
		return rollback(err)
	}
	if hadOld {
		if err := os.RemoveAll(backup); err != nil {
			return Definition{}, err
		}
	}
	return cloneDefinition(installedRecord.definition), nil
}

func (s *Store) Remove(name string) error {
	name = strings.TrimSpace(name)
	if err := validateIdentifier("plugin", name); err != nil {
		return err
	}
	release, err := s.acquire()
	if err != nil {
		return err
	}
	defer release()
	path := filepath.Join(s.root, name)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return newError("PLUGIN_NOT_FOUND", "plugin is not installed", map[string]any{"name": name}, nil)
	} else if err != nil {
		return newError("PLUGIN_STORE_READ_FAILED", "inspect plugin directory", map[string]any{"name": name}, err)
	}
	if err := os.RemoveAll(path); err != nil {
		return newError("PLUGIN_STORE_WRITE_FAILED", "remove plugin directory", map[string]any{"name": name}, err)
	}
	if err := os.Remove(hostStatePath(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) SetEnabled(name string, enabled bool) (Definition, error) {
	return s.updateState(name, func(record packageRecord, state *State) error {
		state.Enabled = enabled
		return nil
	})
}

func (s *Store) SetHeavy(name string, heavy bool) (Definition, error) {
	return s.updateState(name, func(_ packageRecord, state *State) error {
		state.Heavy = &heavy
		return nil
	})
}

func (s *Store) SetMemberEnabled(pluginName, kind, member string, enabled bool) (Definition, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	member = strings.TrimSpace(member)
	return s.updateState(pluginName, func(record packageRecord, state *State) error {
		switch kind {
		case "skill":
			if _, ok := record.skillPaths[member]; !ok {
				return newError("PLUGIN_MEMBER_NOT_FOUND", "plugin Skill member was not found", map[string]any{"plugin": pluginName, "member": member, "member_type": kind}, nil)
			}
			state.Skills[member] = enabled
		case "mcp", "mcp_server":
			if _, ok := record.mcpConfigs[member]; !ok {
				return newError("PLUGIN_MEMBER_NOT_FOUND", "plugin MCP server member was not found", map[string]any{"plugin": pluginName, "member": member, "member_type": kind}, nil)
			}
			state.MCPServers[member] = enabled
		default:
			return newError("PLUGIN_MEMBER_TYPE_INVALID", "plugin member type must be skill or mcp_server", map[string]any{"member_type": kind}, nil)
		}
		return nil
	})
}

func (s *Store) updateState(name string, mutate func(packageRecord, *State) error) (Definition, error) {
	name = strings.TrimSpace(name)
	if err := validateIdentifier("plugin", name); err != nil {
		return Definition{}, err
	}
	release, err := s.acquire()
	if err != nil {
		return Definition{}, err
	}
	defer release()
	record, err := s.readInstalledUnlocked(name)
	if err != nil {
		return Definition{}, err
	}
	state := normalizeState(record.state, record)
	if err := mutate(record, &state); err != nil {
		return Definition{}, err
	}
	if err := writeState(record.root, state); err != nil {
		return Definition{}, err
	}
	updated, err := readPackage(record.root, true)
	if err != nil {
		return Definition{}, err
	}
	return cloneDefinition(updated.definition), nil
}

func (s *Store) SkillMembership(name string) (Membership, bool, error) {
	records, err := s.scan()
	if err != nil {
		return Membership{}, false, err
	}
	name = strings.TrimSpace(name)
	for _, record := range records {
		if _, ok := record.skillPaths[name]; ok {
			return Membership{Plugin: record.manifest.Name, Heavy: record.definition.Heavy, Enabled: record.state.Enabled && memberEnabled(record.state.Skills, name)}, true, nil
		}
	}
	return Membership{}, false, nil
}

func (s *Store) MCPMembership(name string) (Membership, bool, error) {
	records, err := s.scan()
	if err != nil {
		return Membership{}, false, err
	}
	name = strings.TrimSpace(name)
	for _, record := range records {
		if cfg, ok := record.mcpConfigs[name]; ok {
			enabled := record.state.Enabled && memberEnabled(record.state.MCPServers, name) && cfg.Enabled
			return Membership{Plugin: record.manifest.Name, Heavy: record.definition.Heavy, Enabled: enabled}, true, nil
		}
	}
	return Membership{}, false, nil
}

func (s *Store) Skill(name string) (SkillMember, bool, error) {
	records, err := s.scan()
	if err != nil {
		return SkillMember{}, false, err
	}
	name = strings.TrimSpace(name)
	for _, record := range records {
		if path, ok := record.skillPaths[name]; ok {
			return SkillMember{Name: name, Plugin: record.manifest.Name, Path: path, Enabled: record.state.Enabled && memberEnabled(record.state.Skills, name)}, true, nil
		}
	}
	return SkillMember{}, false, nil
}

func (s *Store) Skills() ([]SkillMember, error) {
	records, err := s.scan()
	if err != nil {
		return nil, err
	}
	items := make([]SkillMember, 0)
	for _, pluginName := range sortedPackageNames(records) {
		record := records[pluginName]
		names := sortedKeys(record.skillPaths)
		for _, name := range names {
			items = append(items, SkillMember{
				Name: name, Plugin: pluginName, Path: record.skillPaths[name],
				Enabled: record.state.Enabled && memberEnabled(record.state.Skills, name),
			})
		}
	}
	return items, nil
}

// MCPServers returns plugin-owned server definitions with package-relative
// executable and working-directory paths resolved against the installed plugin
// root. Effective Enabled combines manifest, plugin, and member switches.
func (s *Store) MCPServers() (map[string]MCPMember, error) {
	records, err := s.scan()
	if err != nil {
		return nil, err
	}
	items := make(map[string]MCPMember)
	for _, record := range records {
		for name, config := range record.mcpConfigs {
			config.PluginVersion = record.manifest.Version
			config.Enabled = config.Enabled && record.state.Enabled && memberEnabled(record.state.MCPServers, name)
			if config.Enabled && config.PluginData != "" {
				if _, err := containedPath(s.root, config.PluginData, true); err != nil {
					return nil, err
				}
				if err := os.MkdirAll(config.PluginData, 0o700); err != nil {
					return nil, err
				}
				if err := securepath.EnsurePrivate(config.PluginData); err != nil {
					return nil, err
				}
			}
			items[name] = MCPMember{Plugin: record.manifest.Name, Config: config}
		}
	}
	return items, nil
}

func (s *Store) getRecord(name string) (packageRecord, error) {
	name = strings.TrimSpace(name)
	if err := validateIdentifier("plugin", name); err != nil {
		return packageRecord{}, err
	}
	records, err := s.scan()
	if err != nil {
		return packageRecord{}, err
	}
	record, ok := records[name]
	if !ok {
		return packageRecord{}, newError("PLUGIN_NOT_FOUND", "plugin is not installed", map[string]any{"name": name}, nil)
	}
	return record, nil
}

func (s *Store) scan() (map[string]packageRecord, error) {
	release, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	return s.scanUnlocked()
}

func (s *Store) scanUnlocked() (map[string]packageRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, newError("PLUGIN_STORE_READ_FAILED", "list plugin directories", nil, err)
	}
	records := make(map[string]packageRecord)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		root := filepath.Join(s.root, entry.Name())
		record, err := readPackage(root, true)
		if err != nil {
			records[entry.Name()] = packageRecord{root: root, manifest: Manifest{Name: entry.Name()},
				definition: Definition{Name: entry.Name(), Path: root, Enabled: false, Diagnostics: []string{err.Error()}}}
			continue
		}
		if record.manifest.Name != entry.Name() {
			return nil, newError("PLUGIN_DIRECTORY_MISMATCH", "plugin directory name must equal manifest name", map[string]any{"directory": entry.Name(), "manifest_name": record.manifest.Name}, nil)
		}
		if _, exists := records[record.manifest.Name]; exists {
			return nil, newError("PLUGIN_DUPLICATE", "duplicate installed plugin name", map[string]any{"name": record.manifest.Name}, nil)
		}
		if err := ensureUniqueOwnership(records, record, ""); err != nil {
			return nil, err
		}
		records[record.manifest.Name] = record
	}
	return records, nil
}

func (s *Store) readInstalledUnlocked(name string) (packageRecord, error) {
	path := filepath.Join(s.root, name)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return packageRecord{}, newError("PLUGIN_NOT_FOUND", "plugin is not installed", map[string]any{"name": name}, nil)
	} else if err != nil {
		return packageRecord{}, newError("PLUGIN_STORE_READ_FAILED", "inspect plugin directory", map[string]any{"name": name}, err)
	}
	return readPackage(path, true)
}

func (s *Store) prepareSource(source string) (string, func(), error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", nil, newError("PLUGIN_SOURCE_REQUIRED", "plugin source path is required", nil, nil)
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", nil, newError("PLUGIN_SOURCE_INVALID", "resolve plugin source path", map[string]any{"source": source}, err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", nil, newError("PLUGIN_SOURCE_INVALID", "plugin source does not exist", map[string]any{"source": absolute}, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, newError("PLUGIN_SOURCE_INVALID", "plugin source may not be a symbolic link", map[string]any{"source": absolute}, nil)
	}
	if info.IsDir() {
		return locatePackageRoot(absolute)
	}
	if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(absolute), ".zip") {
		return "", nil, newError("PLUGIN_SOURCE_INVALID", "plugin source must be a directory or .zip archive", map[string]any{"source": absolute}, nil)
	}
	work, err := os.MkdirTemp(s.tempRoot, "source-")
	if err != nil {
		return "", nil, newError("PLUGIN_INSTALL_FAILED", "create plugin extraction directory", nil, err)
	}
	cleanup := func() { _ = os.RemoveAll(work) }
	if err := extractZip(absolute, work); err != nil {
		cleanup()
		return "", nil, err
	}
	root, _, err := locatePackageRoot(work)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return root, cleanup, nil
}

func locatePackageRoot(root string) (string, func(), error) {
	manifest := filepath.Join(root, ManifestDirectory, ManifestFilename)
	if info, err := os.Lstat(manifest); err == nil && info.Mode().IsRegular() {
		return root, nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", nil, newError("PLUGIN_SOURCE_INVALID", "list plugin source directory", map[string]any{"source": root}, err)
	}
	candidates := make([]string, 0, 1)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if info, err := os.Lstat(filepath.Join(candidate, ManifestDirectory, ManifestFilename)); err == nil && info.Mode().IsRegular() {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) != 1 {
		return "", nil, newError("PLUGIN_MANIFEST_NOT_FOUND", "plugin source must contain exactly one root plugin.json", map[string]any{"source": root, "candidate_count": len(candidates)}, nil)
	}
	return candidates[0], nil, nil
}

func defaultState(record packageRecord) State {
	state := State{Enabled: true, Skills: map[string]bool{}, MCPServers: map[string]bool{}}
	for name := range record.skillPaths {
		state.Skills[name] = true
	}
	for name := range record.mcpConfigs {
		state.MCPServers[name] = true
	}
	return state
}

func normalizeState(state State, record packageRecord) State {
	if state.Skills == nil {
		state.Skills = map[string]bool{}
	}
	if state.MCPServers == nil {
		state.MCPServers = map[string]bool{}
	}
	skillsState := make(map[string]bool, len(record.skillPaths))
	for name := range record.skillPaths {
		skillsState[name] = memberEnabled(state.Skills, name)
	}
	mcpState := make(map[string]bool, len(record.mcpConfigs))
	for name := range record.mcpConfigs {
		mcpState[name] = memberEnabled(state.MCPServers, name)
	}
	state.Skills = skillsState
	state.MCPServers = mcpState
	return state
}

func mergeState(previous State, next packageRecord) State {
	state := defaultState(next)
	state.Enabled = previous.Enabled
	state.Heavy = previous.Heavy
	for name := range state.Skills {
		if enabled, ok := previous.Skills[name]; ok {
			state.Skills[name] = enabled
		}
	}
	for name := range state.MCPServers {
		if enabled, ok := previous.MCPServers[name]; ok {
			state.MCPServers[name] = enabled
		}
	}
	return state
}

func memberEnabled(values map[string]bool, name string) bool {
	enabled, ok := values[name]
	if !ok {
		return true
	}
	return enabled
}

func writeState(root string, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return newError("PLUGIN_STATE_WRITE_FAILED", "encode plugin state", nil, err)
	}
	data = append(data, '\n')
	path := hostStatePath(root)
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return newError("PLUGIN_STATE_WRITE_FAILED", "write plugin state", map[string]any{"path": path}, err)
	}
	return nil
}

func readStrictJSON(path string, maximum int64, target any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return errors.New("expected a bounded regular state file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maximum {
		return fmt.Errorf("JSON file exceeds %d bytes", maximum)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON file contains a trailing value")
		}
		return err
	}
	return nil
}

func ensureUniqueOwnership(records map[string]packageRecord, candidate packageRecord, replacing string) error {
	for pluginName, existing := range records {
		if pluginName == replacing || pluginName == candidate.manifest.Name {
			continue
		}
		if member, ok := firstMapIntersection(existing.skillPaths, candidate.skillPaths); ok {
			return newError("PLUGIN_MEMBER_CONFLICT", "Skill already belongs to another plugin", map[string]any{"member_type": "skill", "member": member, "plugin": pluginName}, nil)
		}
		if member, ok := firstMapIntersection(existing.mcpConfigs, candidate.mcpConfigs); ok {
			return newError("PLUGIN_MEMBER_CONFLICT", "MCP server already belongs to another plugin", map[string]any{"member_type": "mcp_server", "member": member, "plugin": pluginName}, nil)
		}
	}
	return nil
}

func firstMapIntersection[A, B any](left map[string]A, right map[string]B) (string, bool) {
	for name := range left {
		if _, ok := right[name]; ok {
			return name, true
		}
	}
	return "", false
}

func copyPackageTree(source, destination string) error {
	var files int
	var total int64
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return newError("PLUGIN_INSTALL_FAILED", "read plugin package", map[string]any{"path": path}, walkErr)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return newError("PLUGIN_SOURCE_INVALID", "symbolic links are not allowed in plugin packages", map[string]any{"path": relative}, nil)
		}

		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return newError("PLUGIN_SOURCE_INVALID", "plugin package contains a non-regular file", map[string]any{"path": relative}, err)
		}
		files++
		total += info.Size()
		if files > maxPluginFiles || total > maxPluginBytes {
			return newError("PLUGIN_SOURCE_TOO_LARGE", "plugin package exceeds file or byte limits", map[string]any{"files": files, "bytes": total}, nil)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0o755)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOutErr := output.Close()
		closeInErr := input.Close()
		return errors.Join(copyErr, closeOutErr, closeInErr)
	})
}

func extractZip(path, destination string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return newError("PLUGIN_SOURCE_INVALID", "open plugin ZIP archive", map[string]any{"path": path}, err)
	}
	defer archive.Close()
	var files int
	var total int64
	for _, entry := range archive.File {
		name := filepath.Clean(filepath.FromSlash(entry.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return newError("PLUGIN_SOURCE_INVALID", "plugin ZIP contains an unsafe path", map[string]any{"path": entry.Name}, nil)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return newError("PLUGIN_SOURCE_INVALID", "symbolic links are not allowed in plugin ZIP archives", map[string]any{"path": entry.Name}, nil)
		}
		target := filepath.Join(destination, name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		files++
		total += int64(entry.UncompressedSize64)
		if files > maxPluginFiles || total > maxPluginBytes {
			return newError("PLUGIN_SOURCE_TOO_LARGE", "plugin ZIP exceeds file or byte limits", map[string]any{"files": files, "bytes": total}, nil)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		writer, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, entry.Mode().Perm()&0o755)
		if err != nil {
			reader.Close()
			return err
		}
		_, copyErr := io.Copy(writer, io.LimitReader(reader, int64(entry.UncompressedSize64)+1))
		closeOutErr := writer.Close()
		closeInErr := reader.Close()
		if err := errors.Join(copyErr, closeOutErr, closeInErr); err != nil {
			return err
		}
	}
	return nil
}

func validateIdentifier(kind, value string) error {
	value = strings.TrimSpace(value)
	if (kind == "plugin" && !validPluginName(value)) || value == "." || value == ".." || !identifierPattern.MatchString(value) {
		return newError("PLUGIN_IDENTIFIER_INVALID", fmt.Sprintf("invalid %s identifier", kind), map[string]any{"kind": kind, "value": value}, nil)
	}
	return nil
}

func sortedPackageNames(values map[string]packageRecord) []string {
	return sortedKeys(values)
}

func sortedKeys[V any](values map[string]V) []string {
	items := make([]string, 0, len(values))
	for name := range values {
		items = append(items, name)
	}
	sort.Strings(items)
	return items
}

func cloneDefinition(value Definition) Definition {
	value.Diagnostics = append([]string(nil), value.Diagnostics...)
	value.Skills = append([]string(nil), value.Skills...)
	value.MCPServers = append([]string(nil), value.MCPServers...)
	return value
}

func (s *Store) acquire() (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := filelock.Acquire(ctx, s.lockPath)
	if err != nil {
		return nil, newError("PLUGIN_STORE_LOCK_FAILED", "lock plugin store", nil, err)
	}
	return release, nil
}
