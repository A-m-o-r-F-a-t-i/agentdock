package taskstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func completedNotificationTask(t *testing.T, s *Store) Task {
	t.Helper()
	task, err := s.Create("通知任务", "验证任务成功通知", []string{"verified"}, []TaskStepInput{{ID: "verify", Title: "Verify"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Checkpoint(task.ID, "verify", StepCompleted, "verified"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinalReview(task.ID, FinalReviewInput{Status: FinalReviewPass, Summary: "verified", VerifiedFacts: []string{"verified"}}); err != nil {
		t.Fatal(err)
	}
	task, err = s.CompleteWithSource(task.ID, "conv_test", "main")
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func TestCompletionNotificationPrimaryStateAndPersistentDeduplication(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	task := completedNotificationTask(t, s)
	// This Store has no secondary activity journal. Primary completion is sufficient.
	now := task.CompletedAt.Add(time.Second)
	items, err := s.ClaimCompletionNotifications(context.Background(), 3, now)
	if err != nil || len(items) != 1 || items[0].TaskID != task.ID || items[0].ConversationID != "conv_test" || items[0].ThreadID != "main" {
		t.Fatalf("notifications=%+v err=%v", items, err)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	items, err = reopened.ClaimCompletionNotifications(context.Background(), 3, now)
	if err != nil || len(items) != 0 {
		t.Fatalf("replayed notifications=%+v err=%v", items, err)
	}
}
func TestCompletionNotificationDoesNotReplayLegacyCancelledOrExpired(t *testing.T) {
	for _, kind := range []string{"legacy", "cancelled", "expired"} {
		t.Run(kind, func(t *testing.T) {
			s, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			task := completedNotificationTask(t, s)
			now := task.CompletedAt.Add(time.Second)
			if kind == "legacy" {
				task.Completion = nil
			}
			if kind == "cancelled" {
				task.Outcome = "cancelled"
				task.CancelledAt = task.CompletedAt
			}
			if kind == "expired" {
				now = task.CompletedAt.Add(CompletionNotificationAge + time.Second)
			}
			data, err := json.Marshal(task)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(s.root, task.ID+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			items, err := s.ClaimCompletionNotifications(context.Background(), 3, now)
			if err != nil || len(items) != 0 {
				t.Fatalf("%s emitted=%+v err=%v", kind, items, err)
			}
		})
	}
}
func TestCompletionNotificationConcurrentObserversClaimOnce(t *testing.T) {
	root := t.TempDir()
	a, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	task := completedNotificationTask(t, a)
	now := task.CompletedAt.Add(time.Second)
	var wg sync.WaitGroup
	var count atomic.Int32
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			s := a
			if index%2 == 1 {
				s = b
			}
			items, err := s.ClaimCompletionNotifications(context.Background(), 3, now)
			if err != nil {
				t.Error(err)
			}
			count.Add(int32(len(items)))
		}(index)
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("notifications=%d", count.Load())
	}
}
