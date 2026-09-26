package activity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestResourceMutexCancellationDoesNotLoseToken(t *testing.T) {
	var mutex resourceMutex
	mutex.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mutex.LockContext(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation while owner remains locked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not return")
	}
	mutex.Unlock()
	// A cancelled context must not consume even an immediately available token.
	if err := mutex.LockContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context acquired lock: %v", err)
	}
	live, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := mutex.LockContext(live); err != nil {
		t.Fatalf("cancelled waiter leaked token: %v", err)
	}
	mutex.Unlock()
}

func TestResourceMutexConcurrentHandoffsRemainExclusive(t *testing.T) {
	var mutex resourceMutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value := 0
	for worker := 0; worker < 40; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for round := 0; round < 100; round++ {
				if err := mutex.LockContext(ctx); err != nil {
					t.Errorf("waiter did not make progress: %v", err)
					return
				}
				value++ // The race detector verifies mutual exclusion as well.
				mutex.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if value != 4000 {
		t.Fatalf("lost handoffs: %d", value)
	}
}
