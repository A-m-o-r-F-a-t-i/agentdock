package taskstate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func threadTestTask(t *testing.T) (*Store, Task) {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Create("thread test", "independent recoverable work", []string{"all steps verified"}, []TaskStepInput{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, task
}

func TestThreadForkCheckpointIsolation(t *testing.T) {
	s, task := threadTestTask(t)
	main, err := s.UpdateThread(task.ID, "main", "checkpoint", ThreadInput{CurrentStepID: "a", Summary: "starting", NextAction: "verify A", WorkspaceID: "wsp_repo"})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := s.CreateThread(task.ID, "main", true, ThreadInput{Title: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	if branch.ParentThreadID != "main" || branch.NextAction != main.NextAction || branch.WorkspaceID != "wsp_repo" {
		t.Fatalf("fork lost checkpoint: %+v", branch)
	}
	_, err = s.UpdateThread(task.ID, branch.ID, "checkpoint", ThreadInput{CompletedStepIDs: []string{"a"}, CurrentStepID: "b", Summary: "A verified", NextAction: "finish B"})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.GetThread(task.ID, "main")
	if err != nil || unchanged.Steps[0].Status != StepInProgress || unchanged.NextAction != "verify A" {
		t.Fatalf("main overwritten: %+v %v", unchanged, err)
	}
	switched, err := s.SwitchThread(task.ID, branch.ID)
	if err != nil || switched.Steps[0].Status != StepCompleted || switched.ActiveThreadID != branch.ID {
		t.Fatalf("switch failed: %+v %v", switched, err)
	}
	_, err = s.Checkpoint(task.ID, "b", StepCompleted, "B verified through legacy tool")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.Get(task.ID)
	if err != nil || restored.ActiveThread == nil || restored.ActiveThread.Steps[1].Status != StepCompleted {
		t.Fatalf("resume failed: %+v %v", restored, err)
	}
	main, _ = s.GetThread(task.ID, "main")
	if main.Steps[1].Status != StepPending {
		t.Fatal("legacy checkpoint leaked to main")
	}
}

func TestThreadLifecycleAndClosedHistory(t *testing.T) {
	s, task := threadTestTask(t)
	for _, action := range []string{"block", "resume", "close"} {
		if _, err := s.UpdateThread(task.ID, "main", action, ThreadInput{Summary: action}); err != nil {
			t.Fatal(err)
		}
	}
	closed, err := s.GetThread(task.ID, "main")
	if err != nil || closed.Status != "closed" || closed.ClosedAt == nil {
		t.Fatalf("%+v %v", closed, err)
	}
	if _, err = s.UpdateThread(task.ID, "main", "bind", ThreadInput{}); err == nil {
		t.Fatal("closed branch accepted command")
	}
	if _, err = s.SwitchThread(task.ID, "main"); err == nil {
		t.Fatal("selected closed thread")
	}
	fork, err := s.CreateThread(task.ID, "main", true, ThreadInput{Title: "restart"})
	if err != nil || fork.Status != "open" || fork.ClosedAt != nil {
		t.Fatalf("fork closed history: %+v %v", fork, err)
	}
}

func TestThreadInvalidCheckpointIsAtomic(t *testing.T) {
	s, task := threadTestTask(t)
	_, err := s.UpdateThread(task.ID, "main", "checkpoint", ThreadInput{CompletedStepIDs: []string{"a"}, CurrentStepID: "missing", Summary: "invalid"})
	if err == nil {
		t.Fatal("invalid checkpoint accepted")
	}
	thread, err := s.GetThread(task.ID, "main")
	if err != nil || thread.Steps[0].Status != StepPending {
		t.Fatalf("partial mutation: %+v %v", thread, err)
	}
	if _, err = s.GetThread(task.ID, "../outside"); err == nil {
		t.Fatal("path traversal accepted")
	}
	other, err := s.Create("other", "other", []string{"done"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := s.CreateThread(other.ID, "main", true, ThreadInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetThread(task.ID, branch.ID); err == nil {
		t.Fatal("cross-task thread accepted")
	}
}

func TestLegacyTaskReadDoesNotMigrateAndForkPreservesMain(t *testing.T) {
	s, task := threadTestTask(t)
	// Recreate a genuine pre-thread state file and remove only this test's generated branch directory.
	task.ActiveThreadID = ""
	task.ActiveThread = nil
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), task.ID+".json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(filepath.Join(s.Root(), "threads", task.ID)); err != nil {
		t.Fatal(err)
	}
	read, err := s.Get(task.ID)
	if err != nil || read.ActiveThreadID != "main" || read.ActiveThread == nil {
		t.Fatalf("legacy load: %+v %v", read, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) {
		t.Fatal("read eagerly rewrote legacy task")
	}
	if _, err = os.Stat(filepath.Join(s.Root(), "threads", task.ID)); !os.IsNotExist(err) {
		t.Fatal("read persisted virtual thread")
	}
	branch, err := s.CreateThread(task.ID, "main", true, ThreadInput{Title: "fork"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SwitchThread(task.ID, branch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Checkpoint(task.ID, "a", StepCompleted, "branch only"); err != nil {
		t.Fatal(err)
	}
	main, err := s.GetThread(task.ID, "main")
	if err != nil || main.Steps[0].Status != StepPending {
		t.Fatalf("legacy main lost: %+v %v", main, err)
	}
}

func TestCancelArchiveAndRestore(t *testing.T) {
	s, task := threadTestTask(t)
	if _, err := s.Archive(task.ID, true); err == nil {
		t.Fatal("active task archived")
	}
	cancelled, err := s.Cancel(task.ID, "user cancelled")
	if err != nil || cancelled.Status != StatusCompleted || cancelled.Outcome != "cancelled" || cancelled.CancelledAt == nil {
		t.Fatalf("%+v %v", cancelled, err)
	}
	if _, err = s.Archive(task.ID, true); err != nil {
		t.Fatal(err)
	}
	list, err := s.List("", 20)
	if err != nil || len(list) != 0 {
		t.Fatalf("archived task in default list: %+v %v", list, err)
	}
	history, err := s.ListHistory("", 20, true)
	if err != nil || len(history) != 1 {
		t.Fatalf("archive lost: %+v %v", history, err)
	}
	restored, err := s.Archive(task.ID, false)
	if err != nil || restored.Outcome != "cancelled" || restored.ArchivedAt != nil {
		t.Fatalf("unarchive: %+v %v", restored, err)
	}
}

func TestThreadTransactionRecovery(t *testing.T) {
	s, task := threadTestTask(t)
	task, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	thread := *task.ActiveThread
	thread.Summary = "recovered committed checkpoint"
	thread.NextAction = "verify after restart"
	thread.UpdatedAt = time.Now().UTC()
	task.Summary = thread.Summary
	task.ActiveThread = nil
	tx := threadTransaction{Task: task, Threads: []TaskThread{thread}}
	data, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(s.Root(), ".thread-transaction.json")
	if err = os.WriteFile(journal, data, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.Get(task.ID)
	if err != nil || restored.Summary != thread.Summary || restored.ActiveThread.NextAction != thread.NextAction {
		t.Fatalf("recovery: %+v %v", restored, err)
	}
	if _, err = os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("recovered transaction not removed")
	}
	if err = os.WriteFile(journal, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Get(task.ID); err == nil || !strings.Contains(err.Error(), "transaction") {
		t.Fatalf("damaged transaction silently ignored: %v", err)
	}
}
