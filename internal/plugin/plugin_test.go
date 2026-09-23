package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTestPlugin(t *testing.T, root, name, version string, withMCP bool) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"$schema":     pluginSchemaURI,
		"name":        name,
		"description": "Demo Plugin",
	}
	if version != "" {
		manifest["version"] = version
	}
	writeJSONFile(t, filepath.Join(root, "plugin.json"), manifest)

	skillRoot := filepath.Join(root, "skills", "demo-skill")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: demo-skill\ndescription: Demo plugin skill.\n---\n\n# Demo\n"
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}

	if !withMCP {
		return
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(bin, "runner")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(root, "mcp.json"), map[string]any{
		"$schema": mcpSchemaURI,
		"mcpServers": map[string]any{
			"local": map[string]any{
				"type":    "stdio",
				"command": "./bin/runner",
				"args":    []string{"--data", "${PLUGIN_DATA}/cache"},
				"env":     map[string]string{"CONFIG": "${PLUGIN_ROOT}/config.json", "MODE": "demo"},
				"cwd":     "${PLUGIN_ROOT}",
			},
			"remote": map[string]any{
				"type":    "streamable-http",
				"url":     "https://example.com/mcp",
				"headers": map[string]string{"X-Tenant": "public"},
			},
		},
	})
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPackageParsesPortableAgentPlugin(t *testing.T) {
	root := t.TempDir()
	writeTestPlugin(t, root, "demo.plugin", "1.2.3", true)

	pkg, err := LoadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.Schema != pluginSchemaURI || pkg.Manifest.Name != "demo.plugin" || pkg.Manifest.Version != "1.2.3" {
		t.Fatalf("manifest = %+v", pkg.Manifest)
	}
	if len(pkg.Components.Skills) != 1 || pkg.Components.Skills[0].Name != "demo-skill" {
		t.Fatalf("skills = %+v", pkg.Components.Skills)
	}
	if len(pkg.Components.MCP) != 2 {
		t.Fatalf("mcp = %+v", pkg.Components.MCP)
	}
	local := pkg.Components.MCP[0]
	if local.Name != "local" || local.Transport != "stdio" || local.StorageKey != nativeServerName("demo.plugin", "local") {
		t.Fatalf("local MCP = %+v", local)
	}
	if local.Environment["CONFIG"] != "${PLUGIN_ROOT}/config.json" {
		t.Fatalf("portable env was not preserved: %+v", local.Environment)
	}
	if pkg.PackageDigest == "" || len(pkg.Unsupported) != 0 {
		t.Fatalf("digest/unsupported = %q / %+v", pkg.PackageDigest, pkg.Unsupported)
	}
}

func TestLoadPackageDefaultsMissingVersionToLocal(t *testing.T) {
	root := t.TempDir()
	writeTestPlugin(t, root, "demo.plugin", "", false)
	pkg, err := LoadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.Version != VersionLocal {
		t.Fatalf("version = %q, want local", pkg.Manifest.Version)
	}
	if len(pkg.Warnings) == 0 || !strings.Contains(strings.Join(pkg.Warnings, " "), "version=local") {
		t.Fatalf("warnings = %+v", pkg.Warnings)
	}
}

func TestLoadPackageReportsUnsupportedWithoutPartialAcceptance(t *testing.T) {
	root := t.TempDir()
	writeTestPlugin(t, root, "demo.plugin", "1.0.0", false)
	writeJSONFile(t, filepath.Join(root, "plugin.json"), map[string]any{
		"$schema": pluginSchemaURI,
		"name":    "demo.plugin", "version": "1.0.0", "description": "Demo Plugin",
		"future_capability": map[string]any{"enabled": true},
		"extensions":        map[string]any{"example.vendor/future": map[string]any{"enabled": true}},
	})
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hooks", "hook.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(root, "mcp.json"), map[string]any{
		"$schema": mcpSchemaURI,
		"mcpServers": map[string]any{
			"legacy": map[string]any{"type": "sse", "url": "https://example.com/sse"},
		},
	})
	pkg, err := LoadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(pkg.Unsupported, " | ")
	if !strings.Contains(got, "hooks") ||
		!strings.Contains(got, "unsupported sse transport") {
		t.Fatalf("unsupported = %+v", pkg.Unsupported)
	}
	if !strings.Contains(strings.Join(pkg.Warnings, " | "), "future_capability") {
		t.Fatalf("unknown portable field diagnostic missing: %#v", pkg.Warnings)
	}
	manager, err := New(filepath.Join(t.TempDir(), ".agentdock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InstallSource(context.Background(), SourceRequest{Type: "local", Ref: root}, false, false); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("install error = %v, want unsupported rejection", err)
	}
}

