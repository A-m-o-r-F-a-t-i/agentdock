package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func assert113Terminated(t *testing.T, err error) {
	t.Helper()
	var value *ToolError
	if !errors.As(err, &value) || value.Code != "CONVERSATION_TERMINATED" || value.Message != activity.ConversationTerminatedMessage {
		t.Fatalf("wrong termination result: %#v %v", value, err)
	}
}
func Test113ConversationTerminationBlocksAllNewExecutionAndKeepsHistory(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("stop-a")
	other := scopeHost("stop-b")
	local := activity.WithLocalManagement(context.Background())
	boot := scopeRead(t, r, ctx)
	id := stringArg(boot, "conversation_id")
	scopeRead(t, r, other)
	pending, err := r.Call(ctx, "file_edit", map[string]any{"action": "add", "path": "never-written.txt", "content": "must not run"})
	if err != nil || pending["status"] != "pending_approval" {
		t.Fatalf("pending fixture: %+v %v", pending, err)
	}
	if _, err := r.RuntimeConversationLifecycle(ctx, id, "terminate", ConversationLifecycleRequest{Confirm: true}); err == nil {
		t.Fatal("model was allowed to change local lifecycle")
	}
	result, err := r.RuntimeConversationLifecycle(local, id, "terminate", ConversationLifecycleRequest{Confirm: true})
	if err != nil || result["terminated"] != true {
		t.Fatalf("terminate: %+v %v", result, err)
	}
	for _, test := range []struct {
		name string
		args map[string]any
	}{{"agentdock_context", nil}, {"read_file", map[string]any{"path": "nonexistent"}}, {"file_edit", map[string]any{"action": "add", "path": "denied.txt", "content": "no"}}, {"task_manage", map[string]any{"action": "list"}}, {"unregistered-tool", nil}} {
		_, err := r.Call(ctx, test.name, test.args)
		assert113Terminated(t, err)
	}
	decision, err := r.RuntimeApprovalDecision(local, stringArg(pending, "approval_id"), "approve", false)
	if err == nil && decision["dispatched"] == true {
		t.Fatal("terminated approval was dispatched")
	}
	if _, err := os.Stat(filepath.Join(r.cfg.AgentDockDefaultDir, "never-written.txt")); !os.IsNotExist(err) {
		t.Fatalf("terminated pending write happened: %v", err)
	}
	if _, err := r.RuntimeConversation(local, id); err != nil {
		t.Fatalf("history blocked: %v", err)
	}
	if page, err := r.activity.Calls(local, activity.CallQuery{ConversationID: id, IncludeOutput: true}); err != nil || len(page.Calls) == 0 {
		t.Fatalf("export unavailable: %+v %v", page, err)
	}
	scopeRead(t, r, other)
	if _, err := r.RuntimeConversationLifecycle(ctx, id, "resume", ConversationLifecycleRequest{Confirm: true}); err == nil {
		t.Fatal("model reopened local gate")
	}
	if _, err := r.RuntimeConversationLifecycle(local, id, "resume", ConversationLifecycleRequest{Confirm: true}); err != nil {
		t.Fatal(err)
	}
	scopeRead(t, r, ctx)
	if _, err := os.Stat(filepath.Join(r.cfg.AgentDockDefaultDir, "never-written.txt")); !os.IsNotExist(err) {
		t.Fatal("resume replayed a pending write")
	}
}
func Test113ConversationGatePersistsAcrossRuntimeRestart(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("restart-gate")
	id := stringArg(scopeRead(t, r, ctx), "conversation_id")
	local := activity.WithLocalManagement(context.Background())
	if _, err := r.RuntimeConversationLifecycle(local, id, "terminate", ConversationLifecycleRequest{Confirm: true}); err != nil {
		t.Fatal(err)
	}
	cfg := r.cfg
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	_, err = next.Call(ctx, "list_dir", map[string]any{"path": "."})
	assert113Terminated(t, err)
	if _, err := next.RuntimeConversationLifecycle(local, id, "resume", ConversationLifecycleRequest{Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if got := stringArg(scopeRead(t, next, ctx), "conversation_id"); got != id {
		t.Fatal("resume changed conversation identity")
	}
}
func Test113TerminatingOneConversationStopsAsyncCommandOnlyInThatScope(t *testing.T) {
	r := executionTestRuntime(t)
	a, b := scopeHost("async-a"), scopeHost("async-b")
	id := stringArg(scopeRead(t, r, a), "conversation_id")
	command := "sleep 20"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 20"
	}
	run := func(ctx context.Context) Result {
		result, err := r.Call(WithLocalUserAction(ctx), "exec_command", map[string]any{"cmd": command, "execution_mode": "async"})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first, second := run(a), run(b)
	local := activity.WithLocalManagement(context.Background())
	stopped, err := r.RuntimeConversationLifecycle(local, id, "terminate", ConversationLifecycleRequest{Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	one := waitExecutionTerminal(t, r, stringArg(first, "call_id"))
	if one.Status == "succeeded" {
		t.Fatalf("long command completed instead of cancellation: %+v %+v", one, stopped)
	}
	two, err := r.activity.Call(local, stringArg(second, "call_id"))
	if err != nil || activity.CallTerminal(two.Status) {
		t.Fatalf("unrelated command stopped: %+v %v", two, err)
	}
	if _, err := r.RuntimeCallStop(local, two.CallID); err != nil {
		t.Fatal(err)
	}
	waitExecutionTerminal(t, r, two.CallID)
}
func Test113TerminationApprovalRaceClosesAdmissionBeforeReturning(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("approval-race")
	id := stringArg(scopeRead(t, r, ctx), "conversation_id")
	local := activity.WithLocalManagement(context.Background())
	for round := 0; round < 8; round++ {
		if round > 0 {
			if _, err := r.RuntimeConversationLifecycle(local, id, "resume", ConversationLifecycleRequest{Confirm: true}); err != nil {
				t.Fatal(err)
			}
		}
		pending, err := r.Call(ctx, "file_edit", map[string]any{"action": "add", "path": "race-write.txt", "content": "admitted before gate", "overwrite": true})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.RuntimeApprovalDecision(local, stringArg(pending, "approval_id"), "approve", false)
		}()
		if _, err := r.RuntimeConversationLifecycle(local, id, "terminate", ConversationLifecycleRequest{Confirm: true}); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		waitExecutionTerminal(t, r, stringArg(pending, "call_id"))
		// A request admitted before the gate may already have completed. Nothing
		// submitted after this point may dispatch, regardless of the race winner.
		_, err = r.Call(ctx, "file_edit", map[string]any{"action": "add", "path": "after-gate.txt", "content": "no"})
		assert113Terminated(t, err)
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(r.cfg.AgentDockDefaultDir, "after-gate.txt")); !os.IsNotExist(err) {
		t.Fatal("post-gate write reached disk")
	}
}
func Test113AutomaticTaskNameAndManualOverride(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("task-name")
	id := stringArg(scopeRead(t, r, ctx), "conversation_id")
	scopeTask(t, r, ctx, "首个明确任务")
	item, err := r.conversations.Get(ctx, id)
	if err != nil || item.Title != "首个明确任务" || item.TitleSource != "task" {
		t.Fatalf("task title missing: %+v %v", item, err)
	}
	if _, err := r.RuntimeManagementBatch(context.Background(), "conversation", BatchRequest{IDs: []string{id}, Action: "rename", Title: "用户名称"}); err != nil {
		t.Fatal(err)
	}
	scopeTask(t, r, ctx, "后续任务")
	item, err = r.conversations.Get(ctx, id)
	if err != nil || item.Title != "用户名称" {
		t.Fatalf("manual name replaced: %+v %v", item, err)
	}
}
