package permission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/fs/filelock"
)

type cancelOnMarshal struct{ cancel context.CancelFunc }

func (value cancelOnMarshal) MarshalJSON() ([]byte, error) {
	value.cancel()
	return []byte(`{"value":1}`), nil
}

func TestPolicyCancellationBeforeCommitAndDuringLockWait(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	change := Change{Scope: "global", Mode: ReadOnly, ExpectedRevision: 1}
	if _, err = s.Update(ctx, change); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel: %v", err)
	}
	release, err := filelock.Acquire(context.Background(), filepath.Join(s.root, ".permission.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Update(ctx, change); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("policy wait ignored cancellation")
	}
	release()
	current, err := s.Get(context.Background())
	if err != nil || current.Revision != 1 {
		t.Fatalf("cancelled policy committed: %+v %v", current, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(s.root, "cancelled.json")
	if err = writeJSON(ctx, path, cancelOnMarshal{cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("commit boundary: %v", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancelled commit produced a file: %v", err)
	}
	if _, err = s.Update(context.Background(), change); err != nil {
		t.Fatal(err)
	}
}
