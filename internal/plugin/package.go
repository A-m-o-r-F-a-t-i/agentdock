package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
	skills "github.com/uvwt/agentdock/internal/skill"
)

func readPackage(root string, installed bool) (packageRecord, error) {
	return readPackageContext(context.Background(), root, installed, false, nil, nil)
}

func readPackageContext(ctx context.Context, root string, installed, lazy bool, watch func(string), metrics *BuildMetrics) (packageRecord, error) {
	if err := ctx.Err(); err != nil {
		return packageRecord{}, err
	}
	if watch != nil {
		watch(root)
		watch(filepath.Join(root, "skills"))
	}

	manifest, diagnostics, err := readManifest(root)
	if err != nil {
		return packageRecord{}, newError("PLUGIN_MANIFEST_INVALID", "invalid root plugin.json: "+err.Error(), map[string]any{"path": filepath.Join(root, ManifestFilename)}, err)
	}
	// Read host Heavy override before touching any member document. Metadata
	// paths and ownership still undergo the same filesystem safety checks.
	preloaded := State{Enabled: true}
	hasState := false
	if installed {
		statePath := hostStatePath(root)
		if _, err := os.Lstat(statePath); err == nil {
			if _, err := containedPath(filepath.Dir(root), statePath, false); err != nil {
				return packageRecord{}, err
			}
			if err := readStrictJSON(statePath, maxStateBytes, &preloaded); err != nil {
				return packageRecord{}, newError("PLUGIN_STATE_INVALID", "read host plugin state", map[string]any{"plugin": manifest.Name}, err)
			}
			hasState = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return packageRecord{}, err
		}
	}
	heavy := manifestHeavy(manifest)
	if preloaded.Heavy != nil {
		heavy = *preloaded.Heavy
	}
	descriptions := map[string]string{}
	skillPaths, skillDiagnostics := discoverPortableSkillsContext(ctx, root, lazy && heavy, watch, metrics, descriptions)
	if err := ctx.Err(); err != nil {
		return packageRecord{}, err
	}

	diagnostics = append(diagnostics, skillDiagnostics...)
	configs, mcpDiagnostics := readMCP(root, manifest.Name)
	diagnostics = append(diagnostics, mcpDiagnostics...)
	record := packageRecord{root: root, manifest: manifest, skillPaths: skillPaths, skillDescriptions: descriptions, mcpConfigs: configs}
	state := defaultState(record)
	if installed {
		if hasState {
			state = preloaded
		}
		if state.Source != nil {
			for name, cfg := range configs {
				configs[name] = applySourceEnvironmentBindings(cfg)
			}
		}
		hostPath := filepath.Join(filepath.Dir(root), ".config", manifest.Name+".json")
		if _, err := os.Lstat(hostPath); err == nil {
			if _, err := containedPath(filepath.Dir(root), hostPath, false); err != nil {
				return record, err
			}
			var host HostConfig
			if err := readStrictJSON(hostPath, maxStateBytes, &host); err != nil {
				return record, newError("PLUGIN_HOST_CONFIG_INVALID", "read host plugin configuration", map[string]any{"plugin": manifest.Name}, err)
			}
			for rawName, local := range host.MCPServers {
				name := nativeServerName(manifest.Name, rawName)
				config, exists := configs[name]
				if !exists {
					continue
				}
				config.HeaderEnv = mergeStringValues(config.HeaderEnv, local.HeaderEnv)
				config.EnvFromEnv = mergeStringValues(config.EnvFromEnv, local.EnvFromEnv)
				if local.Description != "" {
					config.Description = local.Description
				}
				if local.TimeoutMS != 0 {
					config.TimeoutMS = local.TimeoutMS
				}
				if local.Command != "" {
					if config.Transport != mcpclient.TransportStdio || !filepath.IsAbs(local.Command) {
						delete(configs, name)
						diagnostics = append(diagnostics, "MCP "+name+": invalid host executable override")
						continue
					}
					config.Command = local.Command
				}
				config = mcpclient.NormalizeServerConfig(config)
				if err := mcpclient.ValidateServerConfig(config); err != nil {
					delete(configs, name)
					diagnostics = append(diagnostics, "MCP "+name+": invalid host configuration")
					continue
				}
				configs[name] = config
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return record, err
		}
	}
	record.state = normalizeState(state, record)
	record.definition = Definition{
		Source: state.Source, Compatibility: state.Compatibility,
		Name: manifest.Name, Description: manifest.Description, Version: manifest.Version,
		Path: root, Enabled: state.Enabled, Heavy: heavy, Skills: sortedKeys(skillPaths), MCPServers: sortedKeys(configs), Diagnostics: diagnostics,
	}
	if err := ctx.Err(); err != nil {
		return packageRecord{}, err
	}
	return record, nil
}

func discoverPortableSkills(root string) (map[string]string, []string) {
	return discoverPortableSkillsContext(context.Background(), root, false, nil, nil, nil)
}
func discoverPortableSkillsContext(ctx context.Context, root string, lazy bool, watch func(string), metrics *BuildMetrics, descriptions map[string]string) (map[string]string, []string) {
	items := map[string]string{}
	skillsRoot := filepath.Join(root, "skills")
	if _, err := os.Lstat(skillsRoot); errors.Is(err, os.ErrNotExist) {
		return items, nil
	}
	skillsRoot, err := containedPath(root, skillsRoot, false)
	if err != nil {
		return items, []string{"skills/: rejected filesystem path"}
	}
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return items, []string{"skills/: expected a readable directory"}
	}
	diagnostics := []string{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return items, diagnostics
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(skillsRoot, name)
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Lstat(filepath.Join(path, "SKILL.md")); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := containedPath(root, filepath.Join(path, "SKILL.md"), false); err != nil {
			diagnostics = append(diagnostics, "Skill "+name+": rejected SKILL.md filesystem path")
			continue
		}
		if watch != nil {
			watch(path)
		}
		if lazy {
			items[name] = path
			continue
		}
		if metrics != nil {
			metrics.SkillDocuments++
		}
		doc, err := skills.LoadPortableSkillDocument(path)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("Skill %s: %s", name, err))
			continue
		}
		if doc.Name != name {
			diagnostics = append(diagnostics, "Skill "+name+": directory and frontmatter name differ")
			continue
		}
		items[name] = path
		if descriptions != nil {
			descriptions[name] = doc.Description
		}
	}
	return items, diagnostics
}

func hostStatePath(root string) string {
	return filepath.Join(filepath.Dir(root), ".state", filepath.Base(root)+".json")
}

func mergeStringValues(base, overlay map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}
