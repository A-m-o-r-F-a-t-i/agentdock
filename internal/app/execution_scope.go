package app

import (
	"context"
	"fmt"

	"github.com/uvwt/agentdock/internal/activity"
)

// resolveExecutionScope runs once at the shared ingress, before validation.
// Internal dispatches inherit their parent's value snapshot without consulting
// mutable conversation defaults again.
func (r *Runtime) resolveExecutionScope(ctx context.Context) (activity.ExecutionScope, error) {
	if parent := activity.ExecutionScopeFromContext(ctx); parent.CallID != "" {
		return parent, nil
	}
	conversation, err := (activity.ConversationResolver{Registry: r.conversations}).Resolve(ctx)
	source := activity.SourceFromContext(ctx)
	binding := activity.ExecutionScope{ConversationID: conversation.ID, Source: source.Namespace,
		BindingQuality: conversation.Attribution, SourceOwnerKey: activity.SourceOwnerKey(ctx), Visibility: "normal"}
	if binding.BindingQuality == "" {
		binding.BindingQuality = "unattributed"
	}
	if activity.IsDiagnostic(ctx) {
		binding.Visibility = "diagnostic"
	}
	if err != nil {
		return binding, err
	}
	state := conversation.State
	if state.ActiveTaskID != "" {
		if task, lookupErr := r.tasks.Get(state.ActiveTaskID); lookupErr == nil && (string(task.Status) == "completed" || string(task.Status) == "cancelled" || task.TrashedAt != nil) {
			if err = r.conversations.ClearTask(ctx, state.ActiveTaskID); err != nil {
				return binding, err
			}
			conversation, err = r.conversations.Get(ctx, conversation.ID)
			if err != nil {
				return binding, err
			}
			state = conversation.State
		}
	}
	binding.TaskID, binding.ThreadID, binding.WorkspaceID = state.ActiveTaskID, state.ActiveTaskThreadID, state.WorkspaceID
	binding.BindingRevision = state.BindingRevision
	return binding, nil
}

func applyExecutionOverrides(name string, args map[string]any, binding activity.Binding) (activity.Binding, error) {
	management := name == "task_manage"
	action := stringArg(args, "action")
	if management && (action == "create" || action == "list") {
		binding.TaskID, binding.ThreadID, binding.StepID = "", "", ""
	}
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"task_id", &binding.TaskID}, {"thread_id", &binding.ThreadID}, {"workspace_id", &binding.WorkspaceID},
	} {
		value := stringArg(args, field.name)
		if value == "" {
			continue
		}
		if !management && binding.ConversationID != "" && *field.target != "" && *field.target != value {
			return binding, toolError("EXECUTION_BINDING_CONFLICT", fmt.Sprintf("%s conflicts with the server binding; use task_manage set_current/resume or an explicit workspace selection before executing.", field.name), "conflict")
		}
		if management && field.name == "task_id" && binding.TaskID != value {
			binding.ThreadID, binding.StepID, binding.WorkspaceID = "", "", ""
		}
		*field.target = value
	}
	if value := stringArg(args, "step_id"); value != "" {
		binding.StepID = value
	}
	binding.Label = stringArg(args, "activity_label")
	return binding, nil
}

// commitConversationState never changes p.state.binding. A task created by this
// Call is linked through management metadata and milestones; subsequent Calls
// receive the new binding. Existing calls and approval requests keep theirs.
func (r *Runtime) commitConversationState(ctx context.Context, p *preparedExecution, result Result) {
	if result == nil {
		return
	}
	action := stringArg(p.args, "action")
	if p.spec.Name == "task_manage" && (action == "complete" || action == "cancel") {
		if err := r.conversations.ClearTask(ctx, stringArg(p.args, "task_id")); err != nil {
			result["binding_warning"] = "Task lifecycle succeeded, but clearing continuation bindings failed: " + err.Error()
		}
		return
	}
	scope := p.state.entryBinding
	next := activity.ConversationState{ActiveTaskID: scope.TaskID, ActiveTaskThreadID: scope.ThreadID, WorkspaceID: scope.WorkspaceID}
	change := false
	if p.spec.Name == "task_manage" {
		switch action {
		case "create", "resume", "set_current", "thread_switch":
			id := stringArg(result, "task_id")
			if id == "" {
				id = stringArg(p.args, "task_id")
			}
			target := activity.Binding{TaskID: id, ThreadID: stringArg(p.args, "thread_id")}
			if action == "create" {
				target.ThreadID = ""
			}
			resolved, err := r.taskTools.ResolveBinding(target, false)
			if err != nil {
				result["binding_warning"] = err.Error()
				return
			}
			next.ActiveTaskID, next.ActiveTaskThreadID, next.WorkspaceID = id, resolved.ThreadID, resolved.WorkspaceID
			if err = r.conversations.Link(ctx, scope.ConversationID, id, next.WorkspaceID); err != nil {
				result["binding_warning"] = err.Error()
				return
			}
			change = true
		case "unbind":
			next.ActiveTaskID, next.ActiveTaskThreadID = "", ""
			change = true
		}
	}
	if (p.spec.Name == "agentdock_context" || p.spec.Name == "workspace_context") && stringArg(p.args, "workdir") != "" {
		var selected struct {
			ID string `json:"workspace_id"`
		}
		if remarshal(result["workspace"], &selected) == nil && selected.ID != "" {
			next.WorkspaceID, change = selected.ID, true
		}
	}
	if !change {
		return
	}
	if scope.ConversationID == "" {
		result["binding_updated"] = false
		result["binding_warning"] = "Host conversation metadata is unavailable. This call remains unattributed; no conversation defaults were guessed or created."
		return
	}
	state, err := r.conversations.UpdateBinding(ctx, scope.ConversationID, scope.BindingRevision, next)
	if err != nil {
		result["binding_updated"] = false
		result["binding_warning"] = "The operation succeeded, but its continuation update was not applied: " + err.Error()
		return
	}
	result["binding_updated"] = true
	result["conversation_state"] = state
}
