package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	registry "github.com/uvwt/agentdock/internal/plugin"
)

func TestExplicitExpansionReportsHeavyWithoutChangingIt(t *testing.T) {
	for _, heavy := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "heavy"}[heavy], func(t *testing.T) {
			store, err := registry.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := writeToolPluginPackage(t, t.TempDir(), "fixture", []string{"workflow"}, []string{"remote"})
			path := filepath.Join(source, registry.ManifestDirectory, registry.ManifestFilename)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest registry.Manifest
			if err = json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Extensions[registry.ExtensionNamespace] = map[string]any{"heavy": heavy}
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			service := testPluginService(store, false)
			if _, err = service.Manage(t.Context(), ManageRequest{Action: "install", Source: source}); err != nil {
				t.Fatal(err)
			}
			before, err := store.Get("fixture")
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				result, err := service.Load(t.Context(), LoadRequest{Name: "fixture"})
				if err != nil {
					t.Fatal(err)
				}
				plugin := result["plugin"].(map[string]any)
				if plugin["heavy"] != heavy || plugin["load_required"] != heavy {
					t.Fatalf("wrong loading policy: %v", plugin)
				}
			}
			after, err := store.Get("fixture")
			if err != nil || after.Heavy != heavy || before.Heavy != after.Heavy {
				t.Fatal("expanding a plugin changed Heavy configuration")
			}
			deferred, err := service.CapabilityItems()
			if err != nil {
				t.Fatal(err)
			}
			if heavy && len(deferred) != 1 || !heavy && len(deferred) != 0 {
				t.Fatal("ordinary plugin incorrectly requires deferred expansion")
			}
		})
	}
}
