package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/config"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
	"github.com/uvwt/agentdock/internal/permission"
	toolfile "github.com/uvwt/agentdock/internal/tool/file"
	"github.com/uvwt/agentdock/internal/workspace"
)

type liveExecution struct {
	binding   activity.Binding
	cancel    context.CancelFunc
	sessionID string
	source    activity.Source
}
type preparedExecution struct {
	spec                ToolSpec
	args                map[string]any
	state               executionObservation
	source              activity.Source
	decision            permission.Decision
	approvalInline      bool
	approvalID          string
	approvalRequestedAt time.Time
	executionStartedAt  time.Time
	sessionIDs          []string
	mcpTarget           string
	outputPolicy        config.ToolOutputSettings
	completion          *activity.AppendReservation
}

func (r *Runtime) callObserved(ctx context.Context, spec ToolSpec, original map[string]any) (result Result, returnErr error) {
	outputPolicy := r.MCPPresentationSettings().ToolOutput
	received := time.Now()
	callID, err := activity.NewExecutionID("call_")
	if err != nil {
		return nil, err
	}
	parent := activity.FromContext(ctx)
	snapshot, resolveErr := r.resolveExecutionScope(ctx)
	snapshot.CallID, snapshot.ParentCallID, snapshot.RetryOfCallID = callID, parent.CallID, ""
	initial := snapshot
	// Audit the trusted origin before validating optional business overrides.
	initial.TaskID, initial.ThreadID, initial.StepID, initial.WorkspaceID = "", "", "", ""
	state := executionObservation{binding: snapshot, entryBinding: snapshot, started: received, originals: map[string]string{}}
	created := activity.Event{Binding: initial, Kind: "call.created", Status: "created", ToolName: spec.Name, Title: spec.Title}
	if parent.CallID == "" {
		created.Request = &activity.Payload{State: "pending"}
		created.Response = &activity.Payload{State: "pending"}
	}
	if parent.CallID == "" && resolveErr == nil && !activity.IsDiagnostic(ctx) && !activity.IsLocalManagement(ctx) && initial.ConversationID != "" {
		stamp := received.UTC()
		created.RequestReceivedAt = &stamp
	}
	initialEvents := []activity.Event{created}
	if resolveErr == nil {
		payloadCtx, payloadCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if payloadEvent, ok := r.executionPayloadEvent(payloadCtx, initial, spec.Name, "request", original, r.executionRedactor(original)); ok {
			initialEvents = append(initialEvents, payloadEvent)
		}
		payloadCancel()
	}
	if err = r.appendExecutions(initialEvents...); err != nil {
		if errors.Is(err, activity.ErrAppendCapacity) {
			return nil, r.executionError(err, state)
		}
		return nil, toolError("AUDIT_UNAVAILABLE", "The execution journal is unavailable; the tool was not dispatched.", "runtime")
	}
	defer func() {
		binding := state.binding
		if binding.Validate() != nil {
			binding = initial
		}
		if recovered := recover(); recovered != nil {
			result = nil
			returnErr = r.executionError(toolError("TOOL_PANIC", "Tool handler panicked; the side-effect result is unknown. Verify it before retrying.", "runtime"), state)
			if auditErr := r.appendExecution(activity.Event{Binding: binding, Kind: "call.completed", ToolName: spec.Name, Status: "unknown", ErrorCode: "TOOL_PANIC", Summary: returnErr.Error()}); auditErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("panic outcome could not be persisted: %w", auditErr))
			}
		}
		redactor := r.executionRedactor(original)
		// SDK and Bridge adapters persist the final envelope once, after adding the
		// service catalog and trusted user insertions. Direct Runtime callers have
		// no adapter and retain their actual returned result here.
		if !bindResponseAudit(ctx, binding, spec.Name, redactor, received, rpcReturnStatus(result, returnErr)) {
			value := map[string]any{"result": result, "isError": returnErr != nil || resultReportsFailure(result)}
			if returnErr != nil {
				value["error"] = returnErr.Error()
			}
			r.recordExecutionPayload(binding, spec.Name, "response", value, redactor)
			if auditErr := r.recordRPCReturn(binding, spec.Name, received, result, returnErr); auditErr != nil {
				if returnErr != nil {
					returnErr = errors.Join(returnErr, fmt.Errorf("RPC audit persistence failed; verify side effects before retrying: %w", auditErr))
				} else {
					if result == nil {
						result = Result{}
					}
					result["activity_warning"] = "RPC returned, but its timing record could not be saved. Verify side effects before retrying."
				}
			}
		}
	}()
	fail := func(failure error) (Result, error) {
		event := activity.Event{Binding: state.binding, Kind: "call.completed", Status: "failed", ToolName: spec.Name, Title: spec.Title, ElapsedMS: time.Since(state.started).Milliseconds(), Summary: r.executionRedactor(original).Text(failure.Error(), 4096)}
		if event.Binding.Validate() != nil {
			event.Binding = initial
		}
		var toolErr *ToolError
		if errors.As(failure, &toolErr) {
			event.ErrorCode = toolErr.Code
		}
		if spec.Name == "file_edit" {
			event.FileEdit = r.fileEditDetails(original, nil, state, failure)
		}
		if auditErr := r.appendExecution(event); auditErr != nil {
			failure = errors.Join(failure, fmt.Errorf("rejected call could not be persisted: %w", auditErr))
		}
		return nil, r.executionError(failure, state)
	}
	if resolveErr != nil {
		if errors.Is(resolveErr, activity.ErrConversationDeleted) {
			return fail(toolError("CONVERSATION_TERMINATED", activity.ConversationTerminatedMessage, "permission"))
		}
		return fail(toolError("CONVERSATION_BINDING_ERROR", resolveErr.Error(), "validation"))
	}
	if err = r.checkConversationGate(ctx, snapshot.ConversationID); err != nil {
		return fail(err)
	}
	if _, supplied := original["conversation_id"]; supplied {
		return fail(toolError("INVALID_ARGUMENT", "conversation_id is transport-owned and is not a tool argument", "validation"))
	}
	if rejection, _ := ctx.Value(rejectedExecutionKey{}).(string); rejection != "" {
		return fail(toolError("INVALID_ARGUMENT", rejection, "validation"))
	}
	if err = r.validateToolArguments(spec.Name, original); err != nil {
		return fail(err)
	}
	if spec.Handler == nil {
		return fail(toolError("UNKNOWN_TOOL", "tool has no handler", "validation"))
	}
	args, err := cloneExecutionArguments(original)
	if err != nil {
		return fail(toolError("INVALID_ARGUMENT", "tool arguments must be JSON-compatible", "validation"))
	}
	if spec.Name != "insertion_ack" {
		r.reserveInsertion(ctx, snapshot, received)
	}
	if spec.Name == "task_manage" && (stringArg(args, "action") == "create" || stringArg(args, "workspace_id") != "") {
		workspaceID := stringArg(args, "workspace_id")
		if workspaceID == "" && stringArg(args, "project") == "" {
			workspaceID = snapshot.WorkspaceID
		}
		selected, selectErr := r.workspaceRegistry.Select(ctx, workspaceID, stringArg(args, "project"))
		if selectErr != nil {
			return fail(workspaceFailure(selectErr, initial, nil))
		}
		args["workspace_id"] = selected.ID
	}
	snapshot.RetryOfCallID = stringArg(args, "retry_of_call_id")
	if snapshot.RetryOfCallID != "" {
		previous, lookupErr := r.activity.Call(ctx, snapshot.RetryOfCallID)
		if lookupErr != nil {
			return fail(toolError("INVALID_RETRY", "retry_of_call_id was not found", "validation"))
		}
		if previous.ConversationID != initial.ConversationID || previous.SourceOwnerKey != "" && previous.SourceOwnerKey != initial.SourceOwnerKey || previous.Status == "unknown" || !activity.CallTerminal(previous.Status) {
			return fail(toolError("INVALID_RETRY", "Retry must reference a terminal, confirmed call in the same conversation. Verify unknown side effects before issuing a new request.", "validation"))
		}
	}
	ctx = activity.WithExecutionScope(ctx, snapshot)
	state, err = r.prepareObservedExecution(ctx, spec.Name, args)
	state.started = received
	if err != nil {
		return fail(err)
	}
	r.verifyInsertionTarget(ctx, state.binding)
	if state.binding.Label == "" {
		state.binding.Label = spec.Title
	}
	if err = r.validateSessionOwnership(ctx, spec.Name, args, state.binding); err != nil {
		return fail(err)
	}
	if err = r.conversations.Link(ctx, state.binding.ConversationID, state.binding.TaskID, state.binding.WorkspaceID); err != nil {
		return fail(err)
	}
	r.updateConversationName(ctx, state.binding.ConversationID, spec.Name, args)
	description := r.describeExecution(spec.Name, args, state)
	if err = r.appendExecution(activity.Event{Binding: state.binding, Kind: "call.bound", ToolName: spec.Name, Title: description, ParameterSummary: r.executionParameters(args), DisplayCommand: r.executionRedactor(args).Text(stringArg(args, "cmd"), 4096), Summary: description}); err != nil {
		return fail(err)
	}
	if spec.Name == "file_edit" {
		if err = r.appendExecution(activity.Event{Binding: state.binding, Kind: "file.requested", ToolName: spec.Name, FileEdit: r.fileEditDetails(args, nil, state, nil)}); err != nil {
			return fail(err)
		}
	}
	r.executionMu.Lock()
	if err = r.executionAdmissionLocked(ctx, state.binding, spec.Name, stringArg(args, "action"), callID); err != nil {
		r.executionMu.Unlock()
		return fail(err)
	}
	decision, err := r.permissions.Decide(ctx, r.executionFacts(spec.Name, args, state))
	if err != nil {
		r.executionMu.Unlock()
		return fail(err)
	}
	if local, _ := ctx.Value(localUserActionKey{}).(bool); local && decision.Effect == permission.Ask {
		decision.Effect, decision.RuleID, decision.Reason = permission.Allow, "local-user-action", "已认证本地控制面板明确发起的固定管理操作。"
	}
	prepared := &preparedExecution{spec: spec, args: args, state: state, source: activity.SourceFromContext(ctx), decision: decision, outputPolicy: outputPolicy}
	if decision.Effect == permission.Deny {
		r.executionMu.Unlock()
		return fail(toolErrorDetails("PERMISSION_DENIED", decision.Reason, "permission", map[string]any{"rule_id": decision.RuleID, "mode": decision.Mode, "permission": decision, "executed": false}))
	}
	if spec.Name == "mcp_tool_call" {
		prepared.mcpTarget, err = r.dynamicMCP.PermissionTargetFingerprint(ctx, stringArg(args, "name"))
		if err != nil {
			r.executionMu.Unlock()
			return fail(err)
		}
	}
	if err = r.freezeSessionSelection(ctx, prepared); err != nil {
		r.executionMu.Unlock()
		return fail(err)
	}
	if decision.Effect == permission.Ask {
		if len(r.pendingCalls) >= 128 {
			r.executionMu.Unlock()
			return fail(toolError("APPROVAL_LIMIT", "Too many pending approvals; resolve or reject existing requests first.", "resource_limit"))
		}
		operation := description
		if command := stringArg(args, "cmd"); command != "" {
			operation = command
		}
		redactor := r.executionRedactor(args)
		a := permission.Approval{Reviewer: decision.Settings.Reviewer, Settings: &decision.Settings, Binding: state.binding, Tool: spec.Name, Action: stringArg(args, "action"), Operation: redactor.Text(operation, 16384), ScopeDescription: redactor.Text(r.executionScope(prepared), 4096), RuleID: decision.RuleID, Reason: decision.Reason, Mode: decision.Mode, PolicyRevision: decision.Revision}
		if state.selected != nil {
			a.WorkspaceRevision = state.selected.RulesRevision
		}
		approval, createErr := r.permissions.Create(ctx, a)
		if createErr != nil {
			r.executionMu.Unlock()
			return fail(createErr)
		}
		prepared.approvalID = approval.ID
		r.pendingCalls[approval.ID] = prepared
		err = r.appendExecution(activity.Event{Binding: state.binding, Kind: "call.pending", Status: "pending_approval", ToolName: spec.Name, Title: description, ApprovalID: approval.ID, RuleID: decision.RuleID, PermissionMode: decision.Mode, Summary: decision.Reason})
		if err != nil {
			delete(r.pendingCalls, approval.ID)
			_, _ = r.permissions.Settle(ctx, approval.ID, "expired", "执行日志不可写，原操作未派发。")
		}
		r.executionMu.Unlock()
		if err != nil {
			return fail(err)
		}
		if decision.Settings.Reviewer == permission.ReviewerAuto {
			return r.autoReviewPrepared(ctx, prepared, approval)
		}
		return r.decorateExecution(Result{"status": "pending_approval", "executed": false, "approval_id": approval.ID, "approval": approval, "next_required_action": "Wait for the local user to approve or reject this fixed request. Do not change arguments or retry to bypass approval."}, prepared), nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.activeCalls[callID] = &liveExecution{binding: state.binding, cancel: cancel, source: prepared.source}
	r.executionWG.Add(1)
	r.executionMu.Unlock()
	return r.executePrepared(runCtx, prepared)
}

