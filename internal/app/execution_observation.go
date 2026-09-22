package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
	"github.com/uvwt/agentdock/internal/workspace"
)

type executionObservation struct {
	entryBinding activity.Binding
	binding      activity.Binding
	target       *workspace.ResolvedTarget
	originals    map[string]string
	scoped       bool
	started      time.Time
	executed     bool
	waitMS       *int64
	executionMS  *int64
	selected     *workspace.Record
}

func (r *Runtime) prepareObservedExecution(ctx context.Context, name string, args map[string]any) (executionObservation, error) {
	state := executionObservation{started: time.Now(), originals: map[string]string{}}
	state.entryBinding = activity.FromContext(ctx)
	var err error
	state.binding, err = applyExecutionOverrides(name, args, state.entryBinding)
	if err != nil {
		return state, err
	}
	state.scoped = state.binding.TaskID != "" || state.binding.WorkspaceID != "" || stringArg(args, "target_kind") != "" || stringArg(args, "external_path") != ""
	binding, err := r.taskTools.ResolveBinding(state.binding, false)
	if err != nil {
		return state, toolErrorDetails("INVALID_ACTIVITY_BINDING", err.Error(), "validation", map[string]any{"task_id": state.binding.TaskID, "thread_id": state.binding.ThreadID})
	}
	state.binding = binding
	var selected workspace.Record
	if state.scoped {
		selected, err = r.workspaceRegistry.Select(ctx, binding.WorkspaceID, "")
		if err != nil {
			return state, workspaceFailure(err, binding, nil)
		}
		state.binding.WorkspaceID = selected.ID
		state.selected = &selected
	}
	if state.scoped && (name == "exec_command" || name == "file_edit") {
		if runtimeName := stringArg(args, "runtime"); runtimeName != "" && runtimeName != selected.Runtime {
			return state, workspaceFailure(errors.New("runtime conflicts with the selected workspace"), state.binding, &selected)
		}
		if distribution := stringArg(args, "wsl_distribution"); distribution != "" && selected.Distribution != "" && distribution != selected.Distribution {
			return state, workspaceFailure(errors.New("WSL distribution conflicts with workspace"), state.binding, &selected)
		}
		args["runtime"] = selected.Runtime
		if selected.Runtime == "unix" {
			delete(args, "runtime")
		} // Existing native Unix tools have no runtime selector.
		if selected.Distribution != "" {
			args["wsl_distribution"] = selected.Distribution
		}
		if name == "exec_command" {
			target, err := workspace.ResolveCommandPreview(selected, workspace.TargetRequest{Kind: stringArg(args, "target_kind"), Path: stringArg(args, "workdir"), TaskID: state.binding.TaskID, ExternalPath: stringArg(args, "external_path")})
			if err != nil {
				return state, workspaceFailure(err, state.binding, &selected)
			}
			state.target = &target
			args["workdir"] = target.ResolvedPath
		} else {
			if err = r.routeFileEdit(args, selected, &state); err != nil {
				return state, workspaceFailure(err, state.binding, &selected)
			}
		}
	}
	if state.scoped && (name == "read_file" || name == "list_dir" || name == "search_text") {
		logical := stringArg(args, "path")
		if !strings.HasPrefix(logical, "skill://") && !strings.HasPrefix(logical, "~") {
			if selected.Runtime == "wsl" {
				if !path.IsAbs(logical) {
					args["path"] = path.Join(selected.Root, logical)
				}
			} else if !filepath.IsAbs(logical) {
				args["path"] = filepath.Join(selected.Root, logical)
			}
			if selected.Runtime != "unix" && stringArg(args, "runtime") == "" {
				args["runtime"] = selected.Runtime
			}
			if selected.Distribution != "" && stringArg(args, "wsl_distribution") == "" {
				args["wsl_distribution"] = selected.Distribution
			}
		}
	}
	// Resolution is side-effect free. Persist an execution binding only after
	// permission is granted, never while preparing a pending approval.
	if name == "task_manage" {
		// Lifecycle handlers consume task identifiers as business parameters.
		// Ordinary tools obtain the same snapshot exclusively through context.
		for _, key := range []string{"task_id", "thread_id", "workspace_id"} {
			if stringArg(args, key) == "" {
				if value := bindingArguments(state.binding)[key]; value != "" {
					args[key] = value
				}
			}
		}
	}
	return state, nil
}

