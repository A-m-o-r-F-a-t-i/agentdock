package activity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConversationSourceIsolationAndConservativeFallback(t *testing.T) {
	root := t.TempDir()
	registry, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	a := WithSource(context.Background(), Source{Principal: "alice", Provider: "openai", Namespace: "mcp", HostConversationID: "host-alpha"})
	b := WithSource(context.Background(), Source{Principal: "alice", Provider: "openai", Namespace: "mcp", HostConversationID: "host-beta"})
	ca, err := registry.Resolve(a)
	if err != nil || ca.ID == "" || ca.Attribution != "host_metadata" {
		t.Fatalf("resolve: %+v %v", ca, err)
	}
	for i := 0; i < 20; i++ {
		again, err := registry.Resolve(a)
		if err != nil || again.ID != ca.ID {
			t.Fatalf("split same host: %+v %v", again, err)
		}
	}
	cb, err := registry.Resolve(b)
	if err != nil || cb.ID == ca.ID {
		t.Fatalf("crossed hosts: %+v %v", cb, err)
	}
	noHost := WithSource(context.Background(), Source{Principal: "alice", Provider: "openai", Namespace: "mcp", ConnectionID: "shared-transport"})
	for i := 0; i < 3; i++ {
		unknown, err := registry.Resolve(noHost)
		if err != nil || unknown.ID != "" || unknown.Attribution != "unattributed" {
			t.Fatalf("guessed conversation: %+v %v", unknown, err)
		}
	}
	knownConnection := SourceFromContext(noHost)
	knownConnection.ConnectionIsConversation = true
	fallback, err := registry.Resolve(WithSource(context.Background(), knownConnection))
	if err != nil || fallback.ID == "" || fallback.Attribution != "connection_fallback" {
		t.Fatalf("explicit 1:1 fallback: %+v %v", fallback, err)
	}
	for _, change := range []Source{
		{Principal: "bob", Provider: "openai", Namespace: "mcp", HostConversationID: "host-alpha"},
		{Principal: "alice", Provider: "other", Namespace: "mcp", HostConversationID: "host-alpha"},
		{Principal: "alice", Provider: "openai", Namespace: "stdio", HostConversationID: "host-alpha"},
	} {
		other, err := registry.Resolve(WithSource(context.Background(), change))
		if err != nil || other.ID == ca.ID {
			t.Fatalf("mapping scope collision: %+v %v", other, err)
		}
	}
	other := WithSource(context.Background(), Source{Principal: "bob"})
	if err := registry.Owns(other, ca.ID); !errors.Is(err, ErrConversationOwner) {
		t.Fatalf("owner check: %v", err)
	}
	reopened, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	reconnect := SourceFromContext(a)
	reconnect.ConnectionID = "different-transport"
	restored, err := reopened.Resolve(WithSource(context.Background(), reconnect))
	if err != nil || restored.ID != ca.ID {
		t.Fatalf("reconnect split conversation: %+v %v", restored, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "conversations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "host-alpha") || strings.Contains(string(raw), "alice") {
		t.Fatal("raw external identity persisted")
	}
}

func TestConversationConcurrentMappingAndTrash(t *testing.T) {
	root := t.TempDir()
	r, _ := NewConversationRegistry(root)
	other, _ := NewConversationRegistry(root)
	ctx := WithSource(context.Background(), Source{Principal: "client", Namespace: "chatgpt", HostConversationID: "same"})
	ids := make(chan string, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			registry := r
			if i%2 == 0 {
				registry = other
			}
			c, err := registry.Resolve(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- c.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent resolve minted duplicate conversations")
		}
	}
	if err := r.Link(ctx, first, "tsk_test", "wsp_test"); err != nil {
		t.Fatal(err)
	}
	_, err := r.Manage(ctx, first, MetadataChange{Action: "trash", RetentionDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Resolve(ctx); !errors.Is(err, ErrConversationTrashed) {
		t.Fatal("trashed conversation accepted new calls")
	}
	c, err := r.Manage(ctx, first, MetadataChange{Action: "restore"})
	if err != nil || c.PurgeAfter != nil || len(c.TaskIDs) != 1 {
		t.Fatal("restore lost metadata")
	}
	if _, err = r.Manage(ctx, first, MetadataChange{Action: "delete"}); err == nil {
		t.Fatal("active metadata permanently removed")
	}
}

func TestCallProjectionLifecyclePagingAndRedaction(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, Options{}, "persisted-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conv, _ := NewExecutionID("conv_")
	callID, _ := NewExecutionID("call_")
	binding := Binding{ConversationID: conv, CallID: callID}
	appendEvent := func(kind, status, text string) Event {
		t.Helper()
		e, err := store.Append(ctx, Event{Binding: binding, Kind: kind, Status: status, ToolName: "exec_command", OutputPreview: text})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	appendEvent("call.created", "created", "")
	page, err := store.Calls(ctx, CallQuery{ConversationID: conv})
	if err != nil || len(page.Calls) != 1 {
		t.Fatalf("first projection: %+v %v", page, err)
	}
	cursor := page.LatestSeq
	appendEvent("command.started", "running", "")
	for i := 0; i < 3; i++ {
		appendEvent("command.output", "running", "line persisted-secret-value\n")
	}
	appendEvent("command.completed", "success", "")
	appendEvent("call.started", "running", "")
	call, err := store.Call(ctx, callID)
	if err != nil || call.Status != "succeeded" || strings.Count(call.OutputPreview, "line") != 3 {
		t.Fatalf("call aggregation: %+v %v", call, err)
	}
	if strings.Contains(call.OutputPreview, "persisted-secret-value") {
		t.Fatal("secret reached projection")
	}
	updates, err := store.Calls(ctx, CallQuery{ConversationID: conv, Updates: true, After: cursor})
	if err != nil || len(updates.Calls) != 1 || updates.Calls[0].OutputPreview != "" {
		t.Fatal("updates are not compact call snapshots")
	}
	child, _ := NewExecutionID("call_")
	binding = Binding{ConversationID: conv, CallID: child, ParentCallID: callID}
	appendEvent("call.created", "created", "")
	appendEvent("call.completed", "success", "")
	top, err := store.Calls(ctx, CallQuery{ConversationID: conv, TopLevel: true})
	if err != nil || len(top.Calls) != 1 {
		t.Fatal("child calls duplicated at the top level")
	}
	next, err := store.Calls(ctx, CallQuery{ConversationID: conv, Limit: 1})
	if err != nil || !next.HasMore {
		t.Fatal("first page does not report older calls")
	}
	older, err := store.Calls(ctx, CallQuery{ConversationID: conv, Before: next.NextBefore, Limit: 1})
	if err != nil || len(older.Calls) != 1 || older.Calls[0].CallID == next.Calls[0].CallID {
		t.Fatal("pagination repeated a call")
	}
	reopened, err := New(root, Options{}, "persisted-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.Call(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(call)
	b, _ := json.Marshal(restored)
	if string(a) != string(b) {
		t.Fatalf("replayed projection drifted\n%s\n%s", a, b)
	}
}

func TestCallProjectionKeepsUnknownSourceAndActualRetries(t *testing.T) {
	store, _ := New(t.TempDir(), Options{})
	ctx := context.Background()
	first, _ := NewExecutionID("call_")
	second, _ := NewExecutionID("call_")
	for _, b := range []Binding{{CallID: first}, {CallID: second, RetryOfCallID: first}} {
		if _, err := store.Append(ctx, Event{Binding: b, Kind: "call.created", ToolName: "read_file", Title: "same.txt"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(ctx, Event{Binding: b, Kind: "call.completed", Status: "failed", ToolName: "read_file"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Calls(ctx, CallQuery{Unattributed: true})
	if err != nil || len(page.Calls) != 2 || page.Calls[0].RetryOfCallID != first {
		t.Fatal("same-text retries were deduplicated or unknown source hidden")
	}
	for _, c := range page.Calls {
		if c.ConversationID != "" || c.TaskID != "" {
			t.Fatal("unknown call received a false parent")
		}
	}
}
