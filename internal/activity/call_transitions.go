package activity

import "strings"

// Bindings and their indices are coordinated at one projection boundary.
func (p *callProjection) applyCallBinding(call *ExecutionCall, event Event, id string) bool {
	oldConversation, oldTask := call.ConversationID, call.TaskID
	// A created task or resolved branch may become known after the first event.
	// Nonempty identities never migrate between conversations or tasks.
	if call.ConversationID != "" && event.ConversationID != "" && call.ConversationID != event.ConversationID || call.TaskID != "" && event.TaskID != "" && call.TaskID != event.TaskID {
		call.HistoryIncomplete = true
		p.warning("Conflicting call bindings were rejected while rebuilding execution history.")
		return false
	}
	if call.ConversationID == "" {
		call.ConversationID = event.ConversationID
	}
	if call.TaskID == "" {
		call.TaskID = event.TaskID
	}
	if call.ThreadID != "" && event.ThreadID != "" && call.ThreadID != event.ThreadID || call.WorkspaceID != "" && event.WorkspaceID != "" && call.WorkspaceID != event.WorkspaceID {
		call.HistoryIncomplete = true
		p.warning("Conflicting task-thread or workspace bindings were rejected while rebuilding execution history.")
		return false
	}
	if call.ThreadID == "" {
		call.ThreadID = event.ThreadID
	}
	if call.StepID == "" {
		call.StepID = event.StepID
	}
	if call.WorkspaceID == "" {
		call.WorkspaceID = event.WorkspaceID
	}
	if call.ParentCallID == "" {
		call.ParentCallID = event.ParentCallID
	}
	if call.RetryOfCallID == "" {
		call.RetryOfCallID = event.RetryOfCallID
	}
	if call.Label == "" {
		call.Label = event.Label
	}
	if call.Title == "" || event.Kind == "call.bound" {
		call.Title = event.Title
	}
	if call.ToolName == "" {
		call.ToolName = event.ToolName
	}
	if oldConversation != call.ConversationID {
		delete(p.conversations[oldConversation], id)
	}
	if oldTask != call.TaskID {
		delete(p.tasks[oldTask], id)
	}
	callIndexAdd(p.conversations, call.ConversationID, id)
	callIndexAdd(p.tasks, call.TaskID, id)
	call.DisplayTitle, call.TaskThreadID = call.Title, call.ThreadID
	return true
}