func TestLoadPackageRejectsUnsafePortableMCP(t *testing.T) {
	tests := []struct {
		name   string
		server map[string]any
		want   string
	}{
		{
			name:   "non-loopback HTTP",
			server: map[string]any{"type": "streamable-http", "url": "http://example.com/mcp"},
			want:   "must use https",
		},
		{
			name:   "credential header",
			server: map[string]any{"type": "streamable-http", "url": "https://example.com/mcp", "headers": map[string]string{"Authorization": "Bearer secret"}},
			want:   "credential header",
		},
		{
			name:   "command traversal",
			server: map[string]any{"type": "stdio", "command": "../outside"},
			want:   "begin with ./",
		},
		{
			name:   "reserved env",
			server: map[string]any{"type": "stdio", "command": "node", "env": map[string]string{"PLUGIN_DATA_DIR": "bad"}},
			want:   "reserved",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestPlugin(t, root, "demo.plugin", "1.0.0", false)
			writeJSONFile(t, filepath.Join(root, "mcp.json"), map[string]any{
				"$schema":    mcpSchemaURI,
				"mcpServers": map[string]any{"bad": test.server},
			})
			_, err := LoadPackage(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadPackage() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadPackageRejectsPackageSymlink(t *testing.T) {
	root := t.TempDir()
	writeTestPlugin(t, root, "demo.plugin", "1.0.0", false)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "assets-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := LoadPackage(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("LoadPackage() error = %v, want symlink rejection", err)
	}
}

func TestManagerRejectsSymlinkedPluginPackageParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Developer-mode/symlink policy differs between Windows runners. The
		// os.Root implementation is covered by Windows cross-build and data-path tests.
		t.Skip("symlink creation is not reliably available on Windows CI")
	}
	home := filepath.Join(t.TempDir(), ".agentdock")
	manager, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	parent := filepath.Join(home, "plugins", "demo.plugin")
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "plugin")
	writeTestPlugin(t, source, "demo.plugin", "1.0.0", false)
	if _, err := manager.InstallSource(context.Background(), SourceRequest{Type: "local", Ref: source}, false, false); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("install error = %v, want symlink parent rejection", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("install escaped into symlink target: entries=%v err=%v", entries, err)
	}
}

func TestLoadPackageNormalizesSecretEnvironmentBindingsWithoutPersistingValues(t *testing.T) {
	root := t.TempDir()
	writeTestPlugin(t, root, "demo.plugin", "1.0.0", false)
	writeJSONFile(t, filepath.Join(root, "mcp.json"), map[string]any{
		"$schema": mcpSchemaURI,
		"mcpServers": map[string]any{
			"local": map[string]any{
				"type": "stdio", "command": "node",
				"env": map[string]string{
					"TOKEN":  "${DEMO_TOKEN}",
					"CONFIG": "${PLUGIN_ROOT}/config.json",
				},
			},
			"remote": map[string]any{
				"type": "streamable-http", "url": "https://example.com/mcp",
				"headers": map[string]string{
					"Authorization": "${REMOTE_TOKEN}",
					"X-Tenant":      "public",
				},
			},
		},
	})
	pkg, err := LoadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Components.MCP) != 2 {
		t.Fatalf("MCP components = %+v", pkg.Components.MCP)
	}
	local := pkg.Components.MCP[0]
	if local.EnvBindings["TOKEN"] != "DEMO_TOKEN" || local.Environment["CONFIG"] != "${PLUGIN_ROOT}/config.json" {
		t.Fatalf("stdio env normalization = %+v bindings=%+v", local.Environment, local.EnvBindings)
	}
	if _, ok := local.Environment["TOKEN"]; ok {
		t.Fatalf("secret env reference remained a static value: %+v", local.Environment)
	}
	remote := pkg.Components.MCP[1]
	if remote.HeaderEnv["Authorization"] != "REMOTE_TOKEN" || remote.Headers["X-Tenant"] != "public" {
		t.Fatalf("HTTP header normalization = headers=%+v env=%+v", remote.Headers, remote.HeaderEnv)
	}
	if _, ok := remote.Headers["Authorization"]; ok {
		t.Fatalf("credential header remained static: %+v", remote.Headers)
	}
	required := strings.Join(append(append([]string{}, local.RequiredEnv...), remote.RequiredEnv...), ",")
	if !strings.Contains(required, "DEMO_TOKEN") || !strings.Contains(required, "REMOTE_TOKEN") {
		t.Fatalf("required env names = %q", required)
	}

	manager, err := New(filepath.Join(t.TempDir(), ".agentdock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InstallSource(context.Background(), SourceRequest{Type: "local", Ref: root}, false, false); err != nil {
		t.Fatal(err)
	}
	installed, err := manager.Get("demo.plugin")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(hostStatePath(installed.Path))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Bearer")) || bytes.Contains(raw, []byte("${DEMO_TOKEN}")) || bytes.Contains(raw, []byte("${REMOTE_TOKEN}")) {
		t.Fatalf("Plugin state leaked runtime secret expressions: %s", raw)
	}
}

