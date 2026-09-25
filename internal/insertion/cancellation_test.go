package insertion

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/fs/filelock"
)

func TestInsertionCancellationReleasesWaitAndDoesNotCommit(t *testing.T) {
	s, err := New(t.TempDir(), "run", nil)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Owner: "owner", Conversation: "conv_fixture"}
	item, err := s.Add(context.Background(), target, "first", "original")
	if err != nil {
		t.Fatal(err)
	}
	release, err := filelock.Acquire(context.Background(), filepath.Join(s.root, ".queue.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Add(ctx, target, "cancelled", "never committed"); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("insertion wait ignored cancellation")
	}
	release()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	err = s.change(ctx, func(state *diskState, _ time.Time) (bool, error) {
		state.Items[0].Text = "uncommitted"
		cancel()
		return true, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("commit cancellation: %v", err)
	}
	items, err := s.List(context.Background(), target.Owner, target.Conversation)
	if err != nil || len(items) != 1 || items[0].ID != item.ID || items[0].Text != "original" {
		t.Fatalf("cancel changed insertion: %+v %v", items, err)
	}
}
