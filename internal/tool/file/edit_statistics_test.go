package file

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func assertEditStats(t *testing.T, result Result, state string, added, removed int) {
	t.Helper()
	if result["stats_state"] != state || result["insertions"] != added || result["deletions"] != removed {
		t.Fatalf("stats=%#v, want %s +%d/-%d", result, state, added, removed)
	}
}

func TestLogicalEditLineStatistics(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		added, removed      int
	}{
		{"empty", "", "", 0, 0}, {"single", "", "中文", 1, 0},
		{"end marker", "中文", "中文\n", 0, 0}, {"CRLF", "甲\r\n乙\r\n", "甲\n乙\n", 0, 0},
		{"blank line", "甲\n", "甲\n\n", 1, 0}, {"delete", "甲\r\n乙", "", 0, 2},
		{"headerlike data", "-- previous\n", "++ replacement\n", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, stats, err := unifiedDiffPreview("test.txt", tc.before, tc.after, 1)
			if err != nil {
				t.Fatal(err)
			}
			if stats.Insertions != tc.added || stats.Deletions != tc.removed {
				t.Fatalf("%+v", stats)
			}
		})
	}
}

func TestNativeEditActualAndPreviewStatistics(t *testing.T) {
	svc, root := newFileTestService(t)
	ctx := context.Background()
	result, err := svc.Edit(ctx, EditRequest{Action: "add", Path: "a.txt", Content: "甲\r\n乙"})
	if err != nil {
		t.Fatal(err)
	}
	assertEditStats(t, result, "known", 2, 0)
	result, err = svc.Edit(ctx, EditRequest{Action: "delete", Path: "a.txt", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	assertEditStats(t, result, "preview", 0, 2)
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	result, err = svc.Edit(ctx, EditRequest{Action: "move", Path: "a.txt", NewPath: "renamed.txt"})
	if err != nil {
		t.Fatal(err)
	}
	assertEditStats(t, result, "known", 0, 0)
	result, err = svc.Edit(ctx, EditRequest{Action: "delete", Path: "renamed.txt"})
	if err != nil {
		t.Fatal(err)
	}
	assertEditStats(t, result, "known", 0, 2)
	result, err = svc.Edit(ctx, EditRequest{Action: "replace", Path: "missing.txt", Old: "a", New: "b"})
	if err == nil {
		t.Fatal("missing target succeeded")
	}
	if result["stats_state"] == "known" {
		assertEditStats(t, result, "known", 0, 0)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 0, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = svc.Edit(ctx, EditRequest{Action: "delete", Path: "binary.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if result["stats_state"] != "unsupported" || result["deletions"] != nil {
		t.Fatalf("binary counted as text: %#v", result)
	}
}

func TestMoveOverwriteCountsActualRemovedDestination(t *testing.T) {
	svc, root := newFileTestService(t)
	for name, text := range map[string]string{"a.txt": "source\n", "b.txt": "old\r\ntarget"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := svc.Edit(context.Background(), EditRequest{Action: "move", Path: "a.txt", NewPath: "b.txt", Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	assertEditStats(t, result, "known", 0, 2)
	data, err := os.ReadFile(filepath.Join(root, "b.txt"))
	if err != nil || string(data) != "source\n" {
		t.Fatalf("destination=%q, %v", data, err)
	}
}

func TestEnvelopeCountsFinalStateAndRenameOnce(t *testing.T) {
	svc, root := newFileTestService(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-alpha\n+beta\n*** Update File: a.txt\n*** Move to: b.txt\n@@\n-beta\n+alpha\n+added\n*** End Patch"
	result, err := svc.Edit(context.Background(), EditRequest{Action: "patch", Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	assertEditStats(t, result, "known", 1, 0)
	if result["files_changed"] != 1 {
		t.Fatalf("rename counted twice: %#v", result)
	}
	patch = "*** Begin Patch\n*** Update File: b.txt\n*** Move to: c.txt\n@@\n alpha\n added\n*** End Patch"
	result, err = svc.Edit(context.Background(), EditRequest{Action: "patch", Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	assertEditStats(t, result, "known", 0, 0)
}

func TestTransactionConflictAndRollbackStatistics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newContent := "ours\n"
	staged := map[string]stagedPatchFile{path: {Abs: path, Display: "a.txt", Original: []byte("before\n"), OriginalExists: true, Mode: 0o600, Content: &newContent}}
	err := commitStagedPatch(staged)
	if err == nil {
		t.Fatal("concurrent modification was overwritten")
	}
	assertEditStats(t, finishEditStatistics(nil, err, false), "known", 0, 0)
	unknown := finishEditStatistics(Result{"insertions": 1, "deletions": 2}, errors.New("connection lost"), false)
	if unknown["stats_state"] != "unknown" || unknown["insertions"] != nil || unknown["deletions"] != nil {
		t.Fatalf("unknown became zero: %#v", unknown)
	}
}

func TestUnifiedGitPatchStatisticsUseCommittedBytes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	svc, root := newFileTestService(t)
	for _, name := range []string{"space name.txt", "中文.txt"} {
		before, after := "甲\r\n乙\r\n", "甲\n乙\n"
		if err := os.WriteFile(filepath.Join(root, name), []byte(before), 0o600); err != nil {
			t.Fatal(err)
		}
		patch, _, _, err := unifiedDiffPreview(name, before, after, 65536)
		if err != nil {
			t.Fatal(err)
		}
		result, err := svc.Edit(context.Background(), EditRequest{Action: "patch", Patch: patch})
		if err != nil {
			t.Fatal(err)
		}
		assertEditStats(t, result, "known", 0, 0)
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != after {
			t.Fatalf("bytes=%q err=%v", data, err)
		}
	}
	patch := "diff --git a/中文.txt b/renamed.txt\nsimilarity index 100%\nrename from 中文.txt\nrename to renamed.txt\n"
	result, err := svc.Edit(context.Background(), EditRequest{Action: "patch", Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	assertEditStats(t, result, "known", 0, 0)
	if _, err := os.Stat(filepath.Join(root, "中文.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rename left source")
	}
	bad := strings.ReplaceAll(patch, "renamed.txt", "../escape.txt")
	result, err = svc.Edit(context.Background(), EditRequest{Action: "patch", Patch: bad})
	if err == nil {
		t.Fatal("escaping patch succeeded")
	}
	assertEditStats(t, result, "known", 0, 0)
}
