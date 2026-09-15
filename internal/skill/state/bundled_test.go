package state

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestBundledSkillsReplaceAndRead(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	empty, err := store.BundledSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("BundledSkills() = %#v, want empty", empty)
	}

	installAndActivateStateSkill(t, store, "skill-authoring", "1.0.0")
	installAndActivateStateSkill(t, store, "skill-installation", "1.0.0")
	if err := store.ReplaceBundledSkills(context.Background(), []string{"skill-installation", "skill-authoring", "skill-installation"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.BundledSkills()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"skill-authoring", "skill-installation"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BundledSkills() = %#v, want %#v", got, want)
	}

	bundled, err := store.IsBundled("skill-authoring")
	if err != nil {
		t.Fatal(err)
	}
	if !bundled {
		t.Fatal("skill-authoring should be bundled")
	}
	bundled, err = store.IsBundled("user-skill")
	if err != nil {
		t.Fatal(err)
	}
	if bundled {
		t.Fatal("user-skill should not be bundled")
	}
	for _, name := range want {
		path, err := store.SystemPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(path); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("system Skill path %q is not a regular directory: info=%v err=%v", path, info, err)
		}
	}

	markerPath := filepath.Join(store.Root(), systemDirectory, systemSkillsMarker)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(markerPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("system Skill marker permissions = %o, want private", info.Mode().Perm())
		}
	} else if _, err := os.Stat(markerPath); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceBundledSkillsMovesRemovedSystemSkillBackToUserRoot(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	installAndActivateStateSkill(t, store, "skill-authoring", "1.0.0")
	if err := store.ReplaceBundledSkills(context.Background(), []string{"skill-authoring"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceBundledSkills(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	userPath, err := store.UserPath("skill-authoring")
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := regularDirectoryExists(userPath); err != nil || !exists {
		t.Fatalf("removed system Skill was not restored to user root: exists=%v err=%v", exists, err)
	}
	bundled, err := store.IsBundled("skill-authoring")
	if err != nil {
		t.Fatal(err)
	}
	if bundled {
		t.Fatal("skill-authoring remained bundled after removal")
	}
}

func TestReplaceBundledSkillsRejectsInvalidNameWithoutChangingList(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	installAndActivateStateSkill(t, store, "skill-authoring", "1.0.0")
	if err := store.ReplaceBundledSkills(context.Background(), []string{"skill-authoring"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceBundledSkills(context.Background(), []string{"../escape"}); err == nil {
		t.Fatal("ReplaceBundledSkills() accepted invalid name")
	}
	got, err := store.BundledSkills()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"skill-authoring"}) {
		t.Fatalf("BundledSkills() = %#v after failed replace", got)
	}
}

func installAndActivateStateSkill(t *testing.T, store *Store, name, version string) {
	t.Helper()
	path, err := store.InstalledPath(name, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(context.Background(), name, version); err != nil {
		t.Fatal(err)
	}
}
