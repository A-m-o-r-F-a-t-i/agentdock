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
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
	skills "github.com/uvwt/agentdock/internal/skill"
)

const (
	manifestSchemaVersion = 1
	maxManifestBytes      = 1 << 20
	maxStateBytes         = 1 << 20
	maxPluginFiles        = 20000
	maxPluginBytes        = int64(512 << 20)
	maxDescriptionBytes   = 4096
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
	for _, path := range []string{s.root, filepath.Dir(s.lockPath), s.tempRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return newError("PLUGIN_STORE_WRITE_FAILED", "create plugin store directory", map[string]any{"path": path}, err)
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

// Install copies one complete heavy-plugin package into plugins/<name>. The
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
	defer os.RemoveAll(work)
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
	if err := writeState(staged, state); err != nil {
		return Definition{}, err
	}
	if _, err := readPackage(staged, true); err != nil {
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
			_ = os.Rename(backup, destination)
		}
		return Definition{}, newError("PLUGIN_INSTALL_FAILED", "activate installed plugin directory", map[string]any{"name": sourceRecord.manifest.Name}, err)
	}
	if hadOld {
		_ = os.RemoveAll(backup)
		_ = oldRecord
	}
	installedRecord, err := readPackage(destination, true)
	if err != nil {
		return Definition{}, err
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
	return nil
}

func (s *Store) SetEnabled(name string, enabled bool) (Definition, error) {
	return s.updateState(name, func(record packageRecord, state *State) error {
		state.Enabled = enabled
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
			return Membership{Plugin: record.manifest.Name, Enabled: record.state.Enabled && memberEnabled(record.state.Skills, name)}, true, nil
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
			return Membership{Plugin: record.manifest.Name, Enabled: enabled}, true, nil
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
			config.Enabled = config.Enabled && record.state.Enabled && memberEnabled(record.state.MCPServers, name)
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
			return nil, err
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
		return "", nil, newError("PLUGIN_MANIFEST_NOT_FOUND", "plugin source must contain exactly one .agentdock-plugin/plugin.json", map[string]any{"source": root, "candidate_count": len(candidates)}, nil)
	}
	return candidates[0], nil, nil
}

func readPackage(root string, installed bool) (packageRecord, error) {
	manifestPath := filepath.Join(root, ManifestDirectory, ManifestFilename)
	var manifest Manifest
	if err := readStrictJSON(manifestPath, maxManifestBytes, &manifest); err != nil {
		return packageRecord{}, newError("PLUGIN_MANIFEST_INVALID", "read plugin manifest", map[string]any{"path": manifestPath}, err)
	}
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Description = strings.TrimSpace(manifest.Description)
	manifest.Version = strings.TrimSpace(manifest.Version)
	if manifest.SchemaVersion != manifestSchemaVersion {
		return packageRecord{}, newError("PLUGIN_MANIFEST_VERSION_UNSUPPORTED", "unsupported plugin manifest schema version", map[string]any{"version": manifest.SchemaVersion}, nil)
	}
	if err := validateIdentifier("plugin", manifest.Name); err != nil {
		return packageRecord{}, err
	}
	if err := validateIdentifier("version", manifest.Version); err != nil {
		return packageRecord{}, err
	}
	if manifest.Description == "" {
		return packageRecord{}, newError("PLUGIN_DESCRIPTION_REQUIRED", "plugin description is required", map[string]any{"name": manifest.Name}, nil)
	}
	if len([]byte(manifest.Description)) > maxDescriptionBytes {
		return packageRecord{}, newError("PLUGIN_DESCRIPTION_TOO_LONG", "plugin description is too long", map[string]any{"name": manifest.Name, "maximum_bytes": maxDescriptionBytes}, nil)
	}

	skillPaths, err := discoverSkills(root)
	if err != nil {
		return packageRecord{}, err
	}
	configs := make(map[string]mcpclient.ServerConfig, len(manifest.MCPServers))
	for rawName, rawConfig := range manifest.MCPServers {
		name := strings.TrimSpace(rawName)
		if err := validateIdentifier("MCP server", name); err != nil {
			return packageRecord{}, err
		}
		config := rawConfig
		if config.Name != "" && strings.TrimSpace(config.Name) != name {
			return packageRecord{}, newError("PLUGIN_MCP_NAME_MISMATCH", "MCP map key and config name must match", map[string]any{"plugin": manifest.Name, "key": name, "name": config.Name}, nil)
		}
		config.Name = name
		config = resolveMCPConfig(root, config)
		config = mcpclient.NormalizeServerConfig(config)
		if err := mcpclient.ValidateServerConfig(config); err != nil {
			return packageRecord{}, newError("PLUGIN_MCP_INVALID", "validate plugin MCP server", map[string]any{"plugin": manifest.Name, "server": name}, err)
		}
		configs[name] = config
	}
	if len(skillPaths) == 0 && len(configs) == 0 {
		return packageRecord{}, newError("PLUGIN_MEMBERS_REQUIRED", "plugin must contain at least one Skill or MCP server", map[string]any{"name": manifest.Name}, nil)
	}

	record := packageRecord{root: root, manifest: manifest, skillPaths: skillPaths, mcpConfigs: configs}
	state := defaultState(record)
	statePath := filepath.Join(root, ManifestDirectory, StateFilename)
	if installed {
		if _, err := os.Lstat(statePath); err == nil {
			if err := readStrictJSON(statePath, maxStateBytes, &state); err != nil {
				return packageRecord{}, newError("PLUGIN_STATE_INVALID", "read plugin state", map[string]any{"plugin": manifest.Name}, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return packageRecord{}, newError("PLUGIN_STATE_INVALID", "inspect plugin state", map[string]any{"plugin": manifest.Name}, err)
		}
	}
	state = normalizeState(state, record)
	record.state = state
	record.definition = Definition{
		Name: manifest.Name, Description: manifest.Description, Version: manifest.Version,
		Path: root, Enabled: state.Enabled, Skills: sortedKeys(skillPaths), MCPServers: sortedKeys(configs),
	}
	return record, nil
}

func discoverSkills(root string) (map[string]string, error) {
	skillsRoot := filepath.Join(root, "skills")
	entries, err := os.ReadDir(skillsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, newError("PLUGIN_SKILLS_INVALID", "list plugin Skills", map[string]any{"path": skillsRoot}, err)
	}
	items := make(map[string]string)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if err := validateIdentifier("Skill", name); err != nil {
			return nil, err
		}
		path := filepath.Join(skillsRoot, name)
		if err := skills.ValidatePackage(path); err != nil {
			return nil, newError("PLUGIN_SKILL_INVALID", "validate plugin Skill package", map[string]any{"plugin_root": root, "skill": name}, err)
		}
		document, err := skills.LoadSkillDocument(path)
		if err != nil {
			return nil, newError("PLUGIN_SKILL_INVALID", "read plugin Skill document", map[string]any{"plugin_root": root, "skill": name}, err)
		}
		if strings.TrimSpace(document.Name) != name {
			return nil, newError("PLUGIN_SKILL_NAME_MISMATCH", "Skill directory and document name must match", map[string]any{"directory": name, "document_name": document.Name}, nil)
		}
		items[name] = path
	}
	return items, nil
}

func resolveMCPConfig(root string, config mcpclient.ServerConfig) mcpclient.ServerConfig {
	if strings.TrimSpace(config.Cwd) == "" && config.Transport == mcpclient.TransportStdio {
		config.Cwd = root
	} else if config.Cwd != "" && !filepath.IsAbs(config.Cwd) {
		config.Cwd = filepath.Join(root, filepath.FromSlash(config.Cwd))
	}
	command := strings.TrimSpace(config.Command)
	if command != "" && !filepath.IsAbs(command) && (strings.ContainsAny(command, `/\\`) || strings.HasPrefix(command, ".")) {
		config.Command = filepath.Join(root, filepath.FromSlash(command))
	}
	return config
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
	path := filepath.Join(root, ManifestDirectory, StateFilename)
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return newError("PLUGIN_STATE_WRITE_FAILED", "write plugin state", map[string]any{"path": path}, err)
	}
	return nil
}

func readStrictJSON(path string, maximum int64, target any) error {
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
		if filepath.Clean(relative) == filepath.Join(ManifestDirectory, StateFilename) {
			return nil
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
	if !identifierPattern.MatchString(value) {
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
