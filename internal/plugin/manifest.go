package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var pluginNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,62}[a-z0-9])?$`)

func validPluginName(name string) bool {
	return pluginNamePattern.MatchString(name) && !strings.Contains(name, "--") && !strings.Contains(name, "..")
}

func readManifest(root string) (Manifest, []string, error) {
	path, err := containedPath(root, filepath.Join(root, ManifestFilename), false)
	if err != nil {
		return Manifest{}, nil, err
	}
	object, err := readJSONObject(path)
	if err != nil {
		return Manifest{}, nil, err
	}
	diagnostics := []string{}
	allowed := map[string]bool{"$schema": true, "name": true, "version": true, "description": true, "author": true, "homepage": true, "repository": true, "license": true, "keywords": true, "extensions": true}
	for _, key := range sortedKeys(object) {
		if !allowed[key] {
			diagnostics = append(diagnostics, "plugin.json: ignored unknown field "+key)
			delete(object, key)
		}
	}
	if object["$schema"] != ManifestSchema {
		return Manifest{}, nil, errors.New("unsupported or missing plugin $schema")
	}
	name, ok := object["name"].(string)
	if !ok || !validPluginName(name) {
		return Manifest{}, nil, errors.New("invalid Agent Plugins name")
	}
	for _, field := range []string{"version", "description", "homepage", "repository", "license"} {
		if value, exists := object[field]; exists {
			if _, ok := value.(string); !ok {
				return Manifest{}, nil, fmt.Errorf("%s must be a string", field)
			}
		}
	}
	if value, exists := object["keywords"]; exists {
		list, ok := value.([]any)
		if !ok {
			return Manifest{}, nil, errors.New("keywords must be an array")
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return Manifest{}, nil, errors.New("keywords entries must be strings")
			}
		}
	}
	if value, exists := object["author"]; exists {
		author, ok := value.(map[string]any)
		if !ok {
			return Manifest{}, nil, errors.New("author must be an object")
		}
		for key, value := range author {
			if key != "name" && key != "email" && key != "url" {
				return Manifest{}, nil, errors.New("unknown author field")
			}
			if _, ok := value.(string); !ok {
				return Manifest{}, nil, errors.New("author fields must be strings")
			}
		}
	}
	if value, exists := object["extensions"]; exists {
		extensions, ok := value.(map[string]any)
		if !ok {
			diagnostics = append(diagnostics, "plugin.json: ignored non-object extensions")
			delete(object, "extensions")
		} else if own, exists := extensions[ExtensionNamespace]; exists {
			settings, ok := own.(map[string]any)
			if !ok {
				diagnostics = append(diagnostics, "plugin.json: ignored invalid AgentDock extension")
				delete(extensions, ExtensionNamespace)
			} else {
				for _, key := range sortedKeys(settings) {
					value := settings[key]
					if key != "heavy" {
						diagnostics = append(diagnostics, "plugin.json: ignored unknown AgentDock extension field "+key)
						delete(settings, key)
						continue
					}
					if _, ok := value.(bool); !ok {
						diagnostics = append(diagnostics, "plugin.json: ignored non-boolean heavy")
						delete(settings, key)
					}
				}
			}
		}
		// Unknown namespaces are deliberately not interpreted or validated.
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return Manifest{}, nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, nil, err
	}
	return manifest, diagnostics, nil
}

func manifestHeavy(manifest Manifest) bool {
	own, _ := manifest.Extensions[ExtensionNamespace].(map[string]any)
	heavy, _ := own["heavy"].(bool)
	return heavy
}

// readJSONObject is bounded and checks regular-file kind before opening pipes.
func readJSONObject(path string) (map[string]any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return nil, errors.New("expected a bounded regular JSON file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestBytes {
		return nil, errors.New("JSON exceeds size limit")
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("expected a JSON object")
	}
	return object, nil
}

// resolvedPath resolves existing ancestors before appending a missing tail,
// detecting junction/symlink escapes for not-yet-created data directories.
func resolvedPath(path string, allowMissing bool) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !allowMissing || !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	resolved, err = resolvedPath(parent, true)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(absolute)), nil
}

func containedPath(root, path string, allowMissing bool) (string, error) {
	resolvedRoot, err := resolvedPath(root, allowMissing)
	if err != nil {
		return "", err
	}
	resolved, err := resolvedPath(path, allowMissing)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("package path escapes its filesystem-resolved root")
	}
	return resolved, nil
}
