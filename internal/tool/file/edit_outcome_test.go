package file

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditPartialRollbackCountsOnlyConfirmedResiduals(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	changed := "new\n"
	staged := map[string]stagedPatchFile{a: stagedExistingPatchFileForTest(t, a, "a.txt", &changed), b: stagedExistingPatchFileForTest(t, b, "b.txt", &changed)}
	rename := func(source, target string) error {
		if target == b && strings.Contains(filepath.Base(source), "backup") {
			return errors.New("injected restore failure")
		}
		return os.Rename(source, target)
	}
	install := func(source, target string) error {
		if target == b {
			return errors.New("injected install failure")
		}
		return os.Link(source, target)
	}
	err := commitStagedPatchWithFileOps(staged, rename, install)
	if err == nil {
		t.Fatal("fault succeeded")
	}
	result := finishEditStatistics(Result{"insertions": 100, "deletions": 100}, err, false)
	assertEditStats(t, result, "partial", 0, 1)
	data, readErr := os.ReadFile(a)
	if readErr != nil || string(data) != "old\n" {
		t.Fatal("restored file changed", readErr)
	}
	if _, err := os.Stat(b); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected verified deletion", err)
	}
	files := result["file_statistics"].([]editFileStatistics)
	added, removed := 0, 0
	for _, file := range files {
		added += file.Insertions
		removed += file.Deletions
	}
	if added != 0 || removed != 1 {
		t.Fatal("detail totals disagree", files)
	}
}

func TestEditFinalVerificationPreservesForeignReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := "ours\n"
	staged := map[string]stagedPatchFile{path: stagedExistingPatchFileForTest(t, path, "a.txt", &changed)}
	install := func(source, target string) error {
		if err := os.Link(source, target); err != nil {
			return err
		}
		return os.WriteFile(target, []byte("foreign\n"), 0o600)
	}
	err := commitStagedPatchWithFileOps(staged, os.Rename, install)
	if err == nil {
		t.Fatal("foreign bytes were incorrectly confirmed as our successful edit")
	}
	result := finishEditStatistics(Result{"insertions": 1, "deletions": 1}, err, false)
	if result["stats_state"] != "unknown" || result["insertions"] != nil {
		t.Fatal("counted foreign update", result)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "foreign\n" {
		t.Fatal("foreign change overwritten", readErr)
	}
}

func TestEditMoveChainAndOverwriteDetailTotals(t *testing.T) {
	source, step, final := "/source", "/step", "/final"
	old, newText := "one\ntwo\n", "one\nthree\nadded\n"
	staged := map[string]stagedPatchFile{
		source: {Abs: source, Display: "source", Original: []byte(old), OriginalExists: true},
		step:   {Abs: step, Display: "step", MoveFrom: source},
		final:  {Abs: final, Display: "final", MoveFrom: step, Original: []byte("overwritten\n"), OriginalExists: true, Content: &newText},
	}
	files := []editFileStatistics{}
	_, _, stats, err := stagedDiffPreview(staged, 1, &files)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Insertions != 2 || stats.Deletions != 2 || stats.FilesChanged != 2 {
		t.Fatal(stats)
	}
	add, remove := 0, 0
	for _, file := range files {
		add += file.Insertions
		remove += file.Deletions
	}
	if add != stats.Insertions || remove != stats.Deletions {
		t.Fatal("truncated preview corrupted detail totals", files)
	}
	if len(files) != 2 || files[0].Path != "source" || files[0].MoveTo != "final" {
		t.Fatal("move chain was counted more than once", files)
	}
}
