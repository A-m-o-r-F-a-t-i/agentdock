package app

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	toolcommand "github.com/uvwt/agentdock/internal/tool/command"
	"github.com/uvwt/agentdock/internal/workspace"
)

func (r *Runtime) filterSessionList(ctx context.Context, result Result, binding activity.Binding) Result {
	if result == nil {
		return nil
	}
	filtered := maps.Clone(result)
	items, _ := result["sessions"].([]map[string]any)
	visible := []map[string]any{}
	for _, item := range items {
		if err := r.validateSessionOwnership(ctx, "session_observe", map[string]any{"session_id": stringArg(item, "session_id")}, binding); err == nil {
			visible = append(visible, item)
		}
	}
	filtered["sessions"], filtered["count"] = visible, len(visible)
	return filtered
}
func (r *Runtime) freezeSessionSelection(ctx context.Context, p *preparedExecution) error {
	if p.spec.Name != "session_act" || stringArg(p.args, "action") != "kill_all" {
		return nil
	}
	result, err := r.command.Observe(toolcommand.SessionObserveRequest{Action: "list"})
	if err != nil {
		return err
	}
	result = r.filterSessionList(ctx, result, p.state.binding)
	items, _ := result["sessions"].([]map[string]any)
	p.sessionIDs = []string{}
	for _, item := range items {
		p.sessionIDs = append(p.sessionIDs, stringArg(item, "session_id"))
	}
	return nil
}
func (r *Runtime) executeSessionSelection(ctx context.Context, p *preparedExecution) (Result, error) {
	results := []map[string]any{}
	failed := 0
	for _, id := range p.sessionIDs {
		if err := ctx.Err(); err != nil {
			return Result{"status": "partial", "sessions": results, "failed": failed}, err
		}
		result, err := r.command.Act(toolcommand.SessionActRequest{Action: "kill", SessionID: id})
		if err != nil {
			failed++
			results = append(results, map[string]any{"session_id": id, "status": "failed", "error": err.Error()})
		} else {
			results = append(results, result)
		}
	}
	status := "succeeded"
	if failed > 0 {
		status = "partial"
	}
	return Result{"status": status, "sessions": results, "count": len(results), "failed": failed, "scope": "frozen_current_conversation_or_explicit_task"}, nil
}
func (r *Runtime) revalidatePrepared(ctx context.Context, p *preparedExecution) error {
	if p.mcpTarget != "" {
		current, err := r.dynamicMCP.PermissionTargetFingerprint(ctx, stringArg(p.args, "name"))
		if err != nil {
			return err
		}
		if current != p.mcpTarget {
			return errors.New("MCP target configuration changed after request preparation")
		}
	}
	if p.state.selected == nil {
		return nil
	}
	current, err := r.workspaceRegistry.Select(ctx, p.state.selected.ID, "")
	if err != nil {
		return err
	}
	if current.RulesRevision != p.state.selected.RulesRevision {
		return errors.New("workspace rules changed after request preparation")
	}
	if p.state.target == nil {
		return nil
	}
	target := p.state.target
	check, err := workspace.ResolveTarget(current, workspace.TargetRequest{Kind: target.Kind, Path: target.ResolvedPath, TaskID: p.state.binding.TaskID, ExternalPath: stringArg(p.args, "external_path")})
	if err != nil {
		return err
	}
	if check.ResolvedPath != target.ResolvedPath {
		return errors.New("the execution target changed after approval")
	}
	for resolved := range p.state.originals {
		check, err = workspace.ResolveTarget(current, workspace.TargetRequest{Kind: target.Kind, Path: resolved, TaskID: p.state.binding.TaskID, ExternalPath: stringArg(p.args, "external_path")})
		if err != nil {
			return err
		}
		if check.ResolvedPath != resolved {
			return errors.New("a file target changed after approval")
		}
	}
	return nil
}

// Stop requests during the command-start window wait for its real process
// registration. Cancelling only the foreground waiter would leave it running.
func (r *Runtime) stopStartingCommand(ctx context.Context, id string) (bool, error) {
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		if r.command.CallActivityRunning(id) {
			return r.command.StopActivityCall(ctx, id)
		}
		call, err := r.activity.Call(ctx, id)
		if err == nil && activity.CallTerminal(call.Status) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil
		case <-tick.C:
		}
	}
}
