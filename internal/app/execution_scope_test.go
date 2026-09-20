package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/config"
)

func scopeHost(id string) context.Context {
	return activity.WithSource(context.Background(), activity.Source{Principal: "scope-test", Provider: "openai", Namespace: "mcp:http", HostConversationID: id})
}
func scopeTask(t *testing.T, r *Runtime, ctx context.Context, title string) string {
	t.Helper()
	result, err := r.Call(ctx, "task_manage", map[string]any{"action": "create", "title": title, "goal": "Verify automatic continuation", "completion_conditions": []string{"fixture verified"}, "steps": []map[string]any{{"id": "verify", "title": "Verify fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	id := stringArg(result, "task_id")
	if id == "" {
		t.Fatalf("missing task: %+v", result)
	}
	return id
}
func scopeRead(t *testing.T, r *Runtime, ctx context.Context) Result {
	t.Helper()
	result, err := r.Call(ctx, "list_dir", map[string]any{"path": ".", "max_entries": 1})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestExecutionNoToolSchemaPublishesConversationIdentity(t *testing.T) {
	for _, definition := range toolDefinitionsForConfig(config.Config{BrowserEnabled: true, ACPEnabled: true, NexusEndpoint: "http://127.0.0.1:1"}) {
		props, _ := definition.InputSchema["properties"].(map[string]any)
		for _, key := range []string{"conversation_id", "call_id", "parent_call_id", "binding_quality", "binding_revision", "source_owner_key"} {
			if props[key] != nil {
				t.Fatalf("%s exposes internal %s", definition.Name, key)
			}
		}
	}
}
func TestExecutionTaskContinuationViewAndClear(t *testing.T) {
	r := executionTestRuntime(t)
	a, b := scopeHost("a"), scopeHost("b")
	taskA, taskB := scopeTask(t, r, a, "A"), scopeTask(t, r, b, "B")
	if got := stringArg(scopeRead(t, r, a), "task_id"); got != taskA {
		t.Fatalf("not inherited: %s", got)
	}
	if _, err := r.Call(a, "task_manage", map[string]any{"action": "get", "task_id": taskB}); err != nil {
		t.Fatal(err)
	}
	if got := stringArg(scopeRead(t, r, a), "task_id"); got != taskA {
		t.Fatal("view changed current task")
	}
	if _, err := r.Call(a, "file_edit", map[string]any{"action": "add", "path": "forbidden.txt", "content": "not written", "task_id": taskB}); err == nil {
		t.Fatal("conflicting override accepted")
	}
	if _, err := r.Call(a, "task_manage", map[string]any{"action": "unbind"}); err != nil {
		t.Fatal(err)
	}
	if got := stringArg(scopeRead(t, r, a), "task_id"); got != "" {
		t.Fatal("unbind was not inherited")
	}
	if _, err := r.Call(a, "task_manage", map[string]any{"action": "set_current", "task_id": taskA}); err != nil {
		t.Fatal(err)
	}
	if got := stringArg(scopeRead(t, r, a), "task_id"); got != taskA {
		t.Fatal("set_current was not inherited")
	}
	c := scopeHost("c")
	if _, err := r.Call(c, "task_manage", map[string]any{"action": "resume", "task_id": taskA, "summary": "Continue from another conversation"}); err != nil {
		t.Fatal(err)
	}
	if got := stringArg(scopeRead(t, r, c), "task_id"); got != taskA {
		t.Fatal("resume was not inherited")
	}
	if _, err := r.Call(a, "task_manage", map[string]any{"action": "cancel", "summary": "Fixture finished"}); err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{a, c} {
		if got := stringArg(scopeRead(t, r, ctx), "task_id"); got != "" {
			t.Fatal("cancelled task remained bound")
		}
	}
	if got := stringArg(scopeRead(t, r, b), "task_id"); got != taskB {
		t.Fatal("unrelated conversation changed")
	}
}
func TestExecutionCompletedTaskClearsContinuation(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("complete")
	scopeTask(t, r, ctx, "Complete")
	for _, args := range []map[string]any{
		{"action": "checkpoint", "completed_step_ids": []string{"verify"}, "summary": "Fixture verified"},
		{"action": "final_review", "status": "pass", "summary": "Fixture verified", "verified": []string{"fixture verified"}},
		{"action": "complete"},
	} {
		if _, err := r.Call(ctx, "task_manage", args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if got := stringArg(scopeRead(t, r, ctx), "task_id"); got != "" {
		t.Fatal("completed task remained bound")
	}
}
func TestExecutionTwentyConcurrentCallsPerConversation(t *testing.T) {
	r := executionTestRuntime(t)
	a, b := scopeHost("parallel-a"), scopeHost("parallel-b")
	firstA, firstB := scopeRead(t, r, a), scopeRead(t, r, b)
	idA, idB := stringArg(firstA, "conversation_id"), stringArg(firstB, "conversation_id")
	if idA == "" || idA == idB {
		t.Fatal("invalid initial identities")
	}
	var wg sync.WaitGroup
	for _, test := range []struct {
		ctx context.Context
		id  string
	}{{a, idA}, {b, idB}} {
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(ctx context.Context, id string) {
				defer wg.Done()
				got, err := r.Call(ctx, "list_dir", map[string]any{"path": ".", "max_entries": 1})
				if err != nil || stringArg(got, "conversation_id") != id {
					t.Errorf("crossed parallel calls: %+v %v", got, err)
				}
			}(test.ctx, test.id)
		}
	}
	wg.Wait()
	for _, id := range []string{idA, idB} {
		page, err := r.activity.Calls(context.Background(), activity.CallQuery{ConversationID: id, Limit: 100})
		if err != nil || len(page.Calls) != 21 {
			t.Fatalf("missing calls: %d %v", len(page.Calls), err)
		}
		for _, call := range page.Calls {
			if call.ConversationID != id || call.TaskID != "" {
				t.Fatal("mixed call scope")
			}
		}
	}
}
func TestExecutionApprovalRetainsTaskAfterSwitch(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("approval-snapshot")
	taskA := scopeTask(t, r, ctx, "A")
	pending, err := r.Call(ctx, "file_edit", map[string]any{"action": "add", "path": "snapshot.txt", "content": "fixed snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if pending["status"] != "pending_approval" {
		t.Fatalf("not pending: %+v", pending)
	}
	taskB := scopeTask(t, r, ctx, "B")
	if taskA == taskB {
		t.Fatal("duplicate task")
	}
	if _, err = r.RuntimeApprovalDecision(context.Background(), stringArg(pending, "approval_id"), "approve", false); err != nil {
		t.Fatal(err)
	}
	call := waitExecutionTerminal(t, r, stringArg(pending, "call_id"))
	if call.TaskID != taskA || call.Status != "succeeded" {
		t.Fatalf("changed pending scope: %+v", call)
	}
	if got := stringArg(scopeRead(t, r, ctx), "task_id"); got != taskB {
		t.Fatal("approved old call changed new continuation")
	}
	content, err := os.ReadFile(filepath.Join(r.cfg.AgentDockDefaultDir, "snapshot.txt"))
	if err != nil || string(content) != "fixed snapshot" {
		t.Fatalf("fixed write: %q %v", content, err)
	}
}
func TestExecutionRunningCommandKeepsScopeAcrossSwitch(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("running-snapshot")
	taskA := scopeTask(t, r, ctx, "A")
	cmd := "printf 'first\\n'; sleep 1; printf 'second\\n'"
	if runtime.GOOS == "windows" {
		cmd = "Write-Output 'first'; Start-Sleep -Milliseconds 1000; Write-Output 'second'"
	}
	result, err := r.Call(WithLocalUserAction(ctx), "exec_command", map[string]any{"cmd": cmd, "execution_mode": "async"})
	if err != nil {
		t.Fatal(err)
	}
	scopeTask(t, r, ctx, "B")
	call := waitExecutionTerminal(t, r, stringArg(result, "call_id"))
	if call.TaskID != taskA || call.ThreadID != "main" || call.Status != "succeeded" {
		t.Fatalf("running scope drift: %+v", call)
	}
	page, err := r.activity.Calls(ctx, activity.CallQuery{TaskID: taskA, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range page.Calls {
		if item.SessionID == call.SessionID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("command became %d cards", count)
	}
}
func TestExecutionMissingMetadataNeverMintsGlobalConversation(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		result, err := r.Call(ctx, "agentdock_context", nil)
		if err != nil {
			t.Fatal(err)
		}
		if stringArg(result, "conversation_id") != "" || stringArg(result, "binding_quality") != "unattributed" {
			t.Fatalf("guessed identity: %+v", result)
		}
	}
	conversations, err := r.conversations.List(ctx)
	if err != nil || len(conversations) != 0 {
		t.Fatalf("fake conversations: %+v %v", conversations, err)
	}
	page, err := r.activity.Calls(ctx, activity.CallQuery{Unattributed: true})
	if err != nil || len(page.Calls) != 3 {
		t.Fatalf("missing unattributed calls: %d %v", len(page.Calls), err)
	}
}

func TestExecutionApprovalExpiresWhenMCPDestinationChanges(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("fixed-mcp")
	if _, err := r.RuntimeMCPManage(ctx, map[string]any{"action": "add", "name": "fixed", "description": "Isolated approved target", "transport": "streamable_http", "url": "http://127.0.0.1:1/original", "enabled": true}); err != nil {
		t.Fatal(err)
	}
	result, err := r.Call(ctx, "mcp_tool_call", map[string]any{"name": "fixed:write", "arguments": map[string]any{"value": "original"}})
	if err != nil || result["status"] != "pending_approval" {
		t.Fatalf("expected fixed pending call: %+v %v", result, err)
	}
	if _, err = r.RuntimeMCPManage(ctx, map[string]any{"action": "remove", "name": "fixed"}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RuntimeMCPManage(ctx, map[string]any{"action": "add", "name": "fixed", "description": "Isolated approved target", "transport": "streamable_http", "url": "http://127.0.0.1:1/changed", "enabled": true}); err != nil {
		t.Fatal(err)
	}
	decision, err := r.RuntimeApprovalDecision(context.Background(), stringArg(result, "approval_id"), "approve", false)
	if err != nil || decision["dispatched"] != false {
		t.Fatalf("changed target was dispatched: %+v %v", decision, err)
	}
	call, err := r.activity.Call(ctx, stringArg(result, "call_id"))
	if err != nil || call.Status != "cancelled" {
		t.Fatalf("old pending call not cancelled: %+v %v", call, err)
	}
	child, err := r.activity.Calls(ctx, activity.CallQuery{ParentCallID: call.CallID})
	if err != nil || len(child.Calls) != 0 {
		t.Fatalf("changed target was dispatched: %+v %v", child, err)
	}
}
