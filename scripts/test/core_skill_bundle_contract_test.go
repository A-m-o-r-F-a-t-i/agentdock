package scripts

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	skills "github.com/uvwt/agentdock/internal/skill"
	skillbundle "github.com/uvwt/agentdock/internal/skill/bundle"
	skillstate "github.com/uvwt/agentdock/internal/skill/state"
)

func TestGeneratedCoreSkillBundleBootstrapsWithRuntimeContract(t *testing.T) {
	python, script := coreSkillBuilder(t)
	output := filepath.Join(t.TempDir(), "bundle")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, python, script, "--output", output)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build real core Skill bundle: %v\n%s", err, data)
	}
	data, err := os.ReadFile(filepath.Join(output, skillbundle.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var manifest skillbundle.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"agentdock-user-guide", "skill-authoring", "skill-installation"}
	if len(manifest.Skills) != len(wantNames) {
		t.Fatalf("unexpected packaged skills: %+v", manifest.Skills)
	}
	state, err := skillstate.New(filepath.Join(t.TempDir(), "skills"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := skills.New(state)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		result, err := skillbundle.Bootstrap(ctx, state, manager, output)
		if err != nil {
			t.Fatalf("generated bundle bootstrap pass %d: %v", pass+1, err)
		}
		if len(result.Skills) != len(wantNames) {
			t.Fatalf("bootstrap returned incomplete result: %+v", result)
		}
		for index, entry := range manifest.Skills {
			if entry.Name != wantNames[index] || entry.Version == "" {
				t.Fatalf("generated identity is incomplete: %+v", entry)
			}
			source, err := skills.LoadSkillDocument(filepath.Join("..", "..", "core-skills", entry.Name))
			if err != nil || source.Version != entry.Version {
				t.Fatalf("generated version differs from source: %+v source=%+v err=%v", entry, source, err)
			}
			selection, err := state.Snapshot(entry.Name)
			if err != nil || !selection.System || selection.Disabled || selection.ActiveVersion != entry.Version {
				t.Fatalf("bundled activation: %+v err=%v", selection, err)
			}
			installed, err := state.Resolve(entry.Name, entry.Version)
			if err != nil {
				t.Fatal(err)
			}
			document, err := skills.LoadSkillDocument(installed)
			if err != nil || document.Name != source.Name || document.Version != source.Version {
				t.Fatalf("installed document: %+v err=%v", document, err)
			}
		}
		bundled, err := state.BundledSkills()
		if err != nil || !reflect.DeepEqual(bundled, wantNames) {
			t.Fatalf("bundled inventory: %q err=%v", bundled, err)
		}
	}
}

func TestCoreSkillBundleBuilderRejectsInvalidVersion(t *testing.T) {
	python, script := coreSkillBuilder(t)
	for _, test := range []struct{ name, line string }{
		{"missing", ""}, {"blank", "version: \n"}, {"invalid", "version: latest\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			name := "agentdock-user-guide"
			dir := filepath.Join(repo, "core-skills", name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			document := "---\nname: " + name + "\ndescription: Test bundle.\n" + test.line + "---\n\n# Test\n"
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "bundle")
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, python, script, "--repo-root", repo, "--output", output)
			data, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(data), name) || !strings.Contains(string(data), "version is required and must be semantic version") {
				t.Fatalf("invalid Skill version was not rejected explicitly: %v\n%s", err, data)
			}
			if _, err := os.Stat(filepath.Join(output, skillbundle.ManifestFile)); !os.IsNotExist(err) {
				t.Fatalf("invalid version emitted a bundle manifest: %v", err)
			}
		})
	}
}