func TestLoadPackageRejectsSensitiveLiteralEnvAndCredentialQuery(t *testing.T) {
	tests := []struct {
		name   string
		server map[string]any
		want   string
	}{
		{
			name: "sensitive literal env",
			server: map[string]any{
				"type": "stdio", "command": "node",
				"env": map[string]string{"API_KEY": "literal-secret"},
			},
			want: "must use a ${ENV_NAME} binding",
		},
		{
			name: "credential query",
			server: map[string]any{
				"type": "streamable-http", "url": "https://example.com/mcp?token=literal-secret",
			},
			want: "query parameters",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestPlugin(t, root, "demo.plugin", "1.0.0", false)
			writeJSONFile(t, filepath.Join(root, "mcp.json"), map[string]any{
				"$schema":    mcpSchemaURI,
				"mcpServers": map[string]any{"bad": test.server},
			})
			_, err := LoadPackage(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadPackage() error = %v, want %q", err, test.want)
			}
		})
	}
}

// These assertions retain the fork's single direct layout. The upstream
// immutable-version Manager, purge-by-default lifecycle and pending-finalize
// journal are competing designs, not a second runtime to silently install.
func TestDirectStoreFailedUpdatePreservesInstalledBytesAndData(t *testing.T) {
	home := t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	writeTestPlugin(t, source, "direct-demo", "1.0.0", false)
	installed, err := store.InstallSource(t.Context(), SourceRequest{Type: "local", Ref: source}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(installed.Path, "skills", "demo-skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, "plugins", ".data", "direct-demo")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(dataDir, "state.db")
	if err := os.WriteFile(dataFile, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetEnabled("direct-demo", false); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(source, "plugin.json"), map[string]any{"$schema": pluginSchemaURI, "name": "direct-demo", "version": "2.0.0", "description": "Invalid update"})
	if err := os.WriteFile(filepath.Join(source, "skills", "demo-skill", "SKILL.md"), []byte("not a valid skill document"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InstallSource(t.Context(), SourceRequest{Type: "local", Ref: source}, true, false); err == nil {
		t.Fatal("invalid update accepted")
	}
	current, err := store.Get("direct-demo")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(current.Path, "skills", "demo-skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != "1.0.0" || current.Enabled || !bytes.Equal(actual, original) {
		t.Fatalf("failed update changed installed state: %#v", current)
	}
	if err := store.Remove("direct-demo"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dataFile)
	if err != nil || string(data) != "keep" {
		t.Fatalf("remove destroyed user data: %q %v", data, err)
	}
}
func TestDirectStoreStartupDoesNotCreateVersionedPluginRegistry(t *testing.T) {
	home := t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	writeTestPlugin(t, source, "direct-demo", "1.0.0", false)
	installed, err := store.InstallSource(t.Context(), SourceRequest{Type: "local", Ref: source}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Path != filepath.Join(home, "plugins", "direct-demo") {
		t.Fatalf("parallel layout introduced: %q", installed.Path)
	}
	reopened, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	current, err := reopened.Get("direct-demo")
	if err != nil || current.Version != "1.0.0" {
		t.Fatalf("reopen changed direct package: %#v %v", current, err)
	}
	for _, name := range []string{"plugins.json", "cache", ".versions"} {
		if _, err := os.Stat(filepath.Join(home, "plugins", name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected parallel registry/layout %s: %v", name, err)
		}
	}
}
