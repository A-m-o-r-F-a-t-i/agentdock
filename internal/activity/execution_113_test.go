package activity

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func Test113ConversationTitlePriorities(t *testing.T) {
	registry, err := NewConversationRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "test", Namespace: "mcp", HostConversationID: "title"})
	item, err := registry.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ title, source, expected string }{
		{"项目甲 · 09-21 00:00", "fallback", "项目甲 · 09-21 00:00"},
		{"读取接口定义", "operation", "读取接口定义"},
		{"读取其他文件", "operation", "读取接口定义"},
		{"修复调试流程", "task", "修复调试流程"},
		{"后续任务", "task", "修复调试流程"},
		{"宿主对话标题", "host", "宿主对话标题"},
	}
	for _, test := range tests {
		if err := registry.AutoName(ctx, item.ID, test.title, test.source); err != nil {
			t.Fatal(err)
		}
		got, err := registry.Get(ctx, item.ID)
		if err != nil || got.Title != test.expected {
			t.Fatalf("%+v => %+v %v", test, got, err)
		}
	}
	if _, err := registry.Manage(ctx, item.ID, MetadataChange{Action: "rename", Title: "用户指定名称"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.AutoName(ctx, item.ID, "新的宿主标题", "host"); err != nil {
		t.Fatal(err)
	}
	got, _ := registry.Get(ctx, item.ID)
	if got.Title != "用户指定名称" || got.TitleSource != "manual" {
		t.Fatalf("manual overwritten: %+v", got)
	}
	old := Conversation{Title: "旧版自定义名称"}
	if applyAutomaticTitle(&old, "任务名称", "task") {
		t.Fatal("legacy custom name was overwritten")
	}
	placeholder := Conversation{Title: "新对话"}
	if !applyAutomaticTitle(&placeholder, "项目乙 · 09-21 00:00", "fallback") {
		t.Fatal("legacy placeholder did not migrate")
	}
}

func Test113ConversationGateAndTombstoneSurviveReopen(t *testing.T) {
	root := t.TempDir()
	registry, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "test", Namespace: "mcp", HostConversationID: "persistent"})
	item, err := registry.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.SetTerminated(ctx, item.ID, true); !errors.Is(err, ErrConversationOwner) {
		t.Fatalf("model terminated itself: %v", err)
	}
	local := WithLocalManagement(ctx)
	if _, err = registry.SetTerminated(local, item.ID, true); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(ctx, item.ID)
	if err != nil || got.TerminatedAt == nil {
		t.Fatalf("gate did not survive restart: %+v %v", got, err)
	}
	if _, err = reopened.SetTerminated(local, item.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Manage(local, item.ID, MetadataChange{Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Manage(local, item.ID, MetadataChange{Action: "delete"}); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Resolve(ctx); !errors.Is(err, ErrConversationDeleted) {
		t.Fatalf("deleted source was reminted: %v", err)
	}
	if _, err = reopened.Get(ctx, item.ID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatal("deleted item visible in Get")
	}
	if list, err := reopened.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("deleted item visible: %+v %v", list, err)
	}
	if deleted, err := reopened.IsDeleted(ctx, item.ID); err != nil || !deleted {
		t.Fatalf("tombstone missing: %v", err)
	}
}

func Test113CallManagementStableHandleAndReplay(t *testing.T) {
	root := filepath.Join(t.TempDir(), "activity")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parent, _ := NewExecutionID("call_")
	child, _ := NewExecutionID("call_")
	for _, binding := range []Binding{{CallID: parent}, {CallID: child, ParentCallID: parent}} {
		if _, err := store.Append(ctx, Event{Binding: binding, Kind: "call.created", Status: "created", ToolName: "read_file", Title: "历史调用"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(ctx, Event{Binding: binding, Kind: "call.completed", Status: "succeeded", ToolName: "read_file"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		action, view string
		count        int
	}{{"archive", "archived", 2}, {"unarchive", "active", 2}, {"isolate", "isolated", 2}, {"unisolate", "active", 2}, {"trash", "trash", 2}, {"restore", "active", 2}, {"trash", "trash", 2}} {
		if err := store.ManageCall(ctx, parent, MetadataChange{Action: test.action}); err != nil {
			t.Fatal(err)
		}
		page, err := store.Calls(ctx, CallQuery{Unattributed: true, View: test.view})
		if err != nil || len(page.Calls) != test.count {
			t.Fatalf("%s => %d %v", test.action, len(page.Calls), err)
		}
		for _, call := range page.Calls {
			if call.ConversationID != "" || call.TaskID != "" {
				t.Fatal("management invented attribution")
			}
		}
	}
	before, _ := store.Calls(ctx, CallQuery{View: "all"})
	if err := store.ManageCall(ctx, parent, MetadataChange{Action: "delete"}); err != nil {
		t.Fatal(err)
	}
	updates, err := store.Calls(ctx, CallQuery{After: before.LatestSeq, Updates: true, View: "all"})
	if err != nil || len(updates.Calls) != 1 || updates.Calls[0].DeletedAt == nil {
		t.Fatalf("SSE removal missing: %+v %v", updates, err)
	}
	late, _ := NewExecutionID("call_")
	for _, binding := range []Binding{{CallID: parent}, {CallID: late, ParentCallID: parent}} {
		if _, err := store.Append(ctx, Event{Binding: binding, Kind: "call.completed", Status: "succeeded", ToolName: "read_file"}); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reopened.Calls(ctx, CallQuery{View: "all"})
	if err != nil || len(page.Calls) != 0 {
		t.Fatalf("replay resurrected records: %+v %v", page, err)
	}
	for _, id := range []string{parent, child, late} {
		if _, err := reopened.Call(ctx, id); !errors.Is(err, ErrCallNotFound) {
			t.Fatalf("deleted detail %s readable: %v", id, err)
		}
	}
}

func Test113ActiveRecordsAndFilteredUpdates(t *testing.T) {
	store, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, _ := NewExecutionID("call_")
	b := Binding{CallID: id}
	if _, err := store.Append(ctx, Event{Binding: b, Kind: "call.started", Status: "running", ToolName: "exec_command"}); err != nil {
		t.Fatal(err)
	}
	initial, _ := store.Calls(ctx, CallQuery{Status: "running"})
	if err := store.ManageCall(ctx, id, MetadataChange{Action: "trash"}); err == nil {
		t.Fatal("running call was hidden")
	}
	if _, err := store.Append(ctx, Event{Binding: b, Kind: "call.completed", Status: "succeeded", ToolName: "exec_command"}); err != nil {
		t.Fatal(err)
	}
	updates, err := store.Calls(ctx, CallQuery{Status: "running", Updates: true, After: initial.LatestSeq})
	if err != nil || len(updates.Calls) != 1 || updates.Calls[0].Status != "succeeded" {
		t.Fatalf("filtered terminal update was suppressed: %+v %v", updates, err)
	}
}

func Test113LegacyCallCanBeManagedWithoutFabricatedTask(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.Append(ctx, Event{Kind: "tool.completed", ToolName: "read_file", Status: "success", Summary: "旧版未归属记录"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.Calls(ctx, CallQuery{Unattributed: true})
	if err != nil || len(page.Calls) != 1 {
		t.Fatalf("legacy projection unavailable: %+v %v", page, err)
	}
	id := page.Calls[0].CallID
	if !page.Calls[0].ReadOnlyLegacy || id == "" {
		t.Fatalf("legacy handle missing: %+v", page.Calls[0])
	}
	for _, action := range []string{"isolate", "trash", "delete"} {
		if err := store.ManageCall(ctx, id, MetadataChange{Action: action}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	reopened, _ := New(root, Options{})
	after, err := reopened.Calls(ctx, CallQuery{View: "all"})
	if err != nil || len(after.Calls) != 0 {
		t.Fatalf("legacy replay resurrection: %+v %v", after, err)
	}
}
