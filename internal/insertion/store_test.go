package insertion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Store, *time.Time, Target) {
	t.Helper()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store, err := New(t.TempDir(), "run_a", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return store, &now, Target{Owner: "owner_a", Conversation: "conv_a", Task: "tsk_a", Thread: "main"}
}
func enqueue(t *testing.T, s *Store, target Target, submission string) Item {
	t.Helper()
	item, err := s.Add(context.Background(), target, submission, "补充任务要求")
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestInsertionExpiryBoundaries(t *testing.T) {
	for _, test := range []struct {
		name  string
		delay time.Duration
		want  int
	}{{"299.999", 299999 * time.Millisecond, 1}, {"300", 300 * time.Second, 0}, {"300.001", 300001 * time.Millisecond, 0}} {
		t.Run(test.name, func(t *testing.T) {
			s, now, target := fixture(t)
			created := enqueue(t, s, target, "request_a")
			*now = now.Add(test.delay)
			items, err := s.Reserve(context.Background(), target, "call_new", *now)
			if err != nil || len(items) != test.want {
				t.Fatalf("reserve=%v err=%v", items, err)
			}
			listed, err := s.List(context.Background(), target.Owner, target.Conversation)
			if err != nil {
				t.Fatal(err)
			}
			if !listed[0].ExpiresAt.Equal(created.CreatedAt.Add(Lifetime)) {
				t.Fatal("deadline changed")
			}
			if test.want == 0 && listed[0].Status != "expired" {
				t.Fatalf("status=%s", listed[0].Status)
			}
		})
	}
}
func TestInsertionOnlyNextNewCallAndLateResult(t *testing.T) {
	s, now, target := fixture(t)
	created := enqueue(t, s, target, "request_a")
	if items, err := s.Reserve(context.Background(), target, "call_old", now.Add(-time.Second)); err != nil || len(items) != 0 {
		t.Fatalf("old call consumed message: %v %v", items, err)
	}
	*now = now.Add(299 * time.Second)
	if items, err := s.Reserve(context.Background(), target, "call_new", *now); err != nil || len(items) != 1 {
		t.Fatalf("new claim: %v %v", items, err)
	}
	*now = now.Add(10 * time.Minute)
	if err := s.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := s.Finish(context.Background(), "call_new", true)
	if err != nil || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("late result lost: %v %v", items, err)
	}
	if duplicate, err := s.Finish(context.Background(), "call_new", true); err != nil || len(duplicate) != 0 {
		t.Fatal("duplicate attachment")
	}
}
func TestInsertionConcurrentClaimsAreSingleOwner(t *testing.T) {
	s, now, target := fixture(t)
	enqueue(t, s, target, "request_a")
	*now = now.Add(time.Second)
	var count atomic.Int32
	var workers sync.WaitGroup
	for index := 0; index < 32; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			items, err := s.Reserve(context.Background(), target, "call_parallel", *now)
			if err != nil {
				t.Error(err)
			}
			count.Add(int32(len(items)))
		}()
	}
	workers.Wait()
	if count.Load() != 1 {
		t.Fatalf("claim count=%d", count.Load())
	}
}
func TestInsertionIsolationTaskSwitchAndTermination(t *testing.T) {
	s, now, target := fixture(t)
	enqueue(t, s, target, "request_a")
	*now = now.Add(time.Second)
	for _, wrong := range []Target{{Owner: "other", Conversation: target.Conversation, Task: target.Task, Thread: target.Thread}, {Owner: target.Owner, Conversation: "conv_b", Task: target.Task, Thread: target.Thread}} {
		if items, err := s.Reserve(context.Background(), wrong, "call_wrong", *now); err != nil || len(items) != 0 {
			t.Fatal("cross-target claim")
		}
	}
	changed := target
	changed.Task = "tsk_b"
	if items, err := s.Reserve(context.Background(), changed, "call_switched", *now); err != nil || len(items) != 0 {
		t.Fatal("task switch consumed message")
	}
	items, _ := s.List(context.Background(), target.Owner, target.Conversation)
	if items[0].Status != "target_changed" {
		t.Fatal("task change not paused")
	}
	if err := s.Cancel(context.Background(), target.Owner, target.Conversation, "", true); err != nil {
		t.Fatal(err)
	}
	items, _ = s.List(context.Background(), target.Owner, target.Conversation)
	if items[0].Status != "cancelled" {
		t.Fatal("termination failed")
	}
}
func TestInsertionIdempotencyAndRestartDeadline(t *testing.T) {
	s, now, target := fixture(t)
	first := enqueue(t, s, target, "request_a")
	*now = now.Add(2 * time.Minute)
	again := enqueue(t, s, target, "request_a")
	if first.ID != again.ID || !first.ExpiresAt.Equal(again.ExpiresAt) {
		t.Fatal("retry duplicated or extended insertion")
	}
	if _, err := s.Add(context.Background(), target, "request_a", "different"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting retry=%v", err)
	}
	reloaded, err := New(s.root, "run_b", func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	*now = first.ExpiresAt
	items, err := reloaded.List(context.Background(), target.Owner, target.Conversation)
	if err != nil || items[0].Status != "expired" {
		t.Fatalf("restart expiry=%v %v", items, err)
	}
}
func TestInsertionUnknownDeliveryNeverReplayed(t *testing.T) {
	s, now, target := fixture(t)
	enqueue(t, s, target, "request_a")
	*now = now.Add(time.Second)
	_, err := s.Reserve(context.Background(), target, "call_lost", *now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Cancel(context.Background(), target.Owner, target.Conversation, "missing", false); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing cancellation accepted")
	}
	items, _ := s.List(context.Background(), target.Owner, target.Conversation)
	if err = s.Cancel(context.Background(), target.Owner, target.Conversation, items[0].ID, false); err == nil {
		t.Fatal("claimed message reported as withdrawn")
	}
	reloaded, err := New(s.root, "run_b", func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	items, _ = reloaded.List(context.Background(), target.Owner, target.Conversation)
	if items[0].Status != "delivery_unknown" {
		t.Fatal("lost claim not marked unknown")
	}
	items, err = reloaded.Reserve(context.Background(), target, "call_retry", *now)
	if err != nil || len(items) != 0 {
		t.Fatal("unknown delivery replayed")
	}
}
func TestInsertionBoundsAndCorruptStorePreservation(t *testing.T) {
	s, _, target := fixture(t)
	for _, text := range []string{"   ", strings.Repeat("a", MaxTextBytes+1), string([]byte{0xff})} {
		if _, err := s.Add(context.Background(), target, "invalid", text); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
	for index := 0; index < MaxPending; index++ {
		enqueue(t, s, target, "id_"+strings.Repeat("a", index+1))
	}
	if _, err := s.Add(context.Background(), target, "overflow", "x"); !errors.Is(err, ErrLimit) {
		t.Fatalf("queue bound=%v", err)
	}
	path := filepath.Join(s.root, "queue.json")
	broken := []byte("{invalid")
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background(), target.Owner, target.Conversation); err == nil {
		t.Fatal("corrupt store accepted")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(broken) {
		t.Fatal("corrupt store overwritten")
	}
}

func TestInsertionArrivalBeforeDeadlineSurvivesConcurrentExpirySweep(t *testing.T) {
	for _, mode := range []string{"pending", "task_changed", "cancelled", "late"} {
		t.Run(mode, func(t *testing.T) {
			s, now, target := fixture(t)
			item := enqueue(t, s, target, "deadline_race")
			received := item.ExpiresAt.Add(-time.Millisecond)
			if mode == "task_changed" {
				other := target
				other.Task = "tsk_other"
				*now = now.Add(time.Second)
				if _, err := s.Reserve(context.Background(), other, "call_other", *now); err != nil {
					t.Fatal(err)
				}
			}
			*now = item.ExpiresAt.Add(time.Millisecond)
			if err := s.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "cancelled" {
				if err := s.Cancel(context.Background(), target.Owner, target.Conversation, item.ID, false); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "late" {
				received = *now
			}
			items, err := s.Reserve(context.Background(), target, "call_delayed_attribution", received)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "pending" {
				want = 1
			}
			if len(items) != want {
				t.Fatalf("mode=%s claimed=%d", mode, len(items))
			}
		})
	}
}
