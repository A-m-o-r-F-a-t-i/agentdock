package app

import (
	"context"
	"errors"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	toolcommand "github.com/uvwt/agentdock/internal/tool/command"
	"github.com/uvwt/agentdock/internal/workspace"
)

// RuntimeActivityLive observes sessions without consuming their output. All branches
// of the selected task remain visible, even after cancellation or journal retention.
// Workspace lookup uses only explicit task/thread bindings, never the global default.
func (r *Runtime) RuntimeActivityLive(ctx context.Context, taskID, threadID string) (Result, error) {
	binding := activity.Binding{TaskID: taskID, ThreadID: threadID}
	if err := binding.Validate(); err != nil {
		return nil, toolError("INVALID_BINDING", err.Error(), "validation")
	}
	if taskID != "" {
		resolved, err := r.taskTools.ResolveBinding(binding, false)
		if err != nil {
			return nil, err
		}
		binding = resolved
	}
	observed, err := r.command.Observe(toolcommand.SessionObserveRequest{Action: "list"})
	if err != nil {
		return nil, err
	}
	sessions := make([]map[string]any, 0)
	for _, item := range observed["sessions"].([]map[string]any) {
		if taskID == "" || item["task_id"] == taskID {
			sessions = append(sessions, item)
		}
	}
	result := Result{
		"task_id": taskID, "thread_id": binding.ThreadID, "workspace_id": binding.WorkspaceID,
		"workspace_status": "unbound", "sessions": sessions, "observed_at": time.Now().UTC(),
	}
	if binding.WorkspaceID != "" {
		record, err := r.workspaceRegistry.Select(ctx, binding.WorkspaceID, "")
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			result["workspace_status"] = "missing"
		} else if err != nil {
			return nil, err
		} else {
			result["workspace_status"] = "bound"
			result["workspace_path"] = record.Root
			result["workspace_runtime"] = record.Runtime
		}
	}
	return result, nil
}
