package app

import (
	"context"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	tooltask "github.com/uvwt/agentdock/internal/tool/task"
)

// ActivityJournal is intentionally outside the shared Nexus Runtime API interface.
// The HTTP adapter exposes it only to authenticated direct loopback clients.
func (r *Runtime) ActivityJournal() *activity.Store { return r.activity }

func (r *Runtime) RuntimeActivityTask(ctx context.Context, arguments map[string]any) (Result, error) {
	switch stringArg(arguments, "action") {
	case "list", "get", "thread_list", "thread_get":
		var request tooltask.ManageRequest
		if err := decodeToolInput("task_manage", arguments, &request); err != nil {
			return nil, err
		}
		return r.taskTools.Manage(ctx, request)
	}
	return r.Call(ctx, "task_manage", arguments)
}

type ActivityControlRequest struct {
	Action    string    `json:"action"`
	TaskID    string    `json:"task_id,omitempty"`
	ThreadID  string    `json:"thread_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Title     string    `json:"title,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Before    time.Time `json:"before,omitempty"`
}

func (r *Runtime) RuntimeActivityControl(ctx context.Context, request ActivityControlRequest) (Result, error) {
	switch request.Action {
	case "stop":
		binding, ok := r.command.SessionBinding(request.SessionID)
		if !ok {
			return nil, toolError("SESSION_NOT_FOUND", "command session is no longer available", "not_found")
		}
		if binding.TaskID != request.TaskID || binding.ThreadID != request.ThreadID {
			return nil, toolError("SESSION_BINDING_MISMATCH", "selected command belongs to another task or thread", "validation")
		}
		if !r.command.ActivitySessionRunning(request.SessionID) {
			return nil, toolError("SESSION_NOT_RUNNING", "the selected command is no longer running", "validation")
		}
		return r.Call(ctx, "session_act", map[string]any{"action": "kill", "session_id": request.SessionID})
	case "cleanup":
		if request.Before.IsZero() || request.Before.After(time.Now().Add(-24*time.Hour)) {
			return nil, toolError("INVALID_CUTOFF", "activity cleanup keeps at least the latest 24 hours", "validation")
		}
		removed, err := r.activity.Cleanup(ctx, request.Before)
		if err != nil {
			return nil, err
		}
		_, err = r.activity.Append(ctx, activity.Event{Kind: "activity.cleaned", Status: "success", Title: "Expired activity segments removed"})
		result := Result{"ok": true, "removed_segments": removed}
		if err != nil {
			result["activity_warning"] = "Cleanup succeeded; its journal record could not be written."
		}
		return result, nil
	case "thread_switch", "thread_create", "thread_fork", "thread_block", "thread_resume", "thread_close", "resume", "cancel", "archive", "unarchive":
		args := map[string]any{"action": request.Action, "task_id": request.TaskID}
		if request.ThreadID != "" {
			args["thread_id"] = request.ThreadID
		}
		if request.Title != "" {
			args["title"] = request.Title
		}
		if request.Summary != "" {
			args["summary"] = request.Summary
		}
		return r.Call(ctx, "task_manage", args)
	default:
		return nil, toolError("INVALID_ACTION", "unsupported activity control: "+strings.TrimSpace(request.Action), "validation")
	}
}
