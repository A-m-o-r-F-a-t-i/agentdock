package activity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConvergeBindingPreservesRevisionAndAuthority(t *testing.T) {
	r, err := NewConversationRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "owner", HostConversationID: "context"})
	conversation, err := r.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := ConversationState{WorkspaceID: "wsp_0123456789abcdef"}
	first, changed, err := r.ConvergeBinding(ctx, conversation.ID, conversation.State.BindingRevision, next)
	if err != nil || !changed {
		t.Fatalf("first commit: %+v %v", first, err)
	}
	second, changed, err := r.ConvergeBinding(ctx, conversation.ID, conversation.State.BindingRevision, next)
	if err != nil || changed || second != first {
		t.Fatalf("identical target was rewritten: %+v %v", second, err)
	}
	other := WithSource(context.Background(), Source{Principal: "other"})
	if _, _, err = r.ConvergeBinding(other, conversation.ID, first.BindingRevision, next); !errors.Is(err, ErrConversationOwner) {
		t.Fatalf("same target bypassed ownership: %v", err)
	}
	if _, err = r.SetTerminated(WithLocalManagement(ctx), conversation.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.ConvergeBinding(ctx, conversation.ID, first.BindingRevision, next); !errors.Is(err, ErrConversationTerminated) {
		t.Fatalf("same target bypassed termination: %v", err)
	}
}

func TestConvergeBindingFailedStorageReturnsNoCommittedState(t *testing.T) {
	root := t.TempDir()
	r, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "owner", HostConversationID: "storage"})
	conversation, err := r.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "conversations.json")
	if err = os.Rename(file, file+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	state, changed, err := r.ConvergeBinding(ctx, conversation.ID, conversation.State.BindingRevision, ConversationState{WorkspaceID: "wsp_0123456789abcdef"})
	if err == nil || changed || state != (ConversationState{}) {
		t.Fatalf("storage failure returned committed state: %+v %v", state, err)
	}
}
