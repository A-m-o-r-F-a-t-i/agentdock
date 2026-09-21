package activity

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func Test113BatchNamePreservesNewerManualNamesAndDeletion(t *testing.T) {
	r, err := NewConversationRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	create := func(host string) Conversation {
		t.Helper()
		item, err := r.Resolve(WithSource(context.Background(), Source{Principal: "test", HostConversationID: host}))
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	a, b := create("batch-a"), create("batch-b")
	proposals := map[string]AutomaticTitle{a.ID: {Title: "Old proposal", Source: "task"}, b.ID: {Title: "Old deleted proposal", Source: "task"}}
	if _, err := r.Manage(context.Background(), a.ID, MetadataChange{Action: "rename", Title: "用户新名称"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"trash", "delete"} {
		if _, err := r.Manage(context.Background(), b.ID, MetadataChange{Action: action}); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := r.AutoNameBatch(context.Background(), proposals)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[a.ID].Title != "用户新名称" || resolved[a.ID].TitleSource != "manual" {
		t.Fatalf("Stale proposal overrode a newer action: %+v", resolved)
	}
	reopened, err := NewConversationRegistry(r.root)
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := reopened.IsDeleted(context.Background(), b.ID); err != nil || !deleted {
		t.Fatal("Deletion was lost", err)
	}
}

func Test113CancelledBatchNamingDoesNotWrite(t *testing.T) {
	r, err := NewConversationRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, err := r.Resolve(WithSource(context.Background(), Source{HostConversationID: "cancelled-batch"}))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.root, "conversations.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.AutoNameBatch(ctx, map[string]AutomaticTitle{item.ID: {Title: "Not saved", Source: "task"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cancelled update returned %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("Cancelled request changed registry", err)
	}
}
