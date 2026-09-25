package activity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitAppendState(t *testing.T, s *Store, check func(AppendStatistics) bool) AppendStatistics {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		stats := s.AppendStatistics()
		if check(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("append state did not converge: %+v", stats)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAppendBudgetCountBytesCancellationAndRecovery(t *testing.T) {
	for _, options := range []Options{{AppendEvents: 3, AppendBytes: 20 * MaxEventBytes}, {AppendEvents: 20, AppendBytes: 3 * MaxEventBytes}} {
		s, err := New(t.TempDir(), options)
		if err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		locked := true
		func() {
			defer func() {
				if locked {
					s.mu.Unlock()
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			results := make(chan error, 40)
			for i := 0; i < 3; i++ {
				go func(i int) {
					_, err := s.Append(ctx, Event{Kind: "call.created", Binding: Binding{CallID: fmt.Sprintf("budget_%d", i)}})
					results <- err
				}(i)
			}
			waitAppendState(t, s, func(stats AppendStatistics) bool { return stats.ReservedEvents == 3 })
			for range 37 {
				go func() { _, err := s.Append(ctx, Event{Kind: "call.created"}); results <- err }()
			}
			for range 37 {
				select {
				case err := <-results:
					if !errors.Is(err, ErrAppendCapacity) {
						t.Fatalf("overflow: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("overflow did not return a bounded refusal")
				}
			}
			stats := s.AppendStatistics()
			if stats.PeakEvents != 3 || stats.PeakBytes != 3*MaxEventBytes || stats.QueuedEvents > 3 {
				t.Fatalf("capacity exceeded: %+v", stats)
			}
			cancel()
			for range 3 {
				select {
				case err := <-results:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("queued cancellation did not release")
				}
			}
			waitAppendState(t, s, func(stats AppendStatistics) bool { return stats.ReservedEvents == 0 && stats.QueuedEvents == 0 })
			// Repeated cancel while the writer remains frozen must not accumulate
			// queue nodes after their capacity tickets were released.
			for range 100 {
				child, stop := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { _, err := s.Append(child, Event{Kind: "call.created"}); done <- err }()
				waitAppendState(t, s, func(stats AppendStatistics) bool { return stats.ReservedEvents == 1 })
				stop()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			s.mu.Unlock()
			locked = false
			written, err := s.Append(context.Background(), Event{Kind: "call.created", Binding: Binding{CallID: "after_cancel"}})
			if err != nil || written.Seq != 1 {
				t.Fatalf("cancelled events committed: %+v %v", written, err)
			}
			stats = waitAppendState(t, s, func(stats AppendStatistics) bool { return stats.ReservedEvents == 0 })
			// Coarse native clocks can legitimately measure a fast batch as zero.
			// The count verifies instrumentation without inventing elapsed time.
			if stats.CommittedEvents != 1 || stats.CancelledEvents != 103 || stats.RejectedEvents != 37 || stats.PersistBatches == 0 {
				t.Fatalf("incorrect accounting: %+v", stats)
			}
		}()
	}
}

func TestAppendBudgetReservedCompletionAndBatchShareCapacity(t *testing.T) {
	s, err := New(t.TempDir(), Options{AppendEvents: 3, AppendBytes: 3 * MaxEventBytes})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := s.ReserveAppend(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer completion.Close()
	others, err := s.ReserveAppend(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer others.Close()
	if _, err = s.AppendBatch(context.Background(), []Event{{Kind: "call.created"}}); !errors.Is(err, ErrAppendCapacity) {
		t.Fatalf("batch bypassed capacity: %v", err)
	}
	event, err := completion.Append(context.Background(), Event{Kind: "call.completed", Status: "succeeded", Binding: Binding{CallID: "reserved_result"}})
	if err != nil || event.Seq == 0 {
		t.Fatalf("completion lost to saturation: %v", err)
	}
	others.Close()
	completion.Close()
	if _, err = completion.Append(context.Background(), Event{Kind: "call.created"}); err == nil {
		t.Fatal("closed completion was reused")
	}
	batch, err := s.AppendBatch(context.Background(), []Event{{Kind: "call.created", Binding: Binding{CallID: "batch_result"}}, {Kind: "call.completed", Status: "succeeded", Binding: Binding{CallID: "batch_result"}}})
	if err != nil || len(batch) != 2 || batch[1].Seq != batch[0].Seq+1 {
		t.Fatalf("ordered group failed: %v", err)
	}
	if stats := s.AppendStatistics(); stats.ReservedEvents != 0 || stats.ReservedBytes != 0 || stats.CommittedEvents != 3 {
		t.Fatalf("batch leaked capacity: %+v", stats)
	}
}

func TestAppendBudgetInvalidPreparationDoesNotPoisonNeighbours(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Append(context.Background(), Event{Kind: "call.created", Binding: Binding{CallID: fmt.Sprintf("neighbour_%d", i)}})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	if _, err = s.Append(context.Background(), Event{Kind: "call.created", OutputPreview: strings.Repeat("x", (1<<20)+1)}); err == nil {
		t.Fatal("unbounded raw preview accepted")
	}
	if _, err = s.Append(context.Background(), Event{Kind: "call.created", Binding: Binding{SourceOwnerKey: strings.Repeat("x", MaxEventBytes)}}); err == nil {
		t.Fatal("oversized serialized envelope entered queue")
	}
	if _, err = s.AppendBatch(context.Background(), []Event{{Kind: "call.created"}, {Kind: "not-a-kind"}}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	wg.Wait()
	stats := waitAppendState(t, s, func(stats AppendStatistics) bool { return stats.ReservedEvents == 0 })
	if stats.CommittedEvents != 20 {
		t.Fatalf("invalid producer poisoned valid events: %+v", stats)
	}
}
