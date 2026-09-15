package plugin

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

func TestStoreTreatsMissingPluginDirectoryAsEmpty(t *testing.T) {
	home := t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 0 {
		t.Fatalf("plugins = %#v, want empty", plugins)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", ".tmp")); err != nil {
		t.Fatalf("direct plugin layout missing .tmp: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugins/cache must not exist: %v", err)
	}
}

func TestStoreInstallsDirectPackageAndPersistsMemberState(t *testing.T) {
	home := t.TempDir()
	source := writePluginPackage(t, t.TempDir(), "pcb", "1.0.0", []string{"layout", "routing"}, []string{"easyeda"})
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Install(source, false)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(home, "plugins", "pcb")
	if created.Path != wantRoot || created.Version != "1.0.0" || !created.Enabled {
		t.Fatalf("created = %#v", created)
	}
	if _, err := os.Stat(filepath.Join(wantRoot, ManifestDirectory, ManifestFilename)); err != nil {
		t.Fatalf("installed manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wantRoot, ManifestDirectory, StateFilename)); err != nil {
		t.Fatalf("installed state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "plugins.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("central logical registry must not exist: %v", err)
	}

	member, ok, err := store.Skill("layout")
	if err != nil || !ok || member.Plugin != "pcb" || member.Path != filepath.Join(wantRoot, "skills", "layout") || !member.Enabled {
		t.Fatalf("skill member = %#v ok=%v err=%v", member, ok, err)
	}
	servers, err := store.MCPServers()
	if err != nil {
		t.Fatal(err)
	}
	if server, ok := servers["easyeda"]; !ok || server.Plugin != "pcb" || !server.Config.Enabled {
		t.Fatalf("MCP member = %#v ok=%v", server, ok)
	}

	if _, err := store.SetMemberEnabled("pcb", "skill", "layout", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMemberEnabled("pcb", "mcp_server", "easyeda", false); err != nil {
		t.Fatal(err)
	}
	member, ok, err = store.Skill("layout")
	if err != nil || !ok || member.Enabled {
		t.Fatalf("disabled skill member = %#v ok=%v err=%v", member, ok, err)
	}
	servers, err = store.MCPServers()
	if err != nil || servers["easyeda"].Config.Enabled {
		t.Fatalf("disabled MCP member = %#v err=%v", servers["easyeda"], err)
	}

	if _, err := store.SetEnabled("pcb", false); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	membership, ok, err := reopened.SkillMembership("routing")
	if err != nil || !ok || membership.Enabled {
		t.Fatalf("disabled plugin membership = %#v ok=%v err=%v", membership, ok, err)
	}
}

func TestStoreRejectsDuplicateMemberOwnership(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	one := writePluginPackage(t, t.TempDir(), "one", "1.0.0", []string{"shared"}, nil)
	two := writePluginPackage(t, t.TempDir(), "two", "1.0.0", []string{"shared"}, nil)
	if _, err := store.Install(one, false); err != nil {
		t.Fatal(err)
	}
	_, err = store.Install(two, false)
	var pluginErr *Error
	if !errors.As(err, &pluginErr) || pluginErr.Code != "PLUGIN_MEMBER_CONFLICT" {
		t.Fatalf("conflict error = %#v", err)
	}
}

func TestStoreUpdatePreservesCompatibleSwitches(t *testing.T) {
	home := t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	v1 := writePluginPackage(t, t.TempDir(), "pcb", "1.0.0", []string{"layout"}, []string{"easyeda"})
	if _, err := store.Install(v1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetEnabled("pcb", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMemberEnabled("pcb", "skill", "layout", false); err != nil {
		t.Fatal(err)
	}
	v2 := writePluginPackage(t, t.TempDir(), "pcb", "2.0.0", []string{"layout", "fabrication"}, []string{"easyeda"})
	updated, err := store.Install(v2, true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "2.0.0" || updated.Enabled {
		t.Fatalf("updated = %#v", updated)
	}
	layout, _, _ := store.Skill("layout")
	fabrication, _, _ := store.Skill("fabrication")
	if layout.Enabled || fabrication.Enabled {
		t.Fatalf("plugin master switch must mask all members: layout=%#v fabrication=%#v", layout, fabrication)
	}
	if _, err := store.SetEnabled("pcb", true); err != nil {
		t.Fatal(err)
	}
	layout, _, _ = store.Skill("layout")
	fabrication, _, _ = store.Skill("fabrication")
	if layout.Enabled || !fabrication.Enabled {
		t.Fatalf("member state not preserved/defaulted: layout=%#v fabrication=%#v", layout, fabrication)
	}
}

func TestStoreInstallsZipWithoutCacheLayer(t *testing.T) {
	home := t.TempDir()
	source := writePluginPackage(t, t.TempDir(), "github", "0.1.0", nil, []string{"github"})
	archive := filepath.Join(t.TempDir(), "github.zip")
	zipDirectory(t, source, archive, "github")
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(archive, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "github", ManifestDirectory, ManifestFilename)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected cache layer: %v", err)
	}
}

func TestStoreRejectsUnknownManifestFields(t *testing.T) {
	root := t.TempDir()
	meta := filepath.Join(root, ManifestDirectory)
	if err := os.MkdirAll(meta, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meta, ManifestFilename), []byte(`{"schema_version":1,"name":"bad","description":"Bad.","version":"1.0.0","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Validate(root)
	var pluginErr *Error
	if !errors.As(err, &pluginErr) || pluginErr.Code != "PLUGIN_MANIFEST_INVALID" {
		t.Fatalf("invalid manifest error = %#v", err)
	}
}

func writePluginPackage(t *testing.T, parent, name, version string, skillNames, serverNames []string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(root, ManifestDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, skillName := range skillNames {
		dir := filepath.Join(root, "skills", skillName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		doc := fmt.Sprintf("---\nname: %s\ndescription: Test Skill %s.\nversion: 1.0.0\n---\n\n# %s\n", skillName, skillName, skillName)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	servers := map[string]mcpclient.ServerConfig{}
	for _, serverName := range serverNames {
		servers[serverName] = mcpclient.ServerConfig{
			Description: "Test MCP " + serverName + ".", Transport: mcpclient.TransportStdio,
			Command: executable, Cwd: root, Enabled: true, TimeoutMS: 1000,
		}
	}
	manifest := Manifest{SchemaVersion: 1, Name: name, Description: "Test plugin " + name + ".", Version: version, MCPServers: servers}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestDirectory, ManifestFilename), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func zipDirectory(t *testing.T, source, destination, prefix string) {
	t.Helper()
	file, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(prefix, relative))
		writer, err := archive.Create(name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_ = runtime.GOOS
}
