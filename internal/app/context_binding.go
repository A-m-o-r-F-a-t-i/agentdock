package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/agentinstructions"
	"github.com/uvwt/agentdock/internal/workspace"
)

// Select before loading rules so all components observe the same target.
func (r *Runtime) selectContextWorkspace(ctx context.Context, workdir string) (workspace.Record, string, error) {
	if strings.TrimSpace(workdir) != "" {
		resolved, err := r.ws.ResolveExisting(workdir)
		if err != nil {
			return workspace.Record{}, "", toolError("INVALID_ARGUMENT", "instruction workdir must resolve to an existing host directory", "validation")
		}
		info, err := os.Stat(resolved.Abs)
		if err != nil || !info.IsDir() {
			return workspace.Record{}, "", toolError("INVALID_ARGUMENT", "instruction workdir must be a directory", "validation")
		}
		record, err := r.workspaceRegistry.EnsureRoot(ctx, resolved.Abs)
		return record, resolved.Abs, err
	}
	bound := activity.FromContext(ctx).WorkspaceID
	record, err := r.workspaceRegistry.Select(ctx, bound, "")
	if err != nil {
		return workspace.Record{}, "", workspaceFailure(err, activity.FromContext(ctx), nil)
	}
	if record.Runtime != workspace.HostRuntime() {
		return workspace.Record{}, "", toolError("CONTEXT_WORKSPACE_RUNTIME", "The selected workspace requires its own runtime for instruction loading; native default rules were not substituted.", "runtime")
	}
	return record, filepath.Join(record.Root, record.DefaultWorkdir), nil
}

// This is the final context assembly boundary. It never changes p.state or the
// root's historical binding. Complete is published only after coordination.
func (r *Runtime) finalizeContextBinding(ctx context.Context, p *preparedExecution, result Result) error {
	diagnostics, ok := result["context_diagnostics"].(map[string]any)
	if !ok {
		return toolError("CONTEXT_INCONSISTENT", "Context diagnostics are missing.", "runtime")
	}
	diagnostics["complete"] = false
	result["binding_updated"] = false
	fail := func(code, message, category string, cause error) error {
		diagnostics["binding_status"] = "failed"
		if category == "conflict" {
			diagnostics["binding_status"] = "conflict"
		}
		result["binding_warning"] = message
		err := toolError(code, message, category)
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			return errors.Join(err, cause)
		}
		return err
	}
	var selected workspace.Record
	var instructions agentinstructions.Snapshot
	if remarshal(result["workspace"], &selected) != nil || selected.ID == "" ||
		remarshal(result["instruction_files"], &instructions) != nil {
		return fail("CONTEXT_INCONSISTENT", "Context target or instruction snapshot is missing.", "runtime", nil)
	}
	result["workspace_id"] = selected.ID
	relative, err := filepath.Rel(selected.Root, instructions.Workdir)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fail("CONTEXT_INCONSISTENT", "Instruction workdir does not belong to the selected workspace.", "runtime", err)
	}
	if diagnostics["components_complete"] != true || !instructionSnapshotComplete(instructions) {
		diagnostics["binding_status"] = "context_incomplete"
		return nil
	}
	current, err := r.workspaceRegistry.Select(ctx, selected.ID, "")
	if err != nil {
		return fail("CONTEXT_WORKSPACE_CHANGED", "The selected workspace could not be revalidated.", "conflict", err)
	}
	if current != selected {
		return fail("CONTEXT_WORKSPACE_CHANGED", "The workspace changed while context was being prepared.", "conflict", nil)
	}
	scope := p.state.entryBinding
	if scope.ConversationID == "" {
		diagnostics["binding_status"] = "unattributed"
		diagnostics["complete"] = true
		result["binding_warning"] = "Host conversation metadata is unavailable; this result is request-local. Use explicit paths for subsequent calls. No conversation binding was inferred."
		return nil
	}
	next := activity.ConversationState{ActiveTaskID: scope.TaskID, ActiveTaskThreadID: scope.ThreadID, WorkspaceID: selected.ID}
	state, changed, err := r.conversations.ConvergeBinding(ctx, scope.ConversationID, scope.BindingRevision, next)
	if err != nil {
		if errors.Is(err, activity.ErrConversationConflict) {
			return fail("CONTEXT_BINDING_CONFLICT", "Another request selected a different continuation. This context was not committed; inspect the intended workspace before continuing.", "conflict", err)
		}
		return fail("CONTEXT_BINDING_FAILED", "Context continuation could not be committed: "+err.Error(), "runtime", err)
	}
	result["binding_updated"], result["conversation_state"] = changed, state
	diagnostics["binding_status"] = "unchanged"
	if changed {
		diagnostics["binding_status"] = "updated"
	}
	diagnostics["complete"] = true
	return nil
}