func cloneExecutionArguments(input map[string]any) (map[string]any, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}
func (r *Runtime) appendExecution(event activity.Event) error {
	return r.appendExecutions(event)
}
func (r *Runtime) appendExecutions(events ...activity.Event) error {
	for index := range events {
		if events[index].OwnerInstance == "" {
			events[index].OwnerPID = os.Getpid()
			events[index].OwnerInstance = r.executionInstance
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.activity.AppendBatch(ctx, events)
	return err
}
func (r *Runtime) executionError(err error, state executionObservation) error {
	var toolErr *ToolError
	if errors.Is(err, activity.ErrAppendCapacity) {
		message := "The activity journal has no admission capacity. No tool handler was dispatched."
		if state.executed {
			message = "Activity capacity was exhausted after dispatch. Inspect the recorded outcome before retrying."
		}
		toolErr = &ToolError{Code: "ACTIVITY_CAPACITY", Message: message, Category: "resource_limit", Details: map[string]any{"executed": state.executed}}
	}
	if toolErr == nil && !errors.As(err, &toolErr) {
		toolErr = &ToolError{Code: "EXECUTION_FAILED", Message: err.Error(), Category: "runtime"}
	}
	copy := *toolErr
	copy.Details = maps.Clone(toolErr.Details)
	if copy.Details == nil {
		copy.Details = map[string]any{}
	}
	copy.Details["call_id"] = state.binding.CallID
	if state.binding.ConversationID != "" {
		copy.Details["conversation_id"] = state.binding.ConversationID
	}
	if toolErr.Category != "validation" || state.binding.TaskID != "" || state.binding.WorkspaceID != "" {
		copy.Details["agentdock_guidance"] = r.executionGuidance("", state, nil, true)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(&copy, err)
	}
	return &copy
}
func (r *Runtime) executionRedactor(args map[string]any) activity.Redactor {
	values := []string{r.cfg.AuthToken, r.cfg.NexusDeviceToken}
	if env, ok := args["env"].(map[string]any); ok {
		for _, value := range env {
			if text, ok := value.(string); ok && text != "" {
				values = append(values, text)
			}
		}
	}
	if value := stringArg(args, "value"); value != "" {
		values = append(values, value)
	}
	return activity.NewRedactor(values...)
}
func (r *Runtime) describeExecution(name string, args map[string]any, state executionObservation) string {
	action := stringArg(args, "action")
	if name == "mcp_tool_call" {
		return r.executionRedactor(args).Text(stringArg(args, "name"), 512)
	}
	descriptions := map[string]string{"agentdock_context": "加载上下文", "workspace_context": "读取工作区规则", "read_file": "读取文件", "list_dir": "列出目录", "search_text": "搜索文本", "exec_command": "运行命令", "file_edit": "file_edit", "task_manage": "任务管理", "workspace_manage": "工作区管理", "mcp_tool_search": "发现动态工具", "mcp_tool_list": "读取工具目录", "mcp_tool_inspect": "加载工具 Schema", "mcp_tool_call": "调用动态工具", "plugin_load": "展开插件", "session_observe": "查看命令会话", "session_act": "控制命令会话"}
	title := descriptions[name]
	if title == "" {
		title = name
	}
	if action != "" {
		title += " · " + action
	}
	for _, key := range []string{"path", "name", "query", "session_id"} {
		if value := stringArg(args, key); value != "" {
			title += " · " + value
			break
		}
	}
	return r.executionRedactor(args).Text(title, 512)
}

// Persist only bounded operational selectors, not file bodies, credentials or
// arbitrary third-party nested data. The fixed request stays in memory.
func (r *Runtime) executionParameters(args map[string]any) string {
	selected := map[string]any{}
	for _, key := range []string{"action", "name", "path", "new_path", "workdir", "query", "cmd", "session_id", "runtime", "target_kind", "dry_run", "timeout_ms"} {
		if value, found := args[key]; found {
			selected[key] = value
		}
	}
	if nested, ok := args["arguments"].(map[string]any); ok {
		selected["argument_count"] = len(nested)
	}
	for _, key := range []string{"content", "patch", "stdin"} {
		if value, ok := args[key].(string); ok {
			selected[key+"_bytes"] = len(value)
		}
	}
	raw, _ := json.Marshal(selected)
	return r.executionRedactor(args).Text(string(raw), 4096)
}

func (r *Runtime) executionScope(p *preparedExecution) string {
	scope := "当前进程操作系统账户权限；此模式不提升权限，也不限制任意命令内部的文件访问。"
	if p.sessionIDs != nil {
		scope += "\n本次停止的固定会话集合：" + fmt.Sprint(p.sessionIDs)
	}
	if p.state.selected != nil {
		scope = "工作区：" + p.state.selected.Root + "\n" + scope
	}
	if p.state.target != nil {
		scope += "\n目标：" + p.state.target.ResolvedPath + "（" + p.state.target.Kind + "）"
	}
	if p.spec.Name == "mcp_tool_call" {
		scope += "\n第三方 MCP 的内部副作用由该服务实现，未将其注释当作可信只读授权。"
	}
	return scope
}
func (r *Runtime) executionFacts(name string, args map[string]any, state executionObservation) permission.Facts {
	f := permission.Facts{Binding: state.binding, Tool: name, Action: stringArg(args, "action")}
	switch name {
	case "insertion_ack":
		// A receipt cannot change tasks, commands, permissions or user text.
		f.ReadOnly = true
	case "agentdock_context", "workspace_context", "read_file", "list_dir", "search_text", "view_image", "mcp_tool_search", "mcp_tool_list", "mcp_tool_inspect", "plugin_load", "session_observe", "browser_snapshot":
		f.ReadOnly = true
	case "task_manage":
		switch f.Action {
		case "list", "get", "thread_list", "thread_get":
			f.ReadOnly = true
		case "create", "set_current", "unbind", "checkpoint", "block", "resume", "final_review", "complete", "archive", "unarchive", "cancel", "thread_create", "thread_fork", "thread_switch", "thread_checkpoint", "thread_block", "thread_resume", "thread_close":
			f.Management = true
		}
	case "workspace_manage":
		f.ReadOnly = f.Action == "list" || f.Action == "get" || f.Action == "resolve"
	case "plugin_manage", "mcp_manage":
		f.ReadOnly = f.Action == "list" || f.Action == "inspect" || f.Action == "env_list"
	case "skill_package":
		f.ReadOnly = f.Action == "env_list"
	case "session_act":
		f.Management = f.Action == "kill"
		f.Reason = "向命令进程写入数据或批量停止可能改变任务执行，需要确认。"
	case "exec_command":
		f.Reason = "任意命令可能修改文件、安装软件、访问网络或控制服务，需要确认实际命令与工作目录。"
	case "file_edit":
		f.ReadOnly = args["dry_run"] == true
		f.Reason = "文件变更将在指定目标执行；请确认修改内容、删除范围及工作区。"
	}
	return r.profileFacts(f, args, state)
}
func (r *Runtime) executionAdmissionLocked(ctx context.Context, binding activity.Binding, name, action, exclude string) error {
	if err := r.checkConversationGate(ctx, binding.ConversationID); err != nil {
		return err
	}
	r.lifecycleMu.RLock()
	closing := r.closing
	r.lifecycleMu.RUnlock()
	if closing {
		return toolError("RUNTIME_CLOSING", "AgentDock runtime is shutting down", "runtime")
	}
	if binding.TaskID != "" {
		task, err := r.tasks.Get(binding.TaskID)
		if err != nil {
			return err
		}
		if task.TrashedAt != nil {
			return toolError("TASK_TRASHED", "Restore this task from the recycle bin before executing it.", "validation")
		}
		if name == "task_manage" && action == "cancel" && r.taskHasActivityLocked(binding.TaskID, exclude) {
			return toolError("TASK_HAS_ACTIVE_EXECUTIONS", "Stop active commands and reject pending approvals before cancelling this task.", "conflict")
		}
	}
	return nil
}
func (r *Runtime) taskHasActivityLocked(taskID, exclude string) bool {
	for id, live := range r.activeCalls {
		if id != exclude && live.binding.TaskID == taskID {
			return true
		}
	}
	for _, pending := range r.pendingCalls {
		if pending.state.binding.CallID != exclude && pending.state.binding.TaskID == taskID {
			return true
		}
	}
	if r.command.TaskActivityRunning(taskID) {
		return true
	}
	for _, status := range []string{"created", "running", "pending_approval"} {
		page, err := r.activity.Calls(context.Background(), activity.CallQuery{TaskID: taskID, Status: status, Limit: 2})
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
func (r *Runtime) validateSessionOwnership(ctx context.Context, name string, args map[string]any, binding activity.Binding) error {
	if name != "session_observe" && name != "session_act" {
		return nil
	}
	id := stringArg(args, "session_id")
	if id == "" {
		return nil
	}
	original, found := r.command.SessionBinding(id)
	if !found {
		return nil
	}
	if original.SourceOwnerKey != "" && original.SourceOwnerKey != activity.SourceOwnerKey(ctx) {
		return toolError("SESSION_OWNER_MISMATCH", "This command belongs to another authenticated client.", "permission")
	}
	if original.ConversationID == "" {
		return nil
	}
	if err := r.conversations.Owns(ctx, original.ConversationID); err != nil {
		return toolError("SESSION_OWNER_MISMATCH", "This command belongs to another authenticated client.", "permission")
	}
	if binding.ConversationID == original.ConversationID || binding.TaskID != "" && binding.TaskID == original.TaskID {
		return nil
	}
	return toolError("SESSION_CONVERSATION_MISMATCH", "Resume the owning task before accessing this command from another conversation. UI selection never redirects command sessions.", "permission")
}

func (r *Runtime) executePrepared(ctx context.Context, p *preparedExecution) (result Result, returnErr error) {
	defer r.executionWG.Done()
	ctx, finishProgress := r.observeExecutionProgress(ctx, p.state.binding, p.spec.Name, r.executionRedactor(p.args))
	defer finishProgress()
	defer func() {
		// The approval response has already returned. Preserve the eventual
		// outcome of the fixed approved operation on its original root call.
		if p.approvalID != "" && !p.approvalInline {
			value := map[string]any{"result": result, "isError": returnErr != nil || resultReportsFailure(result)}
			if returnErr != nil {
				value["error"] = returnErr.Error()
			}
			r.recordExecutionPayload(p.state.binding, p.spec.Name, "response", value, r.executionRedactor(p.args))
		}
	}()
	defer func() {
		r.executionMu.Lock()
		if live := r.activeCalls[p.state.binding.CallID]; live != nil {
			live.cancel()
			delete(r.activeCalls, p.state.binding.CallID)
		}
		r.executionMu.Unlock()
	}()
	state := p.state
	completion, reserveErr := r.activity.ReserveAppend(ctx, 1)
	if reserveErr != nil {
		return r.finishPrepared(p, Result{"executed": false}, reserveErr, "failed")
	}
	p.completion = completion
	defer completion.Close()
	defer func() {
		if recover() != nil {
			failure := toolError("TOOL_PANIC", "The tool terminated unexpectedly. Inspect its side-effect state before retrying.", "runtime")
			result, returnErr = r.finishPrepared(p, nil, failure, "unknown")
		}
	}()
	ctx = activity.WithSource(ctx, p.source)
	ctx = activity.WithBinding(ctx, state.binding)
	ctx = context.WithValue(ctx, outputPolicyContextKey{}, p.outputPolicy)
	if p.decision.Settings.Profile.Restricted() {
		ctx = toolfile.WithRestrictedFileTools(ctx)
	}
	if p.mcpTarget != "" {
		ctx = mcpclient.WithApprovedToolTarget(ctx, p.mcpTarget)
	}
	started := activity.Event{Binding: state.binding, Kind: "call.started", Status: "running", ToolName: p.spec.Name, Title: r.describeExecution(p.spec.Name, p.args, state), ApprovalID: p.approvalID, RuleID: p.decision.RuleID, PermissionMode: p.decision.Mode}
	if err := ctx.Err(); err != nil {
		return r.finishPrepared(p, nil, err, "cancelled")
	}
	if err := r.appendExecution(started); err != nil {
		return r.finishPrepared(p, nil, err, "failed")
	}
	// Only execution tools persist a branch binding. Inspecting a completed task
	// or switching the viewed branch must not change its continuation point.
	if state.binding.TaskID != "" && (p.spec.Name == "exec_command" || p.spec.Name == "file_edit" || p.spec.Name == "mcp_tool_call" || p.spec.Name == "browser_act") {
		if _, err := r.taskTools.ResolveBinding(state.binding, true); err != nil {
			return r.finishPrepared(p, nil, err, "failed")
		}
	}
	if err := r.revalidatePrepared(ctx, p); err != nil {
		return r.finishPrepared(p, nil, err, "failed")
	}
	if state.target != nil && p.spec.Name == "exec_command" {
		target, err := workspace.ResolveCommandDirectory(*state.selected, workspace.TargetRequest{Kind: state.target.Kind, Path: state.target.ResolvedPath, TaskID: state.binding.TaskID, ExternalPath: stringArg(p.args, "external_path")})
		if err != nil {
			return r.finishPrepared(p, nil, err, "failed")
		}
		if target.ResolvedPath != state.target.ResolvedPath {
			return r.finishPrepared(p, nil, errors.New("the execution target changed after approval"), "failed")
		}
	}
	var err error
	handlerStarted := time.Now()
	waitMS := handlerStarted.Sub(p.state.started).Milliseconds()
	p.state.waitMS, p.state.executed = &waitMS, true
	if p.spec.Name == "session_act" && stringArg(p.args, "action") == "kill_all" {
		result, err = r.executeSessionSelection(ctx, p)
	} else {
		result, err = p.spec.Handler(ctx, r, p.args)
	}
	executionMS := time.Since(handlerStarted).Milliseconds()
	p.state.executionMS = &executionMS
	if p.spec.Name == "file_edit" && err == nil {
		r.recordFileChanges(p.args, result, state)
	}
	if err == nil && !resultReportsFailure(result) {
		if p.spec.Name == "agentdock_context" {
			err = r.finalizeContextBinding(ctx, p, result)
		} else {
			r.commitConversationState(ctx, p, result)
		}
		if err == nil {
			r.updateConversationName(ctx, state.binding.ConversationID, p.spec.Name, p.args)
		}
	}
	if p.spec.Name == "session_observe" && (stringArg(p.args, "action") == "list" || stringArg(p.args, "action") == "") {
		result = r.filterSessionList(ctx, result, p.state.binding)
	}
	if p.spec.Name == "exec_command" && err == nil && stringArg(result, "session_id") != "" {
		phase := activity.Event{Binding: state.binding, Kind: "call.phases", ToolName: p.spec.Name}
		phase.ExecutionElapsedMS, phase.WaitElapsedMS = &executionMS, &waitMS
		if auditErr := r.appendExecution(phase); auditErr != nil {
			result["activity_warning"] = "Command returned but dispatch phase measurements could not be persisted."
		}
		// Command activity owns stdout/stderr and completion, including asynchronous
		// exit. Do not produce a second success event when the process is still running.
		if p.approvalID != "" {
			r.watchApprovalCommand(p.approvalID, state.binding.CallID, stringArg(result, "session_id"))
		}
		return r.decorateExecution(result, p), nil
	}
	status := "succeeded"
	if err != nil || resultReportsFailure(result) {
		status = "failed"
	}
	if result != nil && stringArg(result, "status") == "partial" {
		status = "partial"
	}
	if errors.Is(err, context.Canceled) {
		status = "cancelled"
	}
	return r.finishPrepared(p, result, err, status)
}
func (r *Runtime) finishPrepared(p *preparedExecution, result Result, err error, status string) (Result, error) {
	summary := r.describeExecution(p.spec.Name, p.args, p.state)
	if err != nil {
		summary = r.executionRedactor(p.args).Text(err.Error(), 4096)
	} else {
		if p.spec.Name == "task_manage" && stringArg(p.args, "summary") != "" {
			summary = r.executionRedactor(p.args).Text(stringArg(p.args, "summary"), 4096)
		}
		for _, key := range []string{"count", "matches", "total_matches", "files_changed", "insertions", "deletions"} {
			if value, ok := result[key]; ok {
				switch v := value.(type) {
				case int, int64, float64:
					summary += fmt.Sprintf(" · %s=%v", key, v)
				}
			}
		}
	}
	event := activity.Event{Binding: p.state.binding, Kind: "call.completed", Status: status, ToolName: p.spec.Name, Title: r.describeExecution(p.spec.Name, p.args, p.state), ApprovalID: p.approvalID, PermissionMode: p.decision.Mode, RuleID: p.decision.RuleID, ElapsedMS: time.Since(p.state.started).Milliseconds(), Summary: summary}
	event.OperationElapsedMS = &event.ElapsedMS
	event.ExecutionElapsedMS, event.WaitElapsedMS = p.state.executionMS, p.state.waitMS
	if p.spec.Name == "file_edit" {
		event.FileEdit = r.fileEditDetails(p.args, result, p.state, err)
	}
	var te *ToolError
	if errors.As(err, &te) {
		event.ErrorCode = te.Code
	}
	if p.state.target != nil {
		event.Workdir = p.state.target.ResolvedPath
		event.Runtime = p.state.target.Runtime
	}
	if persistErr := r.appendReservedExecution(p.completion, event); persistErr != nil {
		if result == nil {
			result = Result{}
		}
		result["activity_warning"] = "The actual tool returned, but its final journal event could not be persisted. Verify side effects before retrying."
	}
	if p.approvalID != "" {
		final := status
		if final == "partial" {
			final = "failed"
		}
		_, _ = r.permissions.Settle(context.Background(), p.approvalID, final, summary)
	}
	if err != nil {
		if result != nil {
			return r.decorateExecution(result, p), r.executionError(err, p.state)
		}
		return nil, r.executionError(err, p.state)
	}
	return r.decorateExecution(result, p), nil
}
func (r *Runtime) decorateExecution(result Result, p *preparedExecution) Result {
	r.rememberCapabilityAccess(p.state.binding, p.spec.Name, p.args, result)
	decorated := maps.Clone(result)
	if decorated == nil {
		decorated = Result{}
	}
	for key, value := range bindingArguments(p.state.binding) {
		if value != "" {
			// Context reports its selected scope; the immutable root retains the
			// entry scope in the journal, even when this call changes continuation.
			if key == "workspace_id" && p.spec.Name == "agentdock_context" && stringArg(result, key) != "" {
				continue
			}
			decorated[key] = value
		}
	}
	if p.state.target != nil {
		decorated["workspace_target"] = *p.state.target
	}
	if p.approvalID != "" {
		decorated["approval_id"] = p.approvalID
	}
	decorated["permission"] = p.decision
	decorated["agentdock_guidance"] = r.executionGuidance(p.spec.Name, p.state, result, false)
	decorated["binding_quality"] = p.state.binding.BindingQuality
	return r.applyToolOutputPolicy(p, decorated)
}
func (r *Runtime) watchApprovalCommand(approvalID, callID, sessionID string) {
	go func() {
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for {
			call, err := r.activity.Call(context.Background(), callID)
			if err == nil && activity.CallTerminal(call.Status) {
				status := call.Status
				if status == "partial" {
					status = "failed"
				}
				_, _ = r.permissions.Settle(context.Background(), approvalID, status, call.Summary)
				return
			}
			select {
			case <-r.commandCtx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

type rejectedExecutionKey struct{}

// RejectToolCall lets the transport retain malformed parameter attempts before
// any valid business arguments exist. It can never reach a tool handler.
func (r *Runtime) RejectToolCall(ctx context.Context, name, message string) (Result, error) {
	return r.Call(context.WithValue(ctx, rejectedExecutionKey{}, message), name, map[string]any{})
}
