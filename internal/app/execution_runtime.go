package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/permission"
	"github.com/uvwt/agentdock/internal/taskstate"
)

type ExecutionListQuery struct {
	snapshot    bool // Internal grouped projection only; never exposed as an unbounded HTTP list.
	Selection   bool
	View        string
	Search      string
	WorkspaceID string
	Tag         string
	Offset      int
	Limit       int
}
type ConversationItem struct {
	LastActivityAt time.Time `json:"last_activity_at"`
	activity.Conversation
	Statistics     activity.CallStats `json:"statistics"`
	IsUnattributed bool               `json:"is_unattributed,omitempty"`
}
type ConversationPage struct {
	ServerNow     time.Time          `json:"server_now"`
	SelectedIDs   []string           `json:"selected_ids,omitempty"`
	Conversations []ConversationItem `json:"conversations"`
	Total         int                `json:"total"`
	NextOffset    int                `json:"next_offset"`
	HasMore       bool               `json:"has_more"`
}
type BatchRequest struct {
	IDs              []string `json:"ids"`
	Action           string   `json:"action"`
	Title            string   `json:"title,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	RetentionDays    int      `json:"retention_days,omitempty"`
	ConfirmPermanent bool     `json:"confirm_permanent,omitempty"`
}
type BatchItem struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	CallID  string `json:"call_id,omitempty"`
}
type BatchResult struct {
	Items     []BatchItem `json:"items"`
	Succeeded int         `json:"succeeded"`
	Skipped   int         `json:"skipped"`
	Failed    int         `json:"failed"`
	Status    string      `json:"status"`
}

func (r *Runtime) RuntimeExecutionOverview(ctx context.Context) (Result, error) {
	r.expirePendingApprovals(ctx)
	stats, conversations, err := r.activity.CallStatistics(ctx)
	if err != nil {
		return nil, err
	}
	policy, err := r.permissions.Get(ctx)
	if err != nil {
		return nil, err
	}
	return Result{"statistics": stats, "conversation_activity": conversations, "server_now": time.Now().UTC(), "permission_mode": policy.GlobalMode, "policy_revision": policy.Revision, "schema_version": 2}, nil
}
func (r *Runtime) RuntimeConversations(ctx context.Context, query ExecutionListQuery) (ConversationPage, error) {
	page := ConversationPage{Conversations: []ConversationItem{}, ServerNow: time.Now().UTC()}
	if query.Offset < 0 || query.Offset > 20000 || len(query.Search) > 512 {
		return page, errors.New("invalid conversation query")
	}
	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 100
	}
	if query.View != "" && query.View != "active" && query.View != "archived" && query.View != "trash" && query.View != "all" {
		return page, errors.New("invalid conversation view")
	}
	items, err := r.conversations.List(ctx)
	if err != nil {
		return page, err
	}
	_, stats, err := r.activity.CallStatistics(ctx)
	if err != nil {
		return page, err
	}
	workspaceNames := map[string]string{}
	workspaces, _, err := r.workspaceRegistry.List(ctx)
	if err != nil {
		return page, err
	}
	for _, workspace := range workspaces {
		workspaceNames[workspace.ID] = workspace.Name
	}
	items, err = r.nameConversationSnapshot(ctx, items, workspaceNames)
	if err != nil {
		return page, err
	}
	candidates := []ConversationItem{}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		switch query.View {
		case "trash":
			if item.TrashedAt == nil {
				continue
			}
		case "archived":
			if item.TrashedAt != nil || item.ArchivedAt == nil {
				continue
			}
		case "all":
		default:
			if item.TrashedAt != nil || item.ArchivedAt != nil {
				continue
			}
		}
		workspaceText := ""
		for _, id := range item.WorkspaceIDs {
			workspaceText += " " + workspaceNames[id]
		}
		if query.Search != "" && !strings.Contains(strings.ToLower(item.Title+" "+item.Source+" "+strings.Join(item.Tags, " ")+workspaceText), strings.ToLower(query.Search)) {
			continue
		}
		if query.Tag != "" {
			found := false
			for _, tag := range item.Tags {
				if tag == query.Tag {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if query.WorkspaceID != "" {
			found := false
			for _, id := range item.WorkspaceIDs {
				if id == query.WorkspaceID {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		summary := stats[item.ID]
		if summary.LatestAt.After(item.UpdatedAt) {
			item.UpdatedAt = summary.LatestAt
		}
		lastActivity := item.CreatedAt
		if summary.LastActivityAt != nil && summary.LastActivityAt.After(lastActivity) {
			lastActivity = *summary.LastActivityAt
		}
		candidates = append(candidates, ConversationItem{Conversation: item, Statistics: summary, LastActivityAt: lastActivity})
	}
	if unknown := stats[""]; unknown.Total > 0 && query.View != "trash" && query.View != "archived" && query.Tag == "" && query.WorkspaceID == "" && (query.Search == "" || strings.Contains("未识别对话", query.Search)) {
		// This is a navigation group, not a minted Conversation. Its ID is empty and
		// calls are queried with unattributed=true, never conversation_id=unknown.
		candidates = append(candidates, ConversationItem{Conversation: activity.Conversation{Title: "未识别对话 · 独立调用", Source: "unknown", UpdatedAt: unknown.LatestAt}, Statistics: unknown, IsUnattributed: true, LastActivityAt: unknown.LatestAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Pinned != candidates[j].Pinned {
			return candidates[i].Pinned
		}
		if candidates[i].LastActivityAt.Equal(candidates[j].LastActivityAt) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].LastActivityAt.After(candidates[j].LastActivityAt)
	})
	page.Total = len(candidates)
	if query.Selection {
		page.SelectedIDs = []string{}
		for _, item := range candidates {
			if item.ID != "" {
				page.SelectedIDs = append(page.SelectedIDs, item.ID)
			}
		}
		return page, nil
	}
	if query.snapshot {
		page.Conversations = candidates
		page.NextOffset = len(candidates)
		return page, nil
	}
	start := min(query.Offset, len(candidates))
	end := min(start+query.Limit, len(candidates))
	page.NextOffset = end
	page.HasMore = end < len(candidates)
	page.Conversations = append(page.Conversations, candidates[start:end]...)
	return page, nil
}
func (r *Runtime) RuntimeConversation(ctx context.Context, id string) (Result, error) {
	item, err := r.conversations.Get(ctx, id)
	if err != nil {
		if errors.Is(err, activity.ErrConversationNotFound) {
			if deleted, lookupErr := r.conversations.IsDeleted(ctx, id); lookupErr != nil {
				return nil, lookupErr
			} else if deleted {
				return nil, activity.ErrConversationDeleted
			}
		}
		return nil, err
	}
	effective, err := r.permissions.Effective(ctx, activity.Binding{ConversationID: id, WorkspaceID: item.State.WorkspaceID})
	if err != nil {
		return nil, err
	}
	return Result{"conversation": item, "permission": effective}, nil
}
func (r *Runtime) RuntimeLinkConversation(ctx context.Context, id, taskID string) (Result, error) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	item, err := r.conversations.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	task, err := r.tasks.Get(taskID)
	if err != nil {
		return nil, err
	}
	if item.TrashedAt != nil || task.TrashedAt != nil {
		return nil, errors.New("restore the conversation and task before linking them")
	}
	binding, err := r.localManagementStart(ctx, "conversation.link_task", activity.Binding{ConversationID: id, TaskID: taskID}, "关联已有任务")
	if err != nil {
		return nil, err
	}
	if err = r.conversations.Link(ctx, id, taskID, task.WorkspaceID); err != nil {
		r.localManagementFinish(binding, "conversation.link_task", "failed", err.Error())
		return nil, err
	}
	r.localManagementFinish(binding, "conversation.link_task", "succeeded", "关联仅影响管理关系，不改写历史调用的 task_id。")
	return Result{"linked": true, "conversation_id": id, "task_id": taskID, "history_rewritten": false}, nil
}
func (r *Runtime) RuntimeManagedTasks(ctx context.Context, query taskstate.TaskQuery) (taskstate.ManagedTaskPage, error) {
	return r.tasks.ManagedTasks(ctx, query)
}
func (r *Runtime) RuntimeManagementBatch(ctx context.Context, kind string, request BatchRequest) (BatchResult, error) {
	result := BatchResult{Items: []BatchItem{}, Status: "succeeded"}
	if kind != "task" && kind != "conversation" {
		return result, errors.New("invalid management object type")
	}
	if len(request.IDs) == 0 || len(request.IDs) > 200 {
		return result, errors.New("a batch requires 1–200 explicit IDs; freeze and chunk larger selections")
	}
	switch request.Action {
	case "rename", "pin", "unpin", "tags", "archive", "unarchive", "trash", "restore", "delete":
	default:
		return result, errors.New("unsupported batch action")
	}
	if request.Action == "rename" && len(request.IDs) != 1 {
		return result, errors.New("rename requires one object")
	}
	if request.Action == "delete" && !request.ConfirmPermanent {
		return result, errors.New("permanent deletion requires explicit confirmation; only management data is removed")
	}
	change := activity.MetadataChange{Action: request.Action, Title: request.Title, Tags: append([]string(nil), request.Tags...), RetentionDays: request.RetentionDays}
	// Validate common inputs before mutating any member of the frozen selection.
	if request.Action != "delete" {
		var metadata activity.Management
		title := "placeholder"
		if err := activity.ApplyManagement(&metadata, &title, change, time.Now().UTC()); err != nil {
			return result, err
		}
	}
	ids := append([]string(nil), request.IDs...)
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return result, errors.New("batch IDs must be unique")
		}
		seen[id] = true
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			result.Items = append(result.Items, BatchItem{ID: id, Status: "skipped", Message: "请求已取消，该对象未处理。"})
			result.Skipped++
			continue
		}
		item := r.manageOne(ctx, kind, id, change)
		result.Items = append(result.Items, item)
		switch item.Status {
		case "succeeded":
			result.Succeeded++
		case "skipped":
			result.Skipped++
		default:
			result.Failed++
		}
	}
	if result.Skipped > 0 || result.Failed > 0 {
		result.Status = "partial"
	}
	return result, nil
}
func (r *Runtime) manageOne(ctx context.Context, kind, id string, change activity.MetadataChange) BatchItem {
	item := BatchItem{ID: id, Status: "failed"}
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	binding := activity.Binding{}
	if kind == "task" {
		binding.TaskID = id
	} else {
		binding.ConversationID = id
	}
	if err := binding.Validate(); err != nil {
		item.Message = err.Error()
		return item
	}
	binding, err := r.localManagementStart(ctx, kind+"."+change.Action, binding, change.Action+" · "+id)
	if err != nil {
		item.Message = err.Error()
		return item
	}
	item.CallID = binding.CallID
	finish := func(status, message string) BatchItem {
		item.Status, item.Message = status, message
		callStatus := status
		if status == "skipped" {
			callStatus = "cancelled"
		}
		r.localManagementFinish(binding, kind+"."+change.Action, callStatus, message)
		return item
	}
	if change.Action == "trash" || change.Action == "delete" {
		active := false
		if kind == "task" {
			active = r.taskHasActivityLocked(id, binding.CallID)
		} else {
			active = r.conversationHasActivityLocked(ctx, id, binding.CallID)
		}
		if active {
			return finish("skipped", "仍有关联的运行项或待审批请求，请先停止或处理审批。项目文件未改动。")
		}
	}
	if kind == "task" {
		if change.Action == "delete" {
			_, err = r.tasks.Delete(id)
		} else {
			_, err = r.tasks.ManageMetadata(id, change)
		}
	} else {
		_, err = r.conversations.Manage(ctx, id, change)
	}
	if err != nil {
		return finish("failed", r.executionRedactor(nil).Text(err.Error(), 1024))
	}
	if kind == "task" && (change.Action == "trash" || change.Action == "delete") {
		if err = r.conversations.ClearTask(ctx, id); err != nil {
			return finish("failed", err.Error())
		}
	}
	message := "管理数据已更新。"
	if change.Action == "trash" {
		message = "已移入回收站，源码、仓库和工作区未改动。"
	}
	if change.Action == "delete" {
		message = "管理对象已永久删除；项目文件未改动，执行与审批审计按独立保留规则保存。"
	}
	return finish("succeeded", message)
}
func (r *Runtime) conversationHasActivityLocked(ctx context.Context, id, exclude string) bool {
	for callID, live := range r.activeCalls {
		if callID != exclude && live.binding.ConversationID == id {
			return true
		}
	}
	for _, pending := range r.pendingCalls {
		if pending.state.binding.CallID != exclude && pending.state.binding.ConversationID == id {
			return true
		}
	}
	for _, status := range []string{"created", "running", "pending_approval"} {
		page, err := r.activity.Calls(ctx, activity.CallQuery{ConversationID: id, Status: status, Limit: 2})
		if err != nil {
			return true
		}
		for _, call := range page.Calls {
			if call.CallID != exclude {
				return true
			}
		}
	}
	return false
}
func (r *Runtime) localManagementStart(ctx context.Context, tool string, binding activity.Binding, title string) (activity.Binding, error) {
	// A selected management target is not the identity of the UI request. Only
	// an internal child of an existing Call inherits that Call's conversation.
	if binding.ParentCallID == "" {
		target := binding
		origin, err := r.resolveExecutionScope(ctx)
		if err != nil {
			return binding, err
		}
		binding = origin
		if target.TaskID != "" {
			binding.TaskID, binding.ThreadID, binding.StepID = target.TaskID, "", ""
		}
		if target.WorkspaceID != "" {
			binding.WorkspaceID = target.WorkspaceID
		}
	}
	id, err := activity.NewExecutionID("call_")
	if err != nil {
		return binding, err
	}
	binding.CallID = id
	if err = binding.Validate(); err != nil {
		return binding, err
	}
	err = r.appendExecution(activity.Event{Binding: binding, Kind: "call.created", ToolName: tool, Title: title, Status: "created"})
	if err != nil {
		return binding, err
	}
	err = r.appendExecution(activity.Event{Binding: binding, Kind: "call.started", ToolName: tool, Title: title, Status: "running"})
	return binding, err
}
func (r *Runtime) localManagementFinish(binding activity.Binding, tool, status, summary string) {
	_ = r.appendExecution(activity.Event{Binding: binding, Kind: "call.completed", ToolName: tool, Status: status, Summary: r.executionRedactor(nil).Text(summary, 4096)})
}
func (r *Runtime) RuntimePermissions(ctx context.Context, binding activity.Binding) (Result, error) {
	if binding.ConversationID != "" && binding.WorkspaceID == "" {
		item, err := r.conversations.Get(ctx, binding.ConversationID)
		if err != nil {
			return nil, err
		}
		binding.WorkspaceID = item.State.WorkspaceID
	}
	policy, err := r.permissions.Get(ctx)
	if err != nil {
		return nil, err
	}
	effective, err := r.permissions.Effective(ctx, binding)
	if err != nil {
		return nil, err
	}
	workspaces, _, err := r.workspaceRegistry.List(ctx)
	if err != nil {
		return nil, err
	}
	return Result{"policy": policy, "effective": effective, "workspaces": workspaces, "conversation_id": binding.ConversationID, "workspace_id": binding.WorkspaceID, "os_privileges_unchanged": true}, nil
}
func (r *Runtime) RuntimePermissionsUpdate(ctx context.Context, change permission.Change) (Result, error) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	if change.Scope == "workspace" {
		if _, err := r.workspaceRegistry.Select(ctx, change.ScopeID, ""); err != nil {
			return nil, err
		}
	}
	if change.Scope == "conversation" {
		if _, err := r.conversations.Get(ctx, change.ScopeID); err != nil {
			return nil, err
		}
	}
	if change.Rules != nil {
		for _, rule := range *change.Rules {
			if _, available := r.ToolDefinition(rule.Tool); !available {
				return nil, errors.New("permission rules require an available AgentDock tool")
			}
			if rule.WorkspaceID != "" {
				if _, err := r.workspaceRegistry.Select(ctx, rule.WorkspaceID, ""); err != nil {
					return nil, err
				}
			}
		}
	}
	binding, err := r.localManagementStart(ctx, "permission.update", activity.Binding{}, "修改执行权限")
	if err != nil {
		return nil, err
	}
	policy, err := r.permissions.Update(ctx, change)
	if err != nil {
		r.localManagementFinish(binding, "permission.update", "failed", err.Error())
		return nil, err
	}
	r.localManagementFinish(binding, "permission.update", "succeeded", fmt.Sprintf("scope=%s scope_id=%s mode=%s revision=%d；操作系统权限未改变。", change.Scope, change.ScopeID, change.Mode, policy.Revision))
	return Result{"policy": policy, "os_privileges_unchanged": true}, nil
}
func (r *Runtime) RuntimeApprovals(ctx context.Context, status string, offset, limit int) (Result, error) {
	r.expirePendingApprovals(ctx)
	items, err := r.permissions.Approvals(ctx)
	if err != nil {
		return nil, err
	}
	filtered := []permission.Approval{}
	for _, item := range items {
		if status == "" || status == item.Status {
			filtered = append(filtered, item)
		}
	}
	if offset < 0 || offset > 20000 {
		return nil, errors.New("invalid approval offset")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	start := min(offset, len(filtered))
	end := min(start+limit, len(filtered))
	return Result{"approvals": filtered[start:end], "total": len(filtered), "has_more": end < len(filtered), "next_offset": end}, nil
}
func (r *Runtime) RuntimeApprovalRequest(ctx context.Context, id string) (Result, error) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	a, err := r.permissions.Approval(ctx, id)
	if err != nil {
		return nil, err
	}
	result := Result{"approval": a, "request_available": false}
	if p := r.pendingCalls[id]; p != nil && a.Status == "pending" {
		raw, err := json.MarshalIndent(p.args, "", "  ")
		if err != nil {
			return nil, err
		}
		result["request_available"] = true
		result["fixed_request"] = r.executionRedactor(p.args).Text(string(raw), 1<<20)
		result["rule_preview"] = permission.Rule{Tool: p.spec.Name, Action: stringArg(p.args, "action"), WorkspaceID: p.state.binding.WorkspaceID, Effect: permission.Allow, Reason: "允许当前工作区的此类工具操作。"}
	}
	return result, nil
}
func (r *Runtime) RuntimeApprovalDecision(ctx context.Context, id, action string, allowWorkspace bool) (Result, error) {
	if action != "approve" && action != "reject" {
		return nil, errors.New("invalid approval decision")
	}
	r.executionMu.Lock()
	a, err := r.permissions.Approval(ctx, id)
	if err != nil {
		r.executionMu.Unlock()
		return nil, err
	}
	if a.Status != "pending" {
		r.executionMu.Unlock()
		return Result{"approval": a, "already_decided": true, "dispatched": false}, nil
	}
	p := r.pendingCalls[id]
	cancelPending := func(reason string) (Result, error) {
		settled, settleErr := r.permissions.Settle(ctx, id, "expired", reason)
		delete(r.pendingCalls, id)
		r.executionMu.Unlock()
		_ = r.appendExecution(activity.Event{Binding: a.Binding, Kind: "call.completed", ToolName: a.Tool, ApprovalID: id, Status: "cancelled", Summary: reason})
		return Result{"approval": settled, "dispatched": false}, settleErr
	}
	if action == "reject" {
		settled, settleErr := r.permissions.Settle(ctx, id, "rejected", "用户拒绝，原操作未执行。")
		delete(r.pendingCalls, id)
		r.executionMu.Unlock()
		_ = r.appendExecution(activity.Event{Binding: a.Binding, Kind: "call.completed", ToolName: a.Tool, ApprovalID: id, Status: "cancelled", Summary: "用户拒绝，原操作未执行。"})
		return Result{"approval": settled, "dispatched": false}, settleErr
	}
	if p == nil {
		return cancelPending("原始固定请求不在当前服务中，不能安全恢复，未自动执行。")
	}
	if p.state.selected != nil {
		current, lookupErr := r.workspaceRegistry.Select(ctx, p.state.selected.ID, "")
		if lookupErr != nil || current.RulesRevision != p.state.selected.RulesRevision {
			return cancelPending("工作区规则已变化，原批准失效，操作未执行。")
		}
	}
	if err = r.revalidatePrepared(ctx, p); err != nil {
		return cancelPending(err.Error())
	}
	if err = r.executionAdmissionLocked(ctx, p.state.binding, p.spec.Name, stringArg(p.args, "action"), p.state.binding.CallID); err != nil {
		return cancelPending(err.Error())
	}
	a, claimed, claimErr := r.permissions.ClaimWithWorkspaceRule(ctx, id, allowWorkspace)
	if claimErr != nil {
		if errors.Is(claimErr, permission.ErrApprovalExpired) {
			return cancelPending("审批已过期或权限策略已变化，原操作未执行。")
		}
		r.executionMu.Unlock()
		return nil, claimErr
	}
	if !claimed {
		r.executionMu.Unlock()
		return Result{"approval": a, "already_decided": true, "dispatched": false}, nil
	}
	delete(r.pendingCalls, id)
	p.decision.Revision = a.PolicyRevision
	if a.GrantedRuleID != "" {
		p.decision.RuleID = a.GrantedRuleID
	}
	runCtx, cancel := context.WithCancel(r.commandCtx)
	r.activeCalls[p.state.binding.CallID] = &liveExecution{binding: p.state.binding, cancel: cancel, source: p.source}
	r.executionWG.Add(1)
	r.executionMu.Unlock()
	go func() { _, _ = r.executePrepared(runCtx, p) }()
	return Result{"approval": a, "dispatched": true}, nil
}
func (r *Runtime) expirePendingApprovals(ctx context.Context) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	policy, err := r.permissions.Get(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for id, p := range r.pendingCalls {
		a, err := r.permissions.Approval(ctx, id)
		if err != nil {
			continue
		}
		if a.Status == "pending" && now.Before(a.ExpiresAt) && a.PolicyRevision == policy.Revision {
			continue
		}
		if a.Status == "pending" {
			a, err = r.permissions.Settle(ctx, id, "expired", "审批到期或权限策略已变化，操作未执行。")
			if err != nil {
				continue
			}
		}
		delete(r.pendingCalls, id)
		_ = r.appendExecution(activity.Event{Binding: p.state.binding, Kind: "call.completed", ToolName: p.spec.Name, ApprovalID: id, Status: "cancelled", Summary: a.Summary})
	}
}
func (r *Runtime) RuntimeCallStop(ctx context.Context, id string) (Result, error) {
	call, err := r.activity.Call(ctx, id)
	if err != nil {
		return nil, err
	}
	if activity.CallTerminal(call.Status) {
		return Result{"already_finished": true, "status": call.Status, "stopped": false}, nil
	}
	if call.Status == "pending_approval" && call.ApprovalID != "" {
		return r.RuntimeApprovalDecision(ctx, call.ApprovalID, "reject", false)
	}
	r.executionMu.Lock()
	live := r.activeCalls[id]
	if live != nil && call.ToolName != "exec_command" { /* cancellation follows the durable stop record below */
	}
	r.executionMu.Unlock()
	binding := call.Binding
	binding.CallID = ""
	binding.ParentCallID = id
	binding.RetryOfCallID = ""
	binding, err = r.localManagementStart(ctx, "call.stop", binding, "停止执行")
	if err != nil {
		return nil, err
	}
	if live != nil && call.ToolName != "exec_command" {
		live.cancel()
	}
	if r.command.CallActivityRunning(id) || live != nil && call.ToolName == "exec_command" {
		stopped, stopErr := r.stopStartingCommand(ctx, id)
		status := "succeeded"
		summary := "已发送停止请求，等待进程退出。"
		if stopped {
			summary = "已确认命令进程退出。"
		}
		if stopErr != nil {
			status = "failed"
			summary = stopErr.Error()
		}
		r.localManagementFinish(binding, "call.stop", status, summary)
		return Result{"requested": true, "stopped": stopped, "call_id": id}, stopErr
	}
	if live == nil {
		r.localManagementFinish(binding, "call.stop", "failed", "当前服务没有该运行实例，请核对已记录调用的实际状态。")
		return nil, toolError("CALL_NOT_RUNNING", "The running instance is unavailable; verify its actual state before retrying.", "conflict")
	}
	r.localManagementFinish(binding, "call.stop", "succeeded", "已发送取消请求，最终副作用结果以原调用状态为准。")
	return Result{"requested": true, "stopped": false, "call_id": id}, nil
}

// RuntimeConversationBinding is a low-frequency, explicit local-user action.
// It never changes scopes of calls that have already entered the executor.
func (r *Runtime) RuntimeConversationBinding(ctx context.Context, id string, request ConversationBindingRequest) (Result, error) {
	if !activity.IsLocalManagement(ctx) {
		return nil, toolError("LOCAL_ONLY", "Only an authenticated direct local control action may change another conversation's continuation state.", "permission")
	}
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	if request.TaskID == "" && request.TaskThreadID != "" {
		return nil, errors.New("task_id is required with task_thread_id")
	}
	if request.BindingRevision == nil {
		return nil, errors.New("binding_revision is required")
	}
	item, err := r.conversations.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	next := item.State
	if request.TaskID == "" {
		next.ActiveTaskID, next.ActiveTaskThreadID = "", ""
	} else {
		task, err := r.tasks.Get(request.TaskID)
		if err != nil {
			return nil, err
		}
		if task.TrashedAt != nil || task.Status == taskstate.StatusCompleted || string(task.Status) == "cancelled" {
			return nil, errors.New("only an unfinished task may be selected")
		}
		resolved, err := r.taskTools.ResolveBinding(activity.Binding{TaskID: request.TaskID, ThreadID: request.TaskThreadID}, false)
		if err != nil {
			return nil, err
		}
		next.ActiveTaskID, next.ActiveTaskThreadID, next.WorkspaceID = resolved.TaskID, resolved.ThreadID, resolved.WorkspaceID
	}
	binding, err := r.localManagementStart(ctx, "conversation.set_current", activity.Binding{TaskID: request.TaskID}, "设置对话当前任务")
	if err != nil {
		return nil, err
	}
	state, err := r.conversations.UpdateBinding(ctx, id, *request.BindingRevision, next)
	if err != nil {
		r.localManagementFinish(binding, "conversation.set_current", "failed", err.Error())
		return nil, err
	}
	if err = r.conversations.Link(ctx, id, state.ActiveTaskID, state.WorkspaceID); err != nil {
		r.localManagementFinish(binding, "conversation.set_current", "partial", err.Error())
		return nil, err
	}
	r.localManagementFinish(binding, "conversation.set_current", "succeeded", "已更新后续调用的默认任务。运行项和审批快照保持不变。")
	return Result{"state": state, "binding_updated": true, "history_rewritten": false}, nil
}

type ConversationBindingRequest struct {
	TaskID          string  `json:"task_id"`
	TaskThreadID    string  `json:"task_thread_id,omitempty"`
	BindingRevision *uint64 `json:"binding_revision"`
}
