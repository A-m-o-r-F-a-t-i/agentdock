package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func registryFixture(t *testing.T) (*Registry, Record) {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	registry, err := NewRegistry(home, root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := registry.Select(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	return registry, record
}

func TestWorkspaceRoutingAllKindsAndExplicitExternalTarget(t *testing.T) {
	registry, record := registryFixture(t)
	ctx := context.Background()
	taskID := "tsk_1234567890abcdef"
	for _, test := range []struct{ kind, path, want string }{
		{"source", "src/main.go", filepath.Join(record.Root, "src", "main.go")},
		{"artifact", "release.exe", filepath.Join(record.ArtifactRoot, taskID, "release.exe")},
		{"scratch", "probe.log", filepath.Join(record.ScratchRoot, taskID, "probe.log")},
		{"cache", "dependency.zip", filepath.Join(record.CacheRoot, "dependency.zip")},
	} {
		target, err := ResolveTarget(record, TargetRequest{Kind: test.kind, TaskID: taskID, Path: test.path})
		if err != nil || target.ResolvedPath != test.want || target.WorkspaceID != record.ID {
			t.Fatalf("%s: %+v %v", test.kind, target, err)
		}
	}
	externalParent := t.TempDir()
	canonicalParent, err := filepath.EvalSymlinks(externalParent)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(externalParent, "explicit-report.txt")
	target, err := ResolveTarget(record, TargetRequest{Kind: "external", ExternalPath: external})
	if err != nil || target.ResolvedPath != filepath.Join(canonicalParent, "explicit-report.txt") {
		t.Fatalf("external %+v %v", target, err)
	}
	if _, err = ResolveTarget(record, TargetRequest{Kind: "external", Path: external}); err == nil {
		t.Fatal("implicit external target accepted")
	}
	if _, err = ResolveTarget(record, TargetRequest{Kind: "external", ExternalPath: external, Path: "other.txt"}); err == nil {
		t.Fatal("undeclared external target accepted")
	}
	selected, err := registry.Select(ctx, "", "")
	if err != nil || selected.ID != record.ID {
		t.Fatal("external routing changed the default workspace")
	}
}

func TestWorkspaceRejectsTraversalAndNoImplicitCWD(t *testing.T) {
	_, record := registryFixture(t)
	for _, request := range []TargetRequest{{Path: "../outside.txt"}, {Path: filepath.Join(t.TempDir(), "elsewhere.txt")}, {Kind: "artifact", Path: "file.exe"}, {Kind: "scratch", TaskID: "../escape"}, {Kind: "artifact", TaskID: "tsk_safe", Path: "../other-task/file"}} {
		if _, err := ResolveTarget(record, request); err == nil {
			t.Fatalf("invalid target accepted: %+v", request)
		}
	}
	for _, root := range []string{"", ".", "relative/root"} {
		if _, err := NewRegistry(t.TempDir(), root); !errors.Is(err, ErrWorkspaceRequired) {
			t.Fatalf("implicit root %q: %v", root, err)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := ResolveTarget(record, TargetRequest{Path: filepath.Join(record.Root, "file.txt:secret")}); err == nil {
			t.Fatal("alternate data stream accepted")
		}
	}
}

func TestWorkspaceRegistryRevisionConcurrentUpdates(t *testing.T) {
	registry, record := registryFixture(t)
	ctx := context.Background()
	second, err := NewRegistry(registry.home, record.Root)
	if err != nil {
		t.Fatal(err)
	}
	revision := record.RulesRevision
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	for _, writer := range []*Registry{registry, second} {
		wait.Add(1)
		go func(writer *Registry) {
			defer wait.Done()
			_, err := writer.Register(ctx, RegisterInput{WorkspaceID: record.ID, ExpectedRevision: &revision, Name: "updated"})
			errorsSeen <- err
		}(writer)
	}
	wait.Wait()
	close(errorsSeen)
	conflicts, successes := 0, 0
	for err := range errorsSeen {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrWorkspaceConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent overwrite: successes=%d conflicts=%d", successes, conflicts)
	}
	selected, err := second.Select(ctx, record.ID, "")
	if err != nil || selected.RulesRevision != revision+1 {
		t.Fatalf("revision %+v %v", selected, err)
	}
}

func TestWorkspaceContextSelectionNeverMutatesDefaultAndProjectMapping(t *testing.T) {
	registry, original := registryFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	record, err := registry.EnsureRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := registry.Select(ctx, "", "")
	if err != nil || selected.ID != original.ID {
		t.Fatal("context changed global default")
	}
	revision := record.RulesRevision
	updated, err := registry.Register(ctx, RegisterInput{WorkspaceID: record.ID, ExpectedRevision: &revision, Project: "firmware"})
	if err != nil {
		t.Fatal(err)
	}
	match, err := registry.Select(ctx, "", "firmware")
	if err != nil || match.ID != updated.ID {
		t.Fatalf("project mapping %+v %v", match, err)
	}
	reused, err := registry.EnsureRoot(ctx, root)
	if err != nil || reused.Project != "firmware" || reused.RulesRevision != updated.RulesRevision {
		t.Fatal("context rewrote registered rules")
	}
	other, err := registry.Register(ctx, RegisterInput{Root: t.TempDir(), Project: "firmware"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Select(ctx, "", "firmware"); err == nil {
		t.Fatal("ambiguous project guessed")
	}
	if exact, err := registry.Select(ctx, other.ID, "firmware"); err != nil || exact.ID != other.ID {
		t.Fatal("explicit workspace did not take priority")
	}
}

func TestWorkspaceSymlinkEscapeIsRejected(t *testing.T) {
	_, record := registryFixture(t)
	outside := t.TempDir()
	link := filepath.Join(record.Root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("creating symlink requires platform permission: %v", err)
	}
	if _, err := ResolveTarget(record, TargetRequest{Path: "escape/new.txt"}); !errors.Is(err, ErrWorkspaceBoundary) {
		t.Fatalf("symlink escaped: %v", err)
	}
}

func TestWorkspaceRegistrationValidationPrecedesCreatingDirectory(t *testing.T) {
	registry, _ := registryFixture(t)
	root := filepath.Join(t.TempDir(), "new-root")
	if _, err := registry.Register(context.Background(), RegisterInput{Root: root, CreateRoot: true, Kind: "invalid"}); err == nil {
		t.Fatal("invalid registration accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("invalid registration created the source directory")
	}
	record, err := registry.Register(context.Background(), RegisterInput{Root: root, CreateRoot: true, Name: "created"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := ResolveCommandDirectory(record, TargetRequest{Kind: "scratch", TaskID: "tsk_test"})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(target.ResolvedPath); err != nil || !info.IsDir() {
		t.Fatalf("scratch command directory missing: %v", err)
	}
}

func TestWorkspaceRegistryCorruptionIsNotSilentlyReplaced(t *testing.T) {
	registry, record := registryFixture(t)
	path := filepath.Join(registry.home, "workspaces.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state registryState
	if err = json.Unmarshal(before, &state); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRegistry(registry.home, record.Root); err == nil {
		t.Fatal("corrupt registry silently reset")
	}
	after, _ := os.ReadFile(path)
	if string(after) != "corrupt" {
		t.Fatal("registry corruption was overwritten")
	}
}
