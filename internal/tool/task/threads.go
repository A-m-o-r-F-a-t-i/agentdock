package task

import (
	"context"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/taskstate"
)

func (s *Service) SetActivityStore(store *activity.Store) { s.activity = store }

func (s *Service) Manage(ctx context.Context, request ManageRequest) (Result, error) {
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	var result Result
	var err error
	switch {
	case request.Action == "set_current":
		task, getErr := s.tasks.Get(request.TaskID)
		if getErr != nil {
			return nil, taskToolError(getErr)
		}
		if task.TrashedAt != nil || string(task.Status) == "completed" || string(task.Status) == "cancelled" {
			return nil, toolErrorDetails("TASK_NOT_ACTIVE", "Only an active or blocked task can be selected for continuation.", "validation", nil)
		}
		thread, getErr := s.tasks.GetThread(request.TaskID, request.ThreadID)
		if getErr != nil {
			return nil, taskToolError(getErr)
		}
		result = Result{"action": request.Action, "task_id": task.ID, "thread_id": thread.ID, "task_summary": compactTaskSummary(task)}
	case request.Action == "unbind":
		result = Result{"action": request.Action}
	case strings.HasPrefix(request.Action, "thread_"):
		result, err = s.manageThread(request)
	case request.Action == "cancel" || request.Action == "archive" || request.Action == "unarchive":
		var task taskstate.Task
		if request.Action == "cancel" {
			task, err = s.tasks.Cancel(request.TaskID, request.Summary)
		} else {
			task, err = s.tasks.Archive(request.TaskID, request.Action == "archive")
		}
		if err == nil {
			result = Result{"action": request.Action, "task_id": task.ID, "task_summary": compactTaskSummary(task), "state_dir": s.tasks.Root()}
		}
	case request.Action == "checkpoint" && request.ThreadID != "":
		result, err = s.manageThread(request)
	default:
		result, err = s.manageLegacy(ctx, request)
		if err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, taskToolError(err)
	}
	if request.Action == "create" && request.WorkspaceID != "" {
		id, _ := result["task_id"].(string)
		task, bindErr := s.tasks.SetWorkspace(id, request.WorkspaceID)
		if bindErr != nil {
			result["workspace_warning"] = "Task was created, but workspace binding failed: " + bindErr.Error()
		} else {
			result["task_summary"] = compactTaskSummary(task)
		}
	}
	if request.Action == "get" && request.ThreadID != "" {
		thread, getErr := s.tasks.GetThread(request.TaskID, request.ThreadID)
		if getErr != nil {
			return nil, taskToolError(getErr)
		}
		result["thread"] = thread
	}
	s.recordTaskActivity(ctx, request, result)
	return result, nil
}

func (s *Service) manageThread(request ManageRequest) (Result, error) {
	input := taskstate.ThreadInput{Title: request.Title, WorkspaceID: request.WorkspaceID, CurrentStepID: request.CurrentStepID, Summary: request.Summary, NextAction: request.NextAction, SourceRef: request.SourceRef}
	if request.CompletedStepIDs != nil {
		input.CompletedStepIDs = append([]string(nil), (*request.CompletedStepIDs)...)
	}
	for _, step := range request.Steps {
		input.Steps = append(input.Steps, taskstate.TaskStepInput{ID: step.ID, Title: step.Title})
	}
	var thread taskstate.TaskThread
	var err error
	switch request.Action {
	case "thread_list":
		threads, err := s.tasks.ListThreads(request.TaskID)
		if err != nil {
			return nil, err
		}
		return Result{"action": request.Action, "task_id": request.TaskID, "threads": threads, "count": len(threads)}, nil
	case "thread_get":
		thread, err = s.tasks.GetThread(request.TaskID, request.ThreadID)
	case "thread_create", "thread_fork":
		thread, err = s.tasks.CreateThread(request.TaskID, request.ThreadID, request.Action == "thread_fork", input)
	case "thread_switch":
		task, err := s.tasks.SwitchThread(request.TaskID, request.ThreadID)
		if err != nil {
			return nil, err
		}
		return Result{"action": request.Action, "task_id": task.ID, "task_summary": compactTaskSummary(task), "thread": task.ActiveThread}, nil
	case "thread_checkpoint", "checkpoint":
		if request.StepID != "" {
			if request.CurrentStepID != "" || request.CompletedStepIDs != nil {
				return nil, toolErrorDetails("VALIDATION_ERROR", "single and batch checkpoint fields cannot be combined", "validation", nil)
			}
			switch request.Status {
			case "completed":
				input.CompletedStepIDs = []string{request.StepID}
			case "in_progress":
				input.CurrentStepID = request.StepID
			default:
				return nil, toolErrorDetails("VALIDATION_ERROR", "explicit thread checkpoint requires in_progress or completed", "validation", nil)
			}
		}
		thread, err = s.tasks.UpdateThread(request.TaskID, request.ThreadID, "checkpoint", input)
	case "thread_block", "thread_resume", "thread_close":
		thread, err = s.tasks.UpdateThread(request.TaskID, request.ThreadID, strings.TrimPrefix(request.Action, "thread_"), input)
	default:
		return nil, toolErrorDetails("INVALID_ACTION", "unsupported thread action", "validation", nil)
	}
	if err != nil {
		return nil, err
	}
	return Result{"action": request.Action, "task_id": request.TaskID, "thread_id": thread.ID, "thread": thread, "state_dir": s.tasks.Root()}, nil
}

