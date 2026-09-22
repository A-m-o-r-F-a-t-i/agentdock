package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/uvwt/agentdock/internal/activity"
)

type ConversationLifecycleRequest struct {
	Confirm bool `json:"confirm"`
}

func (r *Runtime) checkConversationGate(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	item, err := r.conversations.Get(ctx, id)
	if err != nil {
		if errors.Is(err, activity.ErrConversationNotFound) {
			deleted, lookupErr := r.conversations.IsDeleted(ctx, id)
			if lookupErr != nil {
				return lookupErr
			}
			if deleted {
				return toolError("CONVERSATION_TERMINATED", activity.ConversationTerminatedMessage, "permission")
			}
		}
		return err
	}
	if item.TerminatedAt != nil {
		return toolError("CONVERSATION_TERMINATED", activity.ConversationTerminatedMessage, "permission")
	}
	return nil
}

// Gate changes and executor admission share executionMu. Persist before
// cancellation. Only the authenticated local UI can reopen this gate.
func (r *Runtime) RuntimeConversationLifecycle(ctx context.Context, id, action string, request ConversationLifecycleRequest) (Result, error) {
	if !activity.IsLocalManagement(ctx) {
		return nil, toolError("LOCAL_ONLY", "Only the authenticated local control panel may stop or restore a conversation.", "permission")
	}
	if !request.Confirm || action != "terminate" && action != "resume" {
		return nil, errors.New("explicit confirmation and terminate/resume action are required")
	}
	r.executionMu.Lock()
	if action == "resume" && r.conversationHasActivityLocked(ctx, id, "") {
		r.executionMu.Unlock()
		return nil, toolError("CONVERSATION_STOPPING", "仍有运行或待审批调用，请确认停止结果后再恢复。", "conflict")
	}
	item, err := r.conversations.SetTerminated(ctx, id, action == "terminate")
	if err != nil {
		r.executionMu.Unlock()
		return nil, err
	}
	calls := map[string]bool{}
	warnings := []string{}
	cancelled := 0
	if action == "terminate" {
		if r.insertions != nil {
			_, owner, lookupErr := r.conversations.LocalTarget(ctx, id)
			if lookupErr != nil {
				warnings = append(warnings, lookupErr.Error())
			} else if cancelErr := r.insertions.Cancel(ctx, owner, id, "", true); cancelErr != nil {
				warnings = append(warnings, "插入取消状态未保存："+cancelErr.Error())
			}
		}
		for approvalID, pending := range r.pendingCalls {
			if pending.state.binding.ConversationID != id {
				continue
			}
			_, settleErr := r.permissions.Settle(ctx, approvalID, "rejected", activity.ConversationTerminatedMessage)
			if settleErr != nil {
				warnings = append(warnings, settleErr.Error())
				continue
			}
			delete(r.pendingCalls, approvalID)
			cancelled++
			if eventErr := r.appendExecution(activity.Event{Binding: pending.state.binding, Kind: "call.completed", Status: "cancelled", ToolName: pending.spec.Name, ApprovalID: approvalID, Summary: activity.ConversationTerminatedMessage}); eventErr != nil {
				warnings = append(warnings, eventErr.Error())
			}
		}
		for callID, live := range r.activeCalls {
			if live.binding.ConversationID == id {
				calls[callID] = true
			}
		}
		// Async commands outlive the foreground tool call and activeCalls.
		for _, status := range []string{"created", "running"} {
			var before uint64
			for {
				page, pageErr := r.activity.Calls(ctx, activity.CallQuery{ConversationID: id, Status: status, Limit: 200, Before: before, View: "all"})
				if pageErr != nil {
					warnings = append(warnings, pageErr.Error())
					break
				}
				for _, call := range page.Calls {
					calls[call.CallID] = true
				}
				if !page.HasMore {
					break
				}
				before = page.NextBefore
			}
		}
	}
	r.executionMu.Unlock()
	stopped := 0
	for callID := range calls {
		result, stopErr := r.RuntimeCallStop(ctx, callID)
		if stopErr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", callID, stopErr))
			continue
		}
		if result["stopped"] == true || result["already_finished"] == true {
			stopped++
		}
	}
	r.executionMu.Lock()
	remaining := r.conversationHasActivityLocked(ctx, id, "")
	r.executionMu.Unlock()
	return Result{"conversation": item, "terminated": action == "terminate", "pending_cancelled": cancelled,
		"stop_requests": len(calls), "confirmed_stopped": stopped, "has_remaining_activity": remaining,
		"warnings": warnings, "history_rewritten": false, "automatic_replay": false}, nil
}

// Upgrade old placeholders on read and new titles when meaningful tasks arrive.
// Name failures do not undo successful execution; the next view refresh retries.
func (r *Runtime) updateConversationName(ctx context.Context, id, tool string, args map[string]any) {
	if id == "" {
		return
	}
	item, err := r.conversations.Get(ctx, id)
	if err != nil || item.TitleSource == "manual" || item.TitleSource == "host" {
		return
	}
	if item.TitleSource == "" && item.Title != "" && item.Title != "新对话" {
		return
	}
	for _, taskID := range item.TaskIDs {
		if task, err := r.tasks.Get(taskID); err == nil && strings.TrimSpace(task.Title) != "" {
			_ = r.conversations.AutoName(ctx, id, task.Title, "task")
			return
		}
	}
	if tool == "task_manage" && stringArg(args, "action") == "create" {
		return
	}
	if title := strings.TrimSpace(stringArg(args, "activity_label")); title != "" && tool != "agentdock_context" {
		_ = r.conversations.AutoName(ctx, id, title, "operation")
		return
	}
	if item.TitleSource == "operation" || item.TitleSource == "task" {
		return
	}
	workspace := "对话"
	workspaceID := item.State.WorkspaceID
	if workspaceID == "" && len(item.WorkspaceIDs) > 0 {
		workspaceID = item.WorkspaceIDs[0]
	}
	if workspaceID != "" {
		if record, err := r.workspaceRegistry.Select(ctx, workspaceID, ""); err == nil && record.Name != "" {
			workspace = record.Name
		}
	}
	_ = r.conversations.AutoName(ctx, id, conversationFallbackTitle(item, workspace), "fallback")
}
