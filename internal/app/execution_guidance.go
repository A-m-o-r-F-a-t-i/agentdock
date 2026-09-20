package app

import (
	"context"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

// Guidance is generated from the server's validated binding and observed result. It
// never incorporates instructions from terminal output, page text or nested MCP content.
func (r *Runtime) executionGuidance(name string, state executionObservation, result Result, failed bool) map[string]any {
	binding := state.binding
	if result != nil {
		if value := stringArg(result, "task_id"); value != "" {
			binding.TaskID = value
		}
		if value := stringArg(result, "thread_id"); value != "" {
			binding.ThreadID = value
		}
		if value := stringArg(result, "step_id"); value != "" {
			binding.StepID = value
		}
		if value := stringArg(result, "workspace_id"); value != "" {
			binding.WorkspaceID = value
		}
	}
	guidance := map[string]any{"source": "agentdock", "schema_version": 1}
	for key, value := range bindingArguments(binding) {
		if value != "" && key != "activity_label" {
			guidance[key] = value
		}
	}
	if binding.WorkspaceID == "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		record, err := r.workspaceRegistry.Select(ctx, "", "")
		cancel()
		if err == nil {
			guidance["default_workspace_id"] = record.ID
		}
	}
	sessionID := stringArg(result, "session_id")
	running := stringArg(result, "status") == "running"
	if running && sessionID != "" {
		guidance["next_required"] = []map[string]any{{"action": "observe", "tool": "session_observe", "arguments": map[string]any{"action": "status", "session_id": sessionID}, "text": "Continue observing this session. Do not start the same long command again."}}
	} else if failed || resultReportsFailure(result) || result["command_ok"] == false {
		guidance["next_required"] = []map[string]any{{"action": "inspect", "text": "Inspect the actual error, exit code and partial effects before retrying. Reuse the same task and thread; a tool response alone does not imply command success."}}
	} else if name == "file_edit" && result["dry_run"] != true && result["changed"] != false {
		guidance["next_required"] = []map[string]any{{"action": "verify", "text": "Verify the changed files, inspect the current diff, and record a thread checkpoint after the stage is verified."}}
	}
	if binding.TaskID != "" {
		guidance["constraints"] = []map[string]string{{"code": "SERVER_BINDING_SNAPSHOT", "text": "The server inherits task and thread binding automatically. Ordinary calls omit task_id/thread_id; use task_manage resume or set_current to change the active task once. Existing command sessions keep their original binding."}}
	}
	if state.target != nil {
		redactor := activity.NewRedactor(r.cfg.AuthToken, r.cfg.NexusDeviceToken)
		guidance["target_kind"] = state.target.Kind
		guidance["resolved_path"] = redactor.Text(state.target.ResolvedPath, 2048)
		guidance["rules_revision"] = state.target.RulesRevision
	}
	return guidance
}
