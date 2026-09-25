package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquirePreCancelledNeverCreatesOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-created", "state.lock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 1000 {
		if release, err := Acquire(ctx, path); release != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled acquire succeeded: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("cancelled acquire created a directory: %v", err)
	}
	release, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
}

func TestAcquireCancellationPreservesOwnerAndReleasesLocalReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	release, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before, err := os.ReadDir(path)
	if err != nil || len(before) != 1 {
		t.Fatal("missing owner")
	}
	for range 10 {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			unlock, err := Acquire(ctx, path)
			if unlock != nil {
				unlock()
			}
			done <- err
		}()
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("lock wait ignored cancellation")
		}
	}
	after, err := os.ReadDir(path)
	if err != nil || len(after) != 1 || after[0].Name() != before[0].Name() {
		t.Fatal("cancelled waiter removed another owner's lock")
	}
	release()
	localGates.Lock()
	remaining := len(localGates.entries)
	localGates.Unlock()
	if remaining != 0 {
		t.Fatalf("local references leaked: %d", remaining)
	}
}
