package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeInventoryUsesOnePluginSnapshotAndDefersFiles(t *testing.T) {
	service, root := newSkillTestService(t)
	var members []PluginSkill
	for i := 0; i < 15; i++ {
		name := fmt.Sprintf("plugin-skill-%02d", i)
		path := filepath.Join(root, "portable", name)
		if err := os.MkdirAll(filepath.Join(path, "references"), 0o700); err != nil {
			t.Fatal(err)
		}
		doc := fmt.Sprintf("---\nname: %s\ndescription: Inventory fixture.\n---\n# Fixture\n", name)
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "references", "detail.md"), []byte("detail"), 0o600); err != nil {
			t.Fatal(err)
		}
		members = append(members, PluginSkill{Name: name, Plugin: "portable", Path: path, Enabled: i != 3})
	}
	bulkCalls, lookupCalls := 0, 0
	if err := service.SetPluginSkillProvider(func(string) (PluginSkill, bool, error) {
		lookupCalls++
		return PluginSkill{}, false, fmt.Errorf("per-row plugin lookup is forbidden in inventory")
	}, func() ([]PluginSkill, error) { bulkCalls++; return members, nil }); err != nil {
		t.Fatal(err)
	}
	for _, summary := range []bool{true, false} {
		bulkCalls = 0
		var result Result
		var err error
		if summary {
			result, err = service.RuntimeSkillSummaries()
		} else {
			result, err = service.RuntimeSkills()
		}
		if err != nil {
			t.Fatal(err)
		}
		if bulkCalls != 1 || lookupCalls != 0 {
			t.Fatalf("bulk=%d per-row=%d", bulkCalls, lookupCalls)
		}
		items := result["skills"].([]map[string]any)
		if len(items) != 15 || result["summary"] != summary {
			t.Fatalf("bad inventory: %#v", result)
		}
		for i, item := range items {
			if item["enabled"] != (i != 3) || item["name"] != members[i].Name || item["description"] != "Inventory fixture." {
				t.Fatalf("metadata lost: %#v", item)
			}
			count, exists := item["file_count"]
			if summary && exists {
				t.Fatal("summary traversed the file tree")
			}
			if !summary && (!exists || count != 2) {
				t.Fatalf("full API lost file count: %#v", item)
			}
		}
		timing := result["timing_ms"].(map[string]int64)
		for name, value := range timing {
			if value < 0 {
				t.Fatalf("negative timing %s=%d", name, value)
			}
		}
		if summary && timing["files"] != 0 {
			t.Fatal("summary spent time enumerating files")
		}
	}
	// No persistent or stale cache: a subsequent call sees current membership.
	members = members[:1]
	members[0].Enabled = false
	updated, err := service.RuntimeSkillSummaries()
	if err != nil {
		t.Fatal(err)
	}
	items := updated["skills"].([]map[string]any)
	if len(items) != 1 || items[0]["enabled"] != false {
		t.Fatalf("stale membership: %#v", updated)
	}
}
