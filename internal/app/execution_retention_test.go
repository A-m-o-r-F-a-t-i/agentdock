package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/taskstate"
)

func TestExecutionRecycleExpiryAndLegacyMetadataIsolation(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := context.Background()
	result, err := r.Call(ctx, "task_manage", map[string]any{"action": "create", "title": "expiry", "goal": "test", "completion_conditions": []any{"verified"}})
	if err != nil {
		t.Fatal(err)
	}
	id := stringArg(result, "task_id")
	sentinel := filepath.Join(r.cfg.AgentDockDefaultDir, "keep.txt")
	if err = os.WriteFile(sentinel, []byte("workspace must remain"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RuntimeManagementBatch(ctx, "task", BatchRequest{IDs: []string{id}, Action: "tags", Tags: []string{"migration", "p0"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RuntimeManagementBatch(ctx, "task", BatchRequest{IDs: []string{id}, Action: "trash", RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	page, err := r.tasks.ManagedTasks(ctx, taskstate.TaskQuery{View: "trash"})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].PurgeAfter == nil {
		t.Fatal("trash lacks a real expiry")
	}
	if count, err := r.purgeExpiredManagement(ctx, time.Now().UTC()); err != nil || count != 0 {
		t.Fatal("metadata deleted before expiry")
	}
	if count, err := r.purgeExpiredManagement(ctx, time.Now().UTC().Add(48*time.Hour)); err != nil || count != 1 {
		t.Fatalf("expired object did not purge: %d %v", count, err)
	}
	if _, err = r.tasks.Get(id); err == nil {
		t.Fatal("expired task still exists")
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "workspace must remain" {
		t.Fatal("retention touched project files")
	}
	calls, err := r.activity.Calls(ctx, activity.CallQuery{TaskID: id})
	if err != nil || len(calls.Calls) == 0 {
		t.Fatal("deleting management erased independent execution audit")
	}
}
