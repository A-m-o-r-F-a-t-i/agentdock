package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalFailedPublicationDoesNotCommitMemory(t *testing.T) {
	j := newJournal(t.TempDir(), "atomic-test")
	if err := j.NoteService(journalService{Name: "original"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(j.Dir, "journal.json")
	previous, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// An existing non-empty directory cannot be replaced by the atomic writer.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "previous.json"), previous, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.NoteCreated("must-not-commit"); err == nil {
		t.Fatal("expected publication failure")
	}
	if len(j.Created) != 0 {
		t.Fatal("failed append committed in memory")
	}
	if err := j.NoteService(journalService{Name: "must-not-commit"}); err == nil {
		t.Fatal("expected publication failure")
	}
	if len(j.Services) != 1 {
		t.Fatal("failed service append committed")
	}
	if err := j.updateService("original", func(s *journalService) { s.StartedByUs = true }); err == nil {
		t.Fatal("expected publication failure")
	}
	if j.Services[0].StartedByUs {
		t.Fatal("failed service mutation changed original slice")
	}
	retained, err := os.ReadFile(filepath.Join(path, "previous.json"))
	if err != nil || string(retained) != string(previous) {
		t.Fatalf("previous journal not preserved: %v", err)
	}
}

func TestJournalAtomicPublicationRemainsReadable(t *testing.T) {
	j := newJournal(t.TempDir(), "readable")
	for range 25 {
		if err := j.NoteCreated(filepath.Join(j.Dir, "fixture")); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(j.Dir, "journal.json"))
		if err != nil {
			t.Fatal(err)
		}
		var actual rollbackJournal
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatal(err)
		}
		if len(actual.Created) != len(j.Created) {
			t.Fatal("disk and memory differ")
		}
	}
}
