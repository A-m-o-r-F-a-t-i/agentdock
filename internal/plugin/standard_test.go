package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeStandardJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStandardMinimalMetadataAndFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	writeStandardJSON(t, filepath.Join(root, ManifestFilename), map[string]any{"$schema": ManifestSchema, "name": "a", "version": "release candidate", "homepage": "not a URL", "extensions": map[string]any{"unknown.namespace": 42}})
	record, err := readPackage(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if record.definition.Heavy || len(record.definition.Skills) != 0 || len(record.definition.Diagnostics) != 0 {
		t.Fatalf("minimal plugin rejected/changed: %#v", record.definition)
	}
	goodSkill := filepath.Join(root, "skills", "demo")
	if err := os.MkdirAll(goodSkill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodSkill, "SKILL.md"), []byte("---\nname: demo\ndescription: 'Description: quoted # literal'\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	badSkill := filepath.Join(root, "skills", "bad")
	if err := os.MkdirAll(badSkill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badSkill, "SKILL.md"), []byte("---\nname: INVALID\ndescription: invalid\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeStandardJSON(t, filepath.Join(root, MCPFilename), map[string]any{"$schema": MCPSchema, "mcpServers": map[string]any{
		"good":        map[string]any{"type": "stdio", "command": "node"},
		"bad":         map[string]any{"type": "stdio", "command": "../escape"},
		"unsupported": map[string]any{"type": "sse", "url": "https://example.org/sse"},
	}})
	record, err = readPackage(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.skillPaths) != 1 || len(record.mcpConfigs) != 1 || len(record.definition.Diagnostics) != 3 {
		t.Fatalf("bad components were not isolated: %#v", record.definition)
	}
}

func TestStandardMCPExpansionAndBoundaryValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "literal-${PLUGIN_DATA}")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseMCPServer(root, "portable", "server", map[string]any{
		"type": "stdio", "command": "node", "args": []any{"${PLUGIN_ROOT}/x", "${UNKNOWN}", "../opaque"},
		"env": map[string]any{"VALUE": "  ${PLUGIN_ROOT}/x  "}, "cwd": "${PLUGIN_DATA}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Args[0] != cfg.PluginRoot+"/x" || !strings.Contains(cfg.Args[0], "${PLUGIN_DATA}") || cfg.Args[1] != "${UNKNOWN}" || cfg.Args[2] != "../opaque" {
		t.Fatalf("expansion not single-pass/literal: %#v", cfg.Args)
	}
	if cfg.PackageEnv["VALUE"] != "  "+cfg.PluginRoot+"/x  " || cfg.Cwd != cfg.PluginData {
		t.Fatalf("expansion altered values: %#v", cfg)
	}
	cases := []map[string]any{
		{"type": "stdio", "command": "node --eval x"},
		{"type": "stdio", "command": "./../escape"},
		{"type": "stdio", "command": "node", "cwd": "../escape"},
		{"type": "stdio", "command": "node", "cwd": "${PLUGIN_DATA}/../escape"},
		{"type": "stdio", "command": "node", "env": map[string]any{"PLUGIN_ROOT": "override"}},
		{"type": "streamable-http", "url": "http://example.org/mcp"},
		{"type": "streamable-http", "url": "https://user:pass@example.org/mcp"},
		{"type": "streamable-http", "url": "https://example.org/mcp#fragment"},
		{"type": "streamable-http", "url": "https://example.org/mcp", "headers": map[string]any{"X-Mode": "a", "x-mode": "b"}},
		{"type": "streamable-http", "url": "https://example.org/mcp", "headers": map[string]any{"X-Mode": "a\r\nInjected: x"}},
		{"type": "stdio", "command": "node", "enabled": false},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, map[string]any{"type": "stdio", "command": "node", "env": map[string]any{"plugin_root": "override"}})
	}
	for i, input := range cases {
		if _, err := parseMCPServer(root, "portable", "server", input); err == nil {
			t.Fatalf("accepted invalid case %d", i)
		}
	}
	literal := "${ENV_SECRET}"
	cfg, err = parseMCPServer(root, "portable", "server", map[string]any{"type": "streamable-http", "url": "https://example.org/mcp?x=${UNKNOWN}", "headers": map[string]any{"X-Literal": literal}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PackageHeaders["X-Literal"] != literal || !strings.Contains(cfg.URL, "${UNKNOWN}") {
		t.Fatal("expanded opaque remote values")
	}
}

func TestStandardFilesystemResolvedConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := containedPath(root, filepath.Join(link, "new", "file"), true); err == nil {
		t.Fatal("accepted missing file through escaping symlink")
	}
	if _, err := parseMCPServer(root, "portable", "server", map[string]any{"type": "stdio", "command": "./escape/server"}); err == nil {
		t.Fatal("accepted executable escape")
	}
}
