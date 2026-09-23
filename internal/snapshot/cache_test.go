package snapshot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func await(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not converge")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestTenWaitersBuildOnceAndCacheIsComplete(t *testing.T) {
	c := New[string](2, time.Second, time.Minute)
	defer c.Close()
	gate := make(chan struct{})
	var builds atomic.Int32
	build := func(ctx context.Context) (string, error) {
		builds.Add(1)
		select {
		case <-gate:
			return "complete rules and tools", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	var wg sync.WaitGroup
	results := make(chan string, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, _, err := c.Get(t.Context(), "same", build)
			if err != nil {
				t.Error(err)
			}
			results <- value
		}()
	}
	await(t, func() bool { return c.Stats().Shared == 9 })
	close(gate)
	wg.Wait()
	close(results)
	if builds.Load() != 1 {
		t.Fatalf("builders=%d", builds.Load())
	}
	for value := range results {
		if value != "complete rules and tools" {
			t.Fatalf("incomplete %q", value)
		}
	}
	value, info, err := c.Get(t.Context(), "same", build)
	if err != nil || !info.CacheHit || value != "complete rules and tools" {
		t.Fatalf("cached complete response: %q %#v %v", value, info, err)
	}
}
func TestFirstWaiterCancelDoesNotCancelOthers(t *testing.T) {
	c := New[int](2, time.Second, time.Minute)
	defer c.Close()
	ctx, cancel := context.WithCancel(t.Context())
	gate := make(chan struct{})
	started := make(chan struct{})
	first := make(chan error, 1)
	second := make(chan error, 1)
	build := func(ctx context.Context) (int, error) {
		close(started)
		select {
		case <-gate:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	go func() { _, _, err := c.Get(ctx, "same", build); first <- err }()
	<-started
	go func() {
		value, _, err := c.Get(t.Context(), "same", build)
		if value != 42 && err == nil {
			err = errors.New("incomplete second response")
		}
		second <- err
	}()
	await(t, func() bool { return c.Stats().Shared == 1 })
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(gate)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}
func TestLastWaiterCancelStopsBuilderAndAllowsNoOverlappingReplacement(t *testing.T) {
	c := New[int](2, time.Second, time.Minute)
	defer c.Close()
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	stopped := make(chan struct{})
	cleanup := make(chan struct{})
	var builds atomic.Int32
	build := func(ctx context.Context) (int, error) {
		builds.Add(1)
		close(started)
		<-ctx.Done()
		close(stopped)
		<-cleanup
		return 0, ctx.Err()
	}
	first := make(chan error, 1)
	go func() { _, _, err := c.Get(ctx, "x", build); first <- err }()
	<-started
	cancel()
	<-first
	<-stopped
	secondCtx, secondCancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer secondCancel()
	_, _, _ = c.Get(secondCtx, "x", build)
	if builds.Load() != 1 || c.Stats().InFlight != 1 {
		t.Fatalf("overlapping replacement: %#v", c.Stats())
	}
	close(cleanup)
	waitCtx, waitCancel := context.WithTimeout(t.Context(), time.Second)
	defer waitCancel()
	if err := c.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
}
func TestBuilderBudgetInvalidationAndCapacity(t *testing.T) {
	c := New[int](2, 25*time.Millisecond, time.Minute)
	defer c.Close()
	_, _, err := c.Get(t.Context(), "slow", func(ctx context.Context) (int, error) { <-ctx.Done(); return 0, ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "c"} {
		if _, _, err := c.Get(t.Context(), key, func(context.Context) (int, error) { return 7, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if c.Stats().Entries != 2 {
		t.Fatal(c.Stats())
	}
	c.Invalidate()
	if c.Stats().Entries != 0 {
		t.Fatal(c.Stats())
	}
}
func TestScopeKeysNeverSharePrivateRuleBodies(t *testing.T) {
	c := New[string](8, time.Second, time.Minute)
	defer c.Close()
	for _, scope := range []string{"workspace-A/identity-A", "workspace-B/identity-B"} {
		value, _, err := c.Get(t.Context(), scope, func(context.Context) (string, error) { return scope, nil })
		if err != nil || value != scope {
			t.Fatalf("scope mix: %q %v", value, err)
		}
	}
}
