package snapshot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInvalidationSeparatesFlightsAndOldResultCannotOverwrite(t *testing.T) {
	c := New[string](4, time.Second, time.Minute)
	defer c.Close()
	started, release := make(chan struct{}), make(chan struct{})
	old := make(chan string, 1)
	go func() {
		value, _, err := c.Get(t.Context(), "same", func(ctx context.Context) (string, error) {
			close(started)
			select {
			case <-release:
				return "old-generation", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
		if err != nil {
			old <- err.Error()
		} else {
			old <- value
		}
	}()
	<-started
	c.Invalidate()
	value, info, err := c.Get(t.Context(), "same", func(context.Context) (string, error) { return "new-generation", nil })
	if err != nil || value != "new-generation" || info.SharedBuild {
		t.Fatalf("new generation joined old flight: %q %#v %v", value, info, err)
	}
	close(release)
	if value := <-old; value != "old-generation" {
		t.Fatalf("old waiter contract: %q", value)
	}
	value, info, err = c.Get(t.Context(), "same", func(context.Context) (string, error) { t.Error("unexpected rebuild"); return "", nil })
	if err != nil || value != "new-generation" || !info.CacheHit {
		t.Fatalf("old build overwrote cache: %q %#v %v", value, info, err)
	}
	if c.Stats().InFlight != 0 || c.Stats().Builds != 2 {
		t.Fatal(c.Stats())
	}
}

func TestCloseCancelsFlightsAcrossGenerations(t *testing.T) {
	c := New[int](2, time.Second, time.Minute)
	started, finished := make(chan struct{}, 2), make(chan error, 2)
	build := func(ctx context.Context) (int, error) { started <- struct{}{}; <-ctx.Done(); return 0, ctx.Err() }
	go func() { _, _, err := c.Get(t.Context(), "same", build); finished <- err }()
	<-started
	c.Invalidate()
	go func() { _, _, err := c.Get(t.Context(), "same", build); finished <- err }()
	<-started
	if c.Stats().InFlight != 2 {
		t.Fatal(c.Stats())
	}
	c.Close()
	for range 2 {
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if err := c.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Get(t.Context(), "same", build); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
