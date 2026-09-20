package app

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/workspace"
)

const maxActivityDiffBytes = 256 << 10

type ActivityDiffRequest struct {
	TaskID, ThreadID string
	Seq              uint64
}

// Only a persisted event reference is accepted. Returned text is the current tracked
// working-tree diff, not a historical copy of the original modification.
func (r *Runtime) RuntimeActivityDiff(ctx context.Context, request ActivityDiffRequest) (Result, error) {
	if request.Seq == 0 || request.TaskID == "" || request.ThreadID == "" {
		return nil, toolError("INVALID_ARGUMENT", "task_id, thread_id and a positive event seq are required", "validation")
	}
	binding := activity.Binding{TaskID: request.TaskID, ThreadID: request.ThreadID}
	if _, err := r.taskTools.ResolveBinding(binding, false); err != nil {
		return nil, toolError("INVALID_ACTIVITY_BINDING", err.Error(), "validation")
	}
	page, err := r.activity.Query(ctx, activity.Query{TaskID: request.TaskID, ThreadID: request.ThreadID, After: request.Seq - 1, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(page.Events) != 1 || page.Events[0].Seq != request.Seq || page.Events[0].Kind != "file.changed" {
		return nil, toolError("EVENT_NOT_FOUND", "the selected file event is absent or has expired", "not_found")
	}
	event := page.Events[0]
	if event.WorkspaceID == "" || event.ResolvedPath == "" || strings.Contains(event.ResolvedPath, "[REDACTED]") {
		return nil, toolError("DIFF_PATH_UNAVAILABLE", "the event has no usable registered file path", "validation")
	}
	record, err := r.workspaceRegistry.Select(ctx, event.WorkspaceID, "")
	if err != nil {
		return nil, workspaceFailure(err, event.Binding, nil)
	}
	var target workspace.ResolvedTarget
	found := false
	for _, kind := range []string{"source", "artifact", "scratch", "cache"} {
		candidate, resolveErr := workspace.ResolveTarget(record, workspace.TargetRequest{Kind: kind, Path: event.ResolvedPath, TaskID: event.TaskID})
		if resolveErr == nil {
			target, found = candidate, true
			break
		}
	}
	if !found {
		return nil, toolError("DIFF_PATH_OUTSIDE_WORKSPACE", "the recorded file is outside the workspace's current registered roots", "validation")
	}
	relative := strings.TrimPrefix(strings.TrimPrefix(target.ResolvedPath, target.Root), "/")
	if record.Runtime != "wsl" {
		relative, err = filepath.Rel(target.Root, target.ResolvedPath)
		if err != nil {
			return nil, err
		}
		relative = filepath.ToSlash(relative)
	}
	if relative == "" || relative == "." {
		return nil, toolError("INVALID_DIFF_PATH", "a file event is required", "validation")
	}
	bounded, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	base := []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "color.ui=false", "--literal-pathspecs", "-C", target.Root}
	run := func(arguments ...string) (string, int, bool, error) {
		return r.runActivityGit(bounded, record, event, append(append([]string(nil), base...), arguments...))
	}
	_, code, _, err := run("rev-parse", "--show-toplevel")
	if err != nil || code != 0 {
		message := "the recorded workspace target is not an available Git repository"
		if err != nil {
			message += ": " + err.Error()
		}
		return nil, toolError("NOT_A_REPOSITORY", message, "validation")
	}
	_, tracked, _, err := run("ls-files", "--error-unmatch", "--", relative)
	if err != nil && bounded.Err() != nil {
		return nil, bounded.Err()
	}
	result := Result{"task_id": request.TaskID, "thread_id": request.ThreadID, "event_seq": request.Seq, "path": target.ResolvedPath, "view": "current_working_tree", "diff": "", "truncated": false, "tracked": tracked == 0}
	if tracked != 0 {
		result["notice"] = "The file is untracked or no longer in the index; no tracked current diff is available."
		return result, nil
	}
	_, headCode, _, _ := run("rev-parse", "--verify", "HEAD")
	arguments := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames"}
	if headCode == 0 {
		arguments = append(arguments, "HEAD")
	}
	arguments = append(arguments, "--", relative)
	diff, code, truncated, err := run(arguments...)
	if err != nil || code != 0 {
		return nil, toolError("DIFF_FAILED", "the bounded read-only Git diff did not complete successfully", "execution")
	}
	result["diff"], result["truncated"] = diff, truncated
	return result, nil
}