// Runtime facts do not independently advance the call lifecycle.
func applyCallExecutionFacts(call *ExecutionCall, event Event) {
	if event.ParameterSummary != "" {
		call.ParameterSummary = event.ParameterSummary
	}
	if event.BindingQuality != "" {
		call.BindingQuality = event.BindingQuality
	}
	if event.Visibility != "" {
		call.Visibility = event.Visibility
	}
	call.UpdatedAt, call.UpdatedSeq = event.CreatedAt, event.Seq
	rpcAlreadyReturned := call.RPCCompletedAt != nil
	applyMeasurements(call, event)
	// Only genuine external-root execution facts advance activity. Projection reads,
	// metadata edits and recovery events must not restart an activity window.
	if call.RequestReceivedAt != nil && call.ParentCallID == "" && call.Visibility != "diagnostic" && genuineActivityEvent(event.Kind) {
		if call.LastActivityAt == nil || event.CreatedAt.After(*call.LastActivityAt) {
			call.LastActivityAt = copyValue(&event.CreatedAt)
		}
	}
	// Interaction is narrower than execution activity. It ends with the root
	// RPC return, so output from a detached/background process cannot keep the
	// two-minute conversation marker alive indefinitely.
	if call.RequestReceivedAt != nil && call.ParentCallID == "" && call.Visibility != "diagnostic" && genuineInteractionEvent(event.Kind, rpcAlreadyReturned) {
		interactionAt := event.CreatedAt
		if event.Kind == "call.created" {
			interactionAt = *call.RequestReceivedAt
		}
		if event.Kind == "call.rpc_returned" && call.RPCCompletedAt != nil {
			interactionAt = *call.RPCCompletedAt
		}
		if call.LastInteractionAt == nil || interactionAt.After(*call.LastInteractionAt) {
			call.LastInteractionAt = copyValue(&interactionAt)
		}
	}
	call.EventCount++
	if call.OwnerPID == 0 {
		call.OwnerPID = event.OwnerPID
		call.OwnerInstance = event.OwnerInstance
	}
	if event.SessionID != "" {
		call.SessionID = event.SessionID
	}
	if event.Runtime != "" {
		call.Runtime = event.Runtime
	}
	if event.Workdir != "" {
		call.Workdir = event.Workdir
	}
	if event.DisplayCommand != "" {
		call.DisplayCommand = event.DisplayCommand
	}
	if event.ApprovalID != "" {
		call.ApprovalID = event.ApprovalID
	}
	if event.RuleID != "" {
		call.RuleID = event.RuleID
	}
	if event.PermissionMode != "" {
		call.PermissionMode = event.PermissionMode
	}
	if event.ErrorCode != "" {
		call.ErrorCode = event.ErrorCode
	}
	if event.ElapsedMS > call.ElapsedMS {
		call.ElapsedMS = event.ElapsedMS
	}
	if event.Summary != "" && (call.Summary == "" || strings.HasPrefix(event.Kind, "call.") || event.Kind == "tool.completed" || event.Kind == "command.completed") {
		call.Summary = event.Summary
	}
	if event.ExitCode != nil {
		value := *event.ExitCode
		call.ExitCode = &value
	}
	if event.CommandOK != nil {
		value := *event.CommandOK
		call.CommandOK = &value
	}
	call.TimedOut = call.TimedOut || event.TimedOut
	if event.OutputPreview != "" {
		call.OutputPreview, call.StdoutTruncated = appendCallOutput(call.OutputPreview, event.OutputPreview, call.StdoutTruncated)
	}
	if event.StderrPreview != "" {
		call.StderrPreview, call.StderrTruncated = appendCallOutput(call.StderrPreview, event.StderrPreview, call.StderrTruncated)
	}
	call.StdoutTruncated = call.StdoutTruncated || event.StdoutTruncated
	call.StderrTruncated = call.StderrTruncated || event.StderrTruncated
	call.HasOutput = call.OutputSource != nil && call.OutputSource.Ref != "" || call.Response != nil && call.Response.State != "pending" || call.OutputPreview != "" || call.StderrPreview != "" || call.StdoutTruncated || call.StderrTruncated
	if event.Kind == "file.changed" {
		if len(call.FileChanges) < 128 {
			call.FileChanges = append(call.FileChanges, FileChange{Path: event.ResolvedPath, Insertions: event.Insertions, Deletions: event.Deletions, StatsKnown: event.ChangeStatsKnown})
		} else {
			call.ChangesTruncated = true
		}
	}
}

// A late asynchronous return must never resurrect an already terminal call.
func applyCallLifecycle(call *ExecutionCall, event Event, legacy, ambiguous bool) {
	var status string
	switch event.Kind {
	case "call.created":
		status = "created"
	case "call.pending":
		status = "pending_approval"
	case "call.started", "tool.started", "command.started":
		status = "running"
	case "call.completed", "call.recovered", "tool.completed", "command.completed":
		status = normalizedCallStatus(event.Status, event.CommandOK)
	}
	if legacy && (ambiguous || !CallTerminal(status)) {
		status = "unknown"
	}
	// An asynchronous return or late start event must not resurrect completion.
	if status != "" && (!CallTerminal(call.Status) || CallTerminal(status)) {
		call.Status = status
		if CallTerminal(status) {
			call.OutputSummary = call.Summary
			if status == "failed" || status == "unknown" {
				call.ErrorSummary = call.Summary
			}
			completed := event.CreatedAt
			call.CompletedAt = &completed
		}
	}
}