func (r *Runtime) routeFileEdit(args map[string]any, record workspace.Record, state *executionObservation) error {
	kind := stringArg(args, "target_kind")
	if kind == "" {
		kind = "source"
	}
	external := stringArg(args, "external_path")
	if kind == "external" {
		if stringArg(args, "action") == "patch" || stringArg(args, "action") == "move" {
			return errors.New("a one-call external file target supports add, replace or delete; register an explicit directory for multi-file edits")
		}
		target, err := workspace.ResolveTarget(record, workspace.TargetRequest{Kind: kind, Path: stringArg(args, "path"), TaskID: state.binding.TaskID, ExternalPath: external})
		if err != nil {
			return err
		}
		state.originals[target.ResolvedPath] = stringArg(args, "path")
		args["path"] = target.ResolvedPath
		if record.Runtime == "wsl" {
			args["workdir"] = path.Dir(target.ResolvedPath)
		} else {
			args["workdir"] = filepath.Dir(target.ResolvedPath)
		}
		state.target = &target
		return nil
	}
	base, err := workspace.ResolveTarget(record, workspace.TargetRequest{Kind: kind, Path: stringArg(args, "workdir"), TaskID: state.binding.TaskID})
	if err != nil {
		return err
	}
	state.target = &base
	args["workdir"] = base.ResolvedPath
	resolve := func(logical string) (string, error) {
		candidate := logical
		if record.Runtime == "wsl" {
			if !path.IsAbs(candidate) {
				candidate = path.Join(base.ResolvedPath, candidate)
			}
		} else {
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(base.ResolvedPath, candidate)
			}
		}
		target, err := workspace.ResolveTarget(record, workspace.TargetRequest{Kind: kind, Path: candidate, TaskID: state.binding.TaskID})
		if err != nil {
			return "", err
		}
		state.originals[target.ResolvedPath] = logical
		return target.ResolvedPath, nil
	}
	if stringArg(args, "action") != "patch" {
		for _, key := range []string{"path", "new_path"} {
			if logical := stringArg(args, key); logical != "" {
				resolved, err := resolve(logical)
				if err != nil {
					return err
				}
				args[key] = resolved
			}
		}
		return nil
	}
	patchText := stringArg(args, "patch")
	if !strings.HasPrefix(strings.TrimSpace(patchText), "*** Begin Patch") {
		return errors.New("workspace-scoped patches require the structured *** Begin Patch envelope so every target can be validated before writing")
	}
	lines := strings.Split(strings.ReplaceAll(patchText, "\r\n", "\n"), "\n")
	for i, line := range lines {
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			absolute, err := resolve(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
			if err != nil {
				return err
			}
			relative := absolute
			if record.Runtime != "wsl" {
				relative, err = filepath.Rel(base.ResolvedPath, absolute)
				if err != nil {
					return err
				}
				relative = filepath.ToSlash(relative)
			}
			lines[i] = prefix + relative
		}
	}
	args["patch"] = strings.Join(lines, "\n")
	args["patch_format"] = "envelope"
	return nil
}

func workspaceFailure(err error, binding activity.Binding, record *workspace.Record) error {
	details := map[string]any{"task_id": binding.TaskID, "thread_id": binding.ThreadID, "workspace_id": binding.WorkspaceID, "next_required": map[string]any{"tool": "workspace_manage", "action": "get", "text": "Select the intended workspace, then retry with its workspace_id and a contained path. Use target_kind=external only with an explicitly named one-call target."}}
	if record != nil {
		details["workspace"] = *record
		details["retry_arguments"] = map[string]any{"workspace_id": record.ID, "workdir": record.Root}
	}
	code := "WORKSPACE_ERROR"
	if errors.Is(err, workspace.ErrWorkspaceBoundary) {
		code = "WORKSPACE_PATH_OUTSIDE_ROOT"
	}
	if errors.Is(err, workspace.ErrWorkspaceRequired) {
		code = "WORKSPACE_REQUIRED"
	}
	if errors.Is(err, workspace.ErrWorkspaceNotFound) {
		code = "WORKSPACE_NOT_FOUND"
	}
	return toolErrorDetails(code, err.Error(), "validation", details)
}

