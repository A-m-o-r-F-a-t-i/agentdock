package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/taskstate"
)

func activityTask(t *testing.T, r *Runtime) string {
	t.Helper()
	result, err := r.Call(context.Background(), "task_manage", map[string]any{"action": "create", "title": "Activity integration", "goal": "observable execution", "steps": []any{map[string]any{"id": "verify", "title": "Verify"}}, "completion_conditions": []any{"execution verified"}})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result["task_id"].(string)
	if id == "" {
		t.Fatalf("missing task id: %+v", result)
	}
	return id
}

func TestActivityCommandLifecycleRedactionAndRecovery(t *testing.T) {
	r := newRuntimeValidationTestRuntime(t)
	id := activityTask(t, r)
	cmd := `printf 'hello\n'; printf '%s\n' "$ACTIVITY_SECRET"; exit 7`
	if runtime.GOOS == "windows" {
		cmd = `Write-Output 'hello'; Write-Output $env:ACTIVITY_SECRET; exit 7`
	}
	result, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": cmd, "task_id": id, "thread_id": "main", "step_id": "verify", "execution_mode": "sync", "env": map[string]any{"ACTIVITY_SECRET": "private-activity-value"}})
	if err != nil {
		t.Fatal(err)
	}
	if result["command_ok"] != false || result["thread_id"] != "main" || !strings.Contains(result["stdout"].(string), "private-activity-value") {
		t.Fatalf("tool result changed: %+v", result)
	}
	page, err := r.activity.Query(context.Background(), activity.Query{TaskID: id, ThreadID: "main"})
	if err != nil {
		t.Fatal(err)
	}
	started, completed := 0, 0
	for _, event := range page.Events {
		if event.Kind == "command.started" {
			started++
			if event.Workdir == "" || event.StepID != "verify" {
				t.Fatalf("missing command metadata: %+v", event)
			}
		}
		if event.Kind == "command.completed" {
			completed++
			if event.ExitCode == nil || *event.ExitCode != 7 || event.Status != "failed" {
				t.Fatalf("false success: %+v", event)
			}
		}
	}
	data, _ := json.Marshal(page)
	if started != 1 || completed != 1 || strings.Contains(string(data), "private-activity-value") {
		t.Fatalf("invalid lifecycle/redaction: %s", data)
	}
	if _, err = r.Call(context.Background(), "task_manage", map[string]any{"action": "thread_checkpoint", "task_id": id, "summary": "command verified", "next_action": "review result", "current_step_id": "verify"}); err != nil {
		t.Fatal(err)
	}
	forked, err := r.Call(context.Background(), "task_manage", map[string]any{"action": "thread_fork", "task_id": id, "thread_id": "main", "title": "review"})
	if err != nil {
		t.Fatal(err)
	}
	branch := forked["thread"].(taskstate.TaskThread)
	if branch.ParentThreadID != "main" || branch.CheckpointEventID == "" || branch.NextAction != "review result" {
		t.Fatalf("fork did not preserve checkpoint: %+v", branch)
	}
	if _, err = r.Call(context.Background(), "task_manage", map[string]any{"action": "thread_switch", "task_id": id, "thread_id": branch.ID}); err != nil {
		t.Fatal(err)
	}
	contextResult, err := r.AgentDockContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	contextData, _ := json.Marshal(contextResult["tasks"])
	if !strings.Contains(string(contextData), branch.ID) || !strings.Contains(string(contextData), "review result") {
		t.Fatalf("context lacks recovery: %s", contextData)
	}
}

func TestActivityAsyncSessionKeepsOriginalThread(t *testing.T) {
	r := newRuntimeValidationTestRuntime(t)
	id := activityTask(t, r)
	cmd := "sleep 30"
	if runtime.GOOS == "windows" {
		cmd = "Start-Sleep -Seconds 30"
	}
	started, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": cmd, "task_id": id, "execution_mode": "async"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _ := started["session_id"].(string)
	branchResult, err := r.Call(context.Background(), "task_manage", map[string]any{"action": "thread_fork", "task_id": id, "title": "other branch"})
	if err != nil {
		t.Fatal(err)
	}
	branch := branchResult["thread"].(taskstate.TaskThread)
	if _, err = r.Call(context.Background(), "task_manage", map[string]any{"action": "thread_switch", "task_id": id, "thread_id": branch.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RuntimeActivityControl(context.Background(), ActivityControlRequest{Action: "stop", TaskID: id, ThreadID: branch.ID, SessionID: sessionID}); err == nil {
		t.Fatal("wrong thread controlled a session")
	}
	stopped, err := r.RuntimeActivityControl(context.Background(), ActivityControlRequest{Action: "stop", TaskID: id, ThreadID: "main", SessionID: sessionID})
	if err != nil || stopped["thread_id"] != "main" {
		t.Fatalf("stop lost session binding: %+v %v", stopped, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err = r.command.WaitActivity(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := r.activity.Query(ctx, activity.Query{TaskID: id})
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for _, event := range page.Events {
		if event.SessionID == sessionID {
			if event.ThreadID != "main" {
				t.Fatalf("command moved between threads: %+v", event)
			}
			if event.Kind == "command.completed" {
				completed = true
			}
		}
	}
	if !completed {
		t.Fatal("stopped command missing completion event")
	}
}

func TestActivityContextDegradesWhenTaskTransactionDamaged(t *testing.T) {
	r := newRuntimeValidationTestRuntime(t)
	path := filepath.Join(r.cfg.AgentDockHome, "tasks", ".thread-transaction.json")
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := r.AgentDockContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), `"tasks"`) || !strings.Contains(string(data), `"source":"tasks"`) {
		t.Fatalf("task failure did not degrade context: %s", data)
	}
}
