package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func journalFixture(t *testing.T) (*rollbackJournal, string, string) {
	t.Helper()
	root := t.TempDir()
	original := filepath.Join(root, "application")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "secret"), []byte("original-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	j := newJournal(state, "recovery-fixture")
	if err := j.Snapshot(original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "secret"), []byte("current-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return j, state, original
}

func assertJournalFile(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != expected {
		t.Fatalf("file %s: got %q, want %q, error %v", path, data, expected, err)
	}
}

func TestRestorePreparationFailurePreservesAllCurrentTargets(t *testing.T) {
	for _, failure := range []string{"missing", "partial", "corrupt", "cancelled", "legacy_without_digest"} {
		t.Run(failure, func(t *testing.T) {
			j, _, original := journalFixture(t)
			created := filepath.Join(filepath.Dir(original), "new-resource")
			if err := os.WriteFile(created, []byte("keep-current-resource"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := j.NoteCreated(created); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "missing":
				if err := os.RemoveAll(j.Backups[0].Backup); err != nil {
					t.Fatal(err)
				}
			case "partial":
				if err := os.Remove(filepath.Join(j.Backups[0].Backup, "secret")); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(filepath.Join(j.Backups[0].Backup, "secret"), []byte("wrong"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			case "legacy_without_digest":
				j.Backups[0].Digest = ""
			}
			if err := j.Restore(ctx, Request{}); err == nil {
				t.Fatal("expected preparation failure")
			}
			assertJournalFile(t, filepath.Join(original, "secret"), "current-secret")
			assertJournalFile(t, created, "keep-current-resource")
		})
	}
}

func TestRestoreResumesAfterEachRenameBoundary(t *testing.T) {
	for _, moveNumber := range []int{1, 2} {
		for _, afterRename := range []bool{false, true} {
			t.Run(fmt.Sprintf("move_%d_after_%t", moveNumber, afterRename), func(t *testing.T) {
				j, state, original := journalFixture(t)
				if err := j.prepareRestore(t.Context()); err != nil {
					t.Fatal(err)
				}
				stage := j.RestoreEntries[0].Stage
				moves := 0
				fault := errors.New("synthetic interruption")
				err := j.advanceRestore(t.Context(), 0, func(source, target string) error {
					moves++
					if moves == moveNumber && !afterRename {
						return fault
					}
					if err := os.Rename(source, target); err != nil {
						return err
					}
					if moves == moveNumber {
						return fault
					}
					return nil
				})
				if !errors.Is(err, fault) {
					t.Fatalf("fault boundary not reached: %v", err)
				}
				if moveNumber == 1 && !afterRename {
					assertJournalFile(t, filepath.Join(original, "secret"), "current-secret")
				} else {
					assertJournalFile(t, filepath.Join(stage, "previous", "secret"), "current-secret")
				}
				loaded, err := loadJournal(state, "recovery-fixture")
				if err != nil {
					t.Fatal(err)
				}
				if err := loaded.Restore(t.Context(), Request{}); err != nil {
					t.Fatal(err)
				}
				assertJournalFile(t, filepath.Join(original, "secret"), "original-secret")
				if _, err := os.Stat(stage); !os.IsNotExist(err) {
					t.Fatalf("verified stage not cleaned: %v", err)
				}
				if err := loaded.Restore(t.Context(), Request{}); err != nil {
					t.Fatalf("repeated restore: %v", err)
				}
				assertJournalFile(t, filepath.Join(original, "secret"), "original-secret")
			})
		}
	}
}

func TestRestoreRejectsCorruptRetainedPreparationBeforeMovingCurrent(t *testing.T) {
	j, state, original := journalFixture(t)
	if err := j.prepareRestore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.RestoreEntries[0].Stage, "prepared", "secret"), []byte("corrupt-stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadJournal(state, "recovery-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Restore(t.Context(), Request{}); err == nil {
		t.Fatal("accepted damaged prepared content")
	}
	assertJournalFile(t, filepath.Join(original, "secret"), "current-secret")
}

func TestBackupChildModeFidelity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are not Windows ACL evidence")
	}
	j, _, original := journalFixture(t)
	if err := j.Restore(t.Context(), Request{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(original, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("child mode became %o", info.Mode().Perm())
	}
}

func TestBackupCopyRejectsLinksAndBudgetExhaustion(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("more-than-four-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyBackupFile(t.Context(), source, target, info, 4); err == nil {
		t.Fatal("over-budget copy reported success")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(source, link); err != nil {
		t.Logf("symlink creation unavailable: %v", err)
		return
	}
	if _, err := copyBackupTree(t.Context(), link, filepath.Join(root, "linked-copy")); err == nil {
		t.Fatal("followed a symlink")
	}
}
