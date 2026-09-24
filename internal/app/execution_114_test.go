package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
)

func Test114FileEditActionsShareOneObservedRoot(t *testing.T) {
	runtime := newRuntimeValidationTestRuntime(t)
	ctx := activity.WithSource(context.Background(), activity.Source{
		Principal:          "file-edit-test",
		Provider:           "openai",
		Namespace:          "mcp",
		HostConversationID: "conversation-114-file-edit",
	})
	boot, err := runtime.Call(ctx, "agentdock_context", nil)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := stringArg(boot, "conversation_id")
	if conversationID == "" {
		t.Fatal("missing conversation binding")
	}

	patch := "*** Begin Patch\n*** Update File: note.txt\n@@\n-beta\n+gamma\n*** Add File: extra.txt\n+extra\n*** End Patch\n"
	actions := []struct {
		name string
		args map[string]any
	}{
		{name: "add", args: map[string]any{"action": "add", "path": "note.txt", "content": "alpha\n"}},
		{name: "replace", args: map[string]any{"action": "replace", "path": "note.txt", "old": "alpha", "new": "beta"}},
		{name: "patch", args: map[string]any{"action": "patch", "patch": patch}},
		{name: "move", args: map[string]any{"action": "move", "path": "note.txt", "new_path": "moved.txt"}},
		{name: "delete", args: map[string]any{"action": "delete", "path": "moved.txt"}},
	}
	for _, test := range actions {
		t.Run(test.name, func(t *testing.T) {
			result, err := runtime.Call(ctx, "file_edit", test.args)
			if err != nil {
				t.Fatal(err)
			}
			callID := stringArg(result, "call_id")
			if callID == "" {
				t.Fatalf("missing call id: %+v", result)
			}
			call, err := runtime.activity.Call(ctx, callID)
			if err != nil {
				t.Fatal(err)
			}
			if call.ConversationID != conversationID || call.ToolName != "file_edit" || call.Status != "succeeded" || call.ParentCallID != "" {
				t.Fatalf("bad file-edit root: %+v", call)
			}
			if !strings.Contains(call.DisplayTitle, "file_edit") || strings.Contains(call.DisplayTitle, "EDIT_FILE") || call.FileEdit == nil || call.FileEdit.Action != test.name || !call.FileEdit.Executed {
				t.Fatalf("missing file-edit observation: %+v", call)
			}
			if call.RequestReceivedAt == nil || call.RPCCompletedAt == nil || call.RPCElapsedMS == nil || call.RPCStatus != "succeeded" || call.ExecutionElapsedMS == nil || call.WaitElapsedMS == nil {
				t.Fatalf("missing lifecycle measurements: %+v", call.CallMeasurements)
			}
		})
	}

	if _, err = os.Stat(filepath.Join(runtime.cfg.AgentDockDefaultDir, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete did not remove moved file: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(runtime.cfg.AgentDockDefaultDir, "extra.txt")); err != nil || string(data) != "extra\n" {
		t.Fatalf("patch result = %q, %v", data, err)
	}
}

func Test114FileEditDryRunAndFailureRemainVisible(t *testing.T) {
	runtime := newRuntimeValidationTestRuntime(t)
	ctx := activity.WithSource(context.Background(), activity.Source{
		Principal:          "file-edit-test",
		Provider:           "openai",
		Namespace:          "mcp",
		HostConversationID: "conversation-114-file-preview",
	})
	if _, err := runtime.Call(ctx, "agentdock_context", nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runtime.cfg.AgentDockDefaultDir, "preview.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := runtime.Call(ctx, "file_edit", map[string]any{"action": "replace", "path": "preview.txt", "old": "before", "new": "after", "dry_run": true})
	if err != nil {
		t.Fatal(err)
	}
	previewCall, err := runtime.activity.Call(ctx, stringArg(preview, "call_id"))
	if err != nil {
		t.Fatal(err)
	}
	if previewCall.FileEdit == nil || !previewCall.FileEdit.DryRun || !previewCall.FileEdit.Executed || previewCall.FileEdit.Changed == nil || *previewCall.FileEdit.Changed {
		t.Fatalf("dry-run facts are ambiguous: %+v", previewCall.FileEdit)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "before\n" {
		t.Fatalf("dry run changed file: %q, %v", data, err)
	}

	_, err = runtime.Call(ctx, "file_edit", map[string]any{"action": "move", "path": "missing.txt", "new_path": "target.txt"})
	if err == nil {
		t.Fatal("missing source move unexpectedly succeeded")
	}
	page, queryErr := runtime.activity.Calls(ctx, activity.CallQuery{ConversationID: previewCall.ConversationID, TopLevel: true, IncludeOutput: true, Limit: 20})
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	var failed *activity.ExecutionCall
	for index := range page.Calls {
		call := &page.Calls[index]
		if call.ToolName == "file_edit" && call.Status == "failed" && call.FileEdit != nil && call.FileEdit.Action == "move" {
			failed = call
			break
		}
	}
	if failed == nil || !failed.FileEdit.Executed || failed.FileEdit.Changed != nil || failed.RPCStatus != "failed" {
		t.Fatalf("failed edit was lost or misreported: %+v", failed)
	}
	if _, err := os.Stat(filepath.Join(runtime.cfg.AgentDockDefaultDir, "target.txt")); !os.IsNotExist(err) {
		t.Fatal("failed move created its target")
	}
	_, validationErr := runtime.Call(ctx, "file_edit", map[string]any{"action": "move", "new_path": "target.txt"})
	if validationErr == nil {
		t.Fatal("missing source path was accepted")
	}
	page, queryErr = runtime.activity.Calls(ctx, activity.CallQuery{ConversationID: previewCall.ConversationID, TopLevel: true, IncludeOutput: true, Limit: 20})
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	foundRejection := false
	for _, rejected := range page.Calls {
		if rejected.FileEdit != nil && rejected.FileEdit.Action == "move" && !rejected.FileEdit.Executed {
			foundRejection = true
			if rejected.FileEdit.Changed == nil || *rejected.FileEdit.Changed || rejected.Status != "failed" {
				t.Fatal("validation rejection was reported as a file modification")
			}
		}
	}
	if !foundRejection {
		t.Fatal("validation rejection disappeared from call history")
	}
}

func Test114LocalManagementDoesNotRefreshConversationActivity(t *testing.T) {
	runtime := newRuntimeValidationTestRuntime(t)
	ctx := activity.WithSource(context.Background(), activity.Source{
		Principal:          "activity-test",
		Provider:           "openai",
		Namespace:          "mcp",
		HostConversationID: "conversation-114-activity",
	})
	boot, err := runtime.Call(ctx, "agentdock_context", nil)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := stringArg(boot, "conversation_id")
	_, before, err := runtime.activity.CallStatistics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	last := before[conversationID].LastToolCallAt
	if last == nil {
		t.Fatal("real tool request did not set activity time")
	}
	if _, err = runtime.Call(activity.WithLocalManagement(ctx), "read_file", map[string]any{"path": "missing.txt"}); err == nil {
		t.Fatal("local read fixture unexpectedly succeeded")
	}
	_, after, err := runtime.activity.CallStatistics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := after[conversationID].LastToolCallAt; got == nil || !got.Equal(*last) {
		t.Fatalf("local UI action refreshed tool activity: before=%v after=%v", last, got)
	}
}
