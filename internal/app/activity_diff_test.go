package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/process"
)

func TestActivityCurrentDiffIsBoundedRedactedAndTaskScoped(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	r := newRuntimeValidationTestRuntime(t)
	ctx := context.Background()
	record, id := workspaceTask(t, r)
	git := func(arguments ...string) {
		t.Helper()
		args := append([]string{"-C", record.Root}, arguments...)
		command := exec.Command("git", args...)
		process.Configure(command)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", arguments, output, err)
		}
	}
	git("init")
	file := filepath.Join(record.Root, "source.txt")
	if err := os.WriteFile(file, []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.email=fixture@example.test", "-c", "user.name=Fixture", "commit", "-m", "fixture")
	if _, err := r.Call(ctx, "file_edit", map[string]any{"action": "replace", "task_id": id, "path": "source.txt", "old": "initial", "new": "updated\npassword=private-diff-secret", "expected_matches": 1}); err != nil {
		t.Fatal(err)
	}
	page, err := r.activity.Query(ctx, activity.Query{TaskID: id})
	if err != nil {
		t.Fatal(err)
	}
	var source activity.Event
	for _, event := range page.Events {
		if event.Kind == "file.changed" && event.ResolvedPath == file {
			source = event
		}
	}
	if source.Seq == 0 {
		t.Fatal("file edit did not emit its actual path")
	}
	request := ActivityDiffRequest{TaskID: id, ThreadID: "main", Seq: source.Seq}
	result, err := r.RuntimeActivityDiff(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	diff := result["diff"].(string)
	if !strings.Contains(diff, "+updated") || strings.Contains(diff, "private-diff-secret") || result["view"] != "current_working_tree" || result["tracked"] != true {
		t.Fatalf("incorrect current diff: %+v", result)
	}
	invalid := request
	invalid.TaskID = "tsk_9999999999999999"
	if _, err = r.RuntimeActivityDiff(ctx, invalid); err == nil {
		t.Fatal("foreign task read the file event")
	}
	invalid = request
	invalid.Seq = source.Seq + 1
	if _, err = r.RuntimeActivityDiff(ctx, invalid); err == nil {
		t.Fatal("non-file event accepted for diff")
	}
	// Diff remains available after cancellation and archiving, without reopening the thread.
	if _, err = r.Call(ctx, "task_manage", map[string]any{"action": "cancel", "task_id": id, "summary": "fixture cancellation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Call(ctx, "task_manage", map[string]any{"action": "archive", "task_id": id}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte(strings.Repeat("changed bounded line\n", 20000)), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = r.RuntimeActivityDiff(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result["truncated"] != true || len(result["diff"].(string)) > maxActivityDiffBytes {
		t.Fatal("large diff exceeded its bound")
	}
	// Repository helper settings cannot replace the fixed read-only operation.
	git("config", "diff.external", "this-command-must-not-execute")
	result, err = r.RuntimeActivityDiff(ctx, request)
	if err != nil || !strings.Contains(result["diff"].(string), "changed bounded line") {
		t.Fatalf("external diff hook was used: %+v %v", result, err)
	}
}
