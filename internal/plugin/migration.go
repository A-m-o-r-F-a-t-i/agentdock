package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/securepath"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
	skills "github.com/uvwt/agentdock/internal/skill"
	"gopkg.in/yaml.v3"
)

const legacyDirectory = ".agentdock-plugin"

type legacyManifest struct {
	SchemaVersion int                               `json:"schema_version"`
	Name          string                            `json:"name"`
	Description   string                            `json:"description"`
	Version       string                            `json:"version"`
	MCPServers    map[string]mcpclient.ServerConfig `json:"mcpServers,omitempty"`
}

type migrationWrite struct {
	path string
	data []byte
}

type LegacyMigration struct {
	home     string
	roots    []string
	writes   []migrationWrite
	removals []string
}

// PlanLegacyMigration only reads explicitly selected installed packages. The
// normal package loader never discovers or interprets the retired manifest.
func PlanLegacyMigration(home string) (*LegacyMigration, error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	plan := &LegacyMigration{home: home}
	pluginRoot := filepath.Join(home, "plugins")
	entries, err := os.ReadDir(pluginRoot)
	if errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		root := filepath.Join(pluginRoot, entry.Name())
		legacyPath := filepath.Join(root, legacyDirectory, ManifestFilename)
		if _, err := os.Lstat(legacyPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if _, err := containedPath(pluginRoot, legacyPath, false); err != nil {
			return nil, err
		}
		for _, name := range []string{ManifestFilename, MCPFilename} {
			if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
				return nil, fmt.Errorf("%s contains both legacy and standard configuration; resolve the conflict before migration", entry.Name())
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if err := plan.addLegacy(root); err != nil {
			return nil, fmt.Errorf("migrate plugin %s: %w", entry.Name(), err)
		}
	}
	return plan, nil
}

func (plan *LegacyMigration) Names() []string {
	names := make([]string, 0, len(plan.roots))
	for _, root := range plan.roots {
		names = append(names, filepath.Base(root))
	}
	return names
}

// Apply requires the installer's persistent rollback journal or an offline
// backup writer. Every old/new path is snapshotted before the first mutation.
func (plan *LegacyMigration) Apply(ctx context.Context, snapshot func(string) error) error {
	if len(plan.roots) == 0 {
		return nil
	}
	if snapshot == nil {
		return errors.New("legacy migration requires a rollback snapshot writer")
	}
	for _, change := range plan.writes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := snapshot(change.path); err != nil {
			return err
		}
	}
	for _, path := range plan.removals {
		if err := snapshot(path); err != nil {
			return err
		}
	}
	for _, change := range plan.writes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := containedPath(plan.home, change.path, true); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(change.path), 0700); err != nil {
			return err
		}
		if err := securepath.EnsurePrivate(filepath.Dir(change.path)); err != nil {
			return err
		}
		if err := atomicfile.Write(change.path, change.data, 0600); err != nil {
			return err
		}
	}
	for _, root := range plan.roots {
		record, err := readPackage(root, true)
		if err != nil {
			return err
		}
		if len(record.definition.Diagnostics) > 0 {
			return fmt.Errorf("migrated plugin %s did not validate: %s", record.manifest.Name, strings.Join(record.definition.Diagnostics, "; "))
		}
	}
	for _, path := range plan.removals {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func (plan *LegacyMigration) addJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	plan.writes = append(plan.writes, migrationWrite{path: path, data: append(data, '\n')})
	return nil
}

