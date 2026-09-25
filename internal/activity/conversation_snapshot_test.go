package activity

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/fs/filelock"
)

func TestConversationSnapshotWarmReadsAvoidRepeatedDecode(t *testing.T) {
	root := t.TempDir()
	writer, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "owner", HostConversationID: "cached"})
	item, err := writer.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if _, err = r.Get(ctx, item.ID); err != nil {
			t.Fatal(err)
		}
		if err = r.Owns(ctx, item.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = r.Resolve(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if count := r.decodeCount.Load(); count != 1 {
		t.Fatalf("same version decoded %d times", count)
	}
	if _, err = writer.Manage(ctx, item.ID, MetadataChange{Action: "rename", Title: "new title"}); err != nil {
		t.Fatal(err)
	}
	updated, err := r.Get(ctx, item.ID)
	if err != nil || updated.Title != "new title" || r.decodeCount.Load() != 2 {
		t.Fatalf("external update invisible: %+v %v", updated, err)
	}
}

func TestConversationSnapshotDetectsInPlaceChangeWithSameMetadata(t *testing.T) {
	root := t.TempDir()
	r, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "owner", HostConversationID: "inplace"})
	item, err := r.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Manage(ctx, item.ID, MetadataChange{Action: "rename", Title: "title-old"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "conversations.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replaced := bytes.ReplaceAll(data, []byte("title-old"), []byte("title-new"))
	if bytes.Equal(data, replaced) {
		t.Fatal("fixture has no replacement")
	}
	if err = os.WriteFile(path, replaced, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	updated, err := r.Get(ctx, item.ID)
	if err != nil || updated.Title != "title-new" {
		t.Fatalf("same-metadata edit was missed: %+v %v", updated, err)
	}
	if err = os.WriteFile(path, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Get(ctx, item.ID); err == nil {
		t.Fatal("damaged registry served stale authority")
	}
}

func TestConversationSnapshotCancelledMutationAndReturnedCopies(t *testing.T) {
	r, err := NewConversationRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "owner", HostConversationID: "cancelled"})
	item, err := r.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	defer cancel()
	err = r.state(cancelled, func(state *conversationState) (bool, error) {
		record := state.Items[item.ID]
		record.Title = "uncommitted"
		state.Items[item.ID] = record
		cancel()
		return true, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("commit cancellation: %v", err)
	}
	got, err := r.Get(ctx, item.ID)
	if err != nil || got.Title != item.Title {
		t.Fatalf("uncommitted cache leaked: %+v %v", got, err)
	}
	if _, err = r.SetTerminated(WithLocalManagement(ctx), item.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err = r.Get(ctx, item.ID)
	if err != nil || got.TerminatedAt == nil {
		t.Fatal("missing termination")
	}
	*got.TerminatedAt = time.Time{}
	local, _, err := r.LocalTarget(WithLocalManagement(ctx), item.ID)
	if err != nil || local.TerminatedAt.IsZero() {
		t.Fatal("returned pointer mutated cache")
	}
	*local.TerminatedAt = time.Time{}
	got, err = r.Get(ctx, item.ID)
	if err != nil || got.TerminatedAt.IsZero() {
		t.Fatal("local target leaked mutable cache")
	}
}

func TestConversationSnapshotWaitCancellation(t *testing.T) {
	r, err := NewConversationRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "owner", HostConversationID: "wait"})
	item, err := r.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release, err := filelock.Acquire(ctx, filepath.Join(r.root, ".conversations.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Get(ctx, item.ID); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("registry wait ignored cancellation")
	}
	release()
	if _, err = r.Get(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
}
