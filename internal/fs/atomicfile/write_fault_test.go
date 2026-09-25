package atomicfile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var injectedWriteFailure = errors.New("injected atomic-write failure")

type faultyTemporary struct {
	*os.File
	fault string
}

func (f faultyTemporary) Chmod(mode os.FileMode) error {
	if f.fault == "mode" {
		return injectedWriteFailure
	}
	return f.File.Chmod(mode)
}
func (f faultyTemporary) Write(data []byte) (int, error) {
	if f.fault == "partial" || f.fault == "short" {
		count, err := f.File.Write(data[:len(data)/2])
		if err != nil {
			return count, err
		}
		if f.fault == "short" {
			return count, nil
		}
		return count, injectedWriteFailure
	}
	return f.File.Write(data)
}
func (f faultyTemporary) Sync() error {
	if f.fault == "sync" {
		return injectedWriteFailure
	}
	return f.File.Sync()
}
func (f faultyTemporary) Close() error {
	err := f.File.Close()
	if err == nil && f.fault == "close" {
		return injectedWriteFailure
	}
	return err
}

func TestAtomicWriteFaultStagesKeepCompleteOldOrNewJournal(t *testing.T) {
	for _, stage := range []string{"create", "mode", "secure", "partial", "short", "sync", "close", "replace", "after_replace"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "journal.json")
			backup := filepath.Join(root, "backup")
			if err := os.WriteFile(backup, []byte("recoverable bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			old, _ := json.Marshal(map[string]any{"revision": 1, "backup": backup})
			next, _ := json.Marshal(map[string]any{"revision": 2, "backup": backup})
			if err := Write(path, old, 0600); err != nil {
				t.Fatal(err)
			}
			ops := writeOperations{
				create: func(dir string) (atomicTemporary, error) {
					if stage == "create" {
						return nil, injectedWriteFailure
					}
					file, err := os.CreateTemp(dir, ".agentdock-atomic-*")
					return faultyTemporary{file, stage}, err
				},
				secure: func(path string, mode os.FileMode) error {
					if stage == "secure" {
						return injectedWriteFailure
					}
					return secureWrittenFile(path, mode)
				},
				replace: func(source, target string) error {
					if stage == "replace" {
						return injectedWriteFailure
					}
					if err := replaceFile(source, target); err != nil {
						return err
					}
					if stage == "after_replace" {
						return injectedWriteFailure
					}
					return nil
				},
			}
			if err := writeUsing(path, next, 0600, ops); err == nil {
				t.Fatal("failure did not reach caller")
			}
			actual, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			expected := old
			if stage == "after_replace" {
				expected = next
			}
			if string(actual) != string(expected) {
				t.Fatalf("incomplete or unexpected journal: %s", actual)
			}
			var record struct {
				Revision int
				Backup   string
			}
			if err = json.Unmarshal(actual, &record); err != nil {
				t.Fatal(err)
			}
			retained, err := os.ReadFile(record.Backup)
			if err != nil || string(retained) != "recoverable bytes" {
				t.Fatal("journal points to unavailable backup")
			}
			assertNoAtomicTemps(t, root)
		})
	}
}