func (plan *LegacyMigration) addLegacy(root string) error {
	var legacy legacyManifest
	if err := readStrictJSON(filepath.Join(root, legacyDirectory, ManifestFilename), maxManifestBytes, &legacy); err != nil {
		return err
	}
	if legacy.SchemaVersion != 1 || !validPluginName(legacy.Name) || legacy.Name != filepath.Base(root) {
		return errors.New("unsupported legacy schema or invalid plugin name")
	}
	state := State{Enabled: true, Skills: map[string]bool{}, MCPServers: map[string]bool{}}
	statePath := filepath.Join(root, legacyDirectory, StateFilename)
	if _, err := os.Lstat(statePath); err == nil {
		if err := readStrictJSON(statePath, maxStateBytes, &state); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if state.MCPServers == nil {
		state.MCPServers = map[string]bool{}
	}
	manifest := Manifest{Schema: ManifestSchema, Name: legacy.Name, Version: legacy.Version, Description: legacy.Description,
		Extensions: map[string]any{ExtensionNamespace: map[string]any{"heavy": true}}}
	portable := MCPConfig{Schema: MCPSchema, MCPServers: map[string]MCPServer{}}
	host := HostConfig{MCPServers: map[string]MCPHostConfig{}}
	hostPath := filepath.Join(filepath.Dir(root), ".config", legacy.Name+".json")
	if _, err := os.Lstat(hostPath); err == nil {
		return errors.New("host configuration already exists; refusing to replace it during legacy migration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range sortedKeys(legacy.MCPServers) {
		old := legacy.MCPServers[name]
		local := MCPHostConfig{Description: old.Description, HeaderEnv: old.HeaderEnv, EnvFromEnv: old.EnvFromEnv, TimeoutMS: old.TimeoutMS}
		server := MCPServer{Args: old.Args}
		switch old.Transport {
		case mcpclient.TransportStdio:
			server.Type = "stdio"
			server.Command = old.Command
			if filepath.IsAbs(old.Command) {
				relative, err := filepath.Rel(root, old.Command)
				if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					server.Command = "./" + filepath.ToSlash(relative)
				} else {
					server.Command = filepath.Base(old.Command)
					local.Command = old.Command
				}
			} else if strings.ContainsAny(old.Command, "/\\") {
				server.Command = "./" + strings.TrimPrefix(filepath.ToSlash(old.Command), "./")
			}
			if old.Cwd != "" {
				cwd := old.Cwd
				if filepath.IsAbs(cwd) {
					relative, err := filepath.Rel(root, cwd)
					if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
						return fmt.Errorf("MCP %s has a working directory outside the package", name)
					}
					cwd = relative
				}
				server.Cwd = "./" + strings.TrimPrefix(filepath.ToSlash(cwd), "./")
			}
		case mcpclient.TransportStreamableHTTP:
			server.Type = "streamable-http"
			server.URL = old.URL
			server.Args = nil
		default:
			return fmt.Errorf("unsupported legacy MCP transport for %s", name)
		}
		portable.MCPServers[name] = server
		host.MCPServers[name] = local
		state.MCPServers[nativeServerName(legacy.Name, name)] = old.Enabled && memberEnabled(state.MCPServers, name)
	}
	if err := plan.addJSON(filepath.Join(root, ManifestFilename), manifest); err != nil {
		return err
	}
	if err := plan.addJSON(filepath.Join(root, MCPFilename), portable); err != nil {
		return err
	}
	if err := plan.addJSON(hostStatePath(root), state); err != nil {
		return err
	}
	if err := plan.addJSON(hostPath, host); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(root, "skills"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		skillRoot := filepath.Join(root, "skills", entry.Name())
		path := filepath.Join(skillRoot, "SKILL.md")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := containedPath(root, path, false); err != nil {
			return err
		}
		// Validate the complete bounded document before touching its frontmatter.
		if _, err := skills.LoadPortableSkillDocument(skillRoot); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		converted, err := portableFrontmatter(data)
		if err != nil {
			return err
		}
		if string(converted) != string(data) {
			plan.writes = append(plan.writes, migrationWrite{path: path, data: converted})
		}
	}
	plan.roots = append(plan.roots, root)
	plan.removals = append(plan.removals, filepath.Join(root, legacyDirectory))
	return nil
}

// Move legacy Skill version metadata without changing body text or other YAML.
func portableFrontmatter(data []byte) ([]byte, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	parts := strings.SplitN(text, "\n---", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "---\n") {
		return nil, errors.New("invalid Skill frontmatter")
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(parts[0], "---\n")), &fields); err != nil {
		return nil, err
	}
	version, exists := fields["version"]
	if !exists {
		return data, nil
	}
	metadata, _ := fields["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, exists := metadata["version"]; !exists {
		metadata["version"] = fmt.Sprint(version)
	}
	fields["metadata"] = metadata
	delete(fields, "version")
	front, err := yaml.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(front) + "---" + parts[1]), nil
}