func bindingArguments(binding activity.Binding) map[string]string {
	return map[string]string{"conversation_id": binding.ConversationID, "call_id": binding.CallID, "parent_call_id": binding.ParentCallID, "retry_of_call_id": binding.RetryOfCallID, "task_id": binding.TaskID, "thread_id": binding.ThreadID, "step_id": binding.StepID, "workspace_id": binding.WorkspaceID, "activity_label": binding.Label}
}
func stringArg(args map[string]any, key string) string { value, _ := args[key].(string); return value }

func resultReportsFailure(result Result) bool {
	if result == nil {
		return false
	}
	for _, key := range []string{"isError", "is_error"} {
		if flag, ok := result[key].(bool); ok && flag {
			return true
		}
	}
	for _, key := range []string{"ok", "browser_ok"} {
		if flag, ok := result[key].(bool); ok && !flag {
			return true
		}
	}
	if inner, ok := result["result"].(map[string]any); ok {
		return resultReportsFailure(Result(inner))
	}
	inner := reflect.ValueOf(result["result"])
	if inner.IsValid() && inner.Kind() == reflect.Pointer && !inner.IsNil() {
		inner = inner.Elem()
	}
	if inner.IsValid() && inner.Kind() == reflect.Struct {
		field := inner.FieldByName("IsError")
		if field.IsValid() && field.Kind() == reflect.Bool {
			return field.Bool()
		}
	}
	return false
}

func (r *Runtime) recordObservedEvent(event activity.Event, result Result) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.activity.Append(ctx, event); err != nil && result != nil {
		result["activity_warning"] = "The operation completed, but its activity journal could not be fully updated."
	}
}

func (r *Runtime) recordFileChanges(args map[string]any, result Result, state executionObservation) {
	if result == nil || result["dry_run"] == true || result["changed"] == false {
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	generic := map[string]any{}
	if json.Unmarshal(data, &generic) != nil {
		return
	}
	paths := []string{}
	if affected, ok := generic["affected_files"].([]any); ok {
		for _, entry := range affected {
			if item, ok := entry.(map[string]any); ok {
				if value := stringArg(item, "path"); value != "" {
					paths = append(paths, value)
				}
				if value := stringArg(item, "move_to"); value != "" {
					paths = append(paths, value)
				}
			} else if value, ok := entry.(string); ok {
				paths = append(paths, value)
			}
		}
	} else {
		if value := stringArg(generic, "path"); value != "" {
			paths = append(paths, value)
		}
		if value := stringArg(generic, "new_path"); value != "" {
			paths = append(paths, value)
		}
	}
	seen := map[string]bool{}
	for _, value := range paths {
		actual := value
		workdir := stringArg(args, "workdir")
		runtimeName := stringArg(args, "runtime")
		if runtimeName == "" {
			runtimeName = workspace.HostRuntime()
		}
		if runtimeName == "wsl" {
			if !path.IsAbs(actual) {
				actual = path.Join(workdir, actual)
			}
		} else {
			// Existing Host results already use paths relative to the configured root, not workdir.
			resolved, resolveErr := r.ws.ResolveForWrite(actual)
			if resolveErr != nil {
				continue
			}
			actual = resolved.Abs
			if workdir == "" {
				workdir = r.ws.DefaultCWD()
			}
		}
		if seen[actual] {
			continue
		}
		seen[actual] = true
		logical := state.originals[actual]
		if logical == "" {
			logical = value
		}
		event := activity.Event{Binding: state.binding, Kind: "file.changed", ToolName: "file_edit", Status: "success", Title: state.binding.Label, LogicalPath: logical, ResolvedPath: actual, Workdir: workdir, Runtime: runtimeName, Summary: fmt.Sprintf("%s: %s", stringArg(args, "action"), logical)}
		if len(paths) == 1 {
			event.Insertions = toolcore.IntArg(generic, "insertions", 0)
			event.Deletions = toolcore.IntArg(generic, "deletions", 0)
			_, event.ChangeStatsKnown = generic["insertions"]
		}
		r.recordObservedEvent(event, result)
	}
}