func (s *Service) ResolveBinding(binding activity.Binding, persist bool) (activity.Binding, error) {
	if err := binding.Validate(); err != nil {
		return binding, err
	}
	if binding.TaskID == "" {
		return binding, nil
	}
	task, err := s.tasks.Get(binding.TaskID)
	if err != nil {
		return binding, err
	}
	thread, err := s.tasks.GetThread(binding.TaskID, binding.ThreadID)
	if err != nil {
		return binding, err
	}
	binding.ThreadID = thread.ID
	if binding.StepID == "" {
		binding.StepID = thread.CurrentStepID
	}
	if binding.WorkspaceID == "" {
		binding.WorkspaceID = thread.WorkspaceID
	}
	if binding.WorkspaceID == "" {
		binding.WorkspaceID = task.WorkspaceID
	}
	if persist {
		_, err = s.tasks.UpdateThread(binding.TaskID, binding.ThreadID, "bind", taskstate.ThreadInput{CurrentStepID: binding.StepID, WorkspaceID: binding.WorkspaceID})
	}
	return binding, err
}

func (s *Service) recordTaskActivity(parent context.Context, request ManageRequest, result Result) {
	if s.activity == nil {
		return
	}
	kind := map[string]string{
		"create": "task.created", "checkpoint": "step.summary", "block": "task.blocked", "resume": "task.resumed", "final_review": "review.completed", "complete": "task.completed", "cancel": "task.cancelled", "archive": "task.archived", "unarchive": "task.unarchived",
		"thread_create": "thread.created", "thread_fork": "thread.created", "thread_switch": "thread.switched", "thread_checkpoint": "step.summary", "thread_block": "thread.blocked", "thread_resume": "thread.resumed", "thread_close": "thread.closed",
	}[request.Action]
	if kind == "" {
		return
	}
	id, _ := result["task_id"].(string)
	if id == "" {
		id = request.TaskID
	}
	binding := activity.FromContext(parent)
	// Milestones retain their causal Call without changing that Call's scope.
	binding.ParentCallID, binding.CallID = binding.CallID, ""
	binding.TaskID = id
	if request.ThreadID != "" {
		binding.ThreadID = request.ThreadID
	}
	if request.WorkspaceID != "" {
		binding.WorkspaceID = request.WorkspaceID
	}
	var thread *taskstate.TaskThread
	switch value := result["thread"].(type) {
	case taskstate.TaskThread:
		thread = &value
	case *taskstate.TaskThread:
		thread = value
	}
	if thread == nil {
		if summary, ok := result["task_summary"].(map[string]any); ok {
			if selected, ok := summary["active_thread_id"].(string); ok {
				binding.ThreadID = selected
			}
		}
		if current, err := s.tasks.GetThread(id, binding.ThreadID); err == nil {
			thread = &current
		}
	}
	if thread != nil {
		binding.ThreadID, binding.StepID, binding.WorkspaceID = thread.ID, thread.CurrentStepID, thread.WorkspaceID
	}
	status := "success"
	if thread != nil {
		status = thread.Status
	}
	switch request.Action {
	case "create", "resume":
		status = "active"
	case "block":
		status = "blocked"
	case "complete":
		status = "success"
	case "archive":
		status = "archived"
	case "unarchive":
		status = "completed"
	}
	if request.Action == "cancel" {
		status = "cancelled"
	}
	if request.Action == "final_review" {
		status = request.Status
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := s.activity.Append(ctx, activity.Event{Binding: binding, Kind: kind, Status: status, Title: request.Title, ToolName: ToolTaskManage, Summary: request.Summary})
	if err != nil {
		result["activity_warning"] = "Task state saved; activity journal could not be updated: " + err.Error()
		return
	}
	result["activity_event_id"] = event.EventID
	if kind == "step.summary" && thread != nil {
		updated, refErr := s.tasks.UpdateThread(id, thread.ID, "checkpoint_ref", taskstate.ThreadInput{CheckpointEventID: event.EventID})
		if refErr != nil {
			result["activity_warning"] = "Checkpoint saved; activity reference could not be attached: " + refErr.Error()
		} else if result["thread"] != nil {
			result["thread"] = updated
		}
	}
	if request.Action == "create" {
		if _, err = s.activity.Append(ctx, activity.Event{Binding: binding, Kind: "thread.created", Status: "open", Title: "main", ToolName: ToolTaskManage}); err != nil {
			result["activity_warning"] = "Task and main thread saved; thread activity could not be recorded: " + err.Error()
		}
	}
	if request.CurrentStepID != "" && (kind == "step.summary") {
		binding.StepID = request.CurrentStepID
		if _, err = s.activity.Append(ctx, activity.Event{Binding: binding, Kind: "step.started", Status: "in_progress", ToolName: ToolTaskManage}); err != nil {
			result["activity_warning"] = "Checkpoint saved; step activity could not be recorded: " + err.Error()
		}
	}
}
