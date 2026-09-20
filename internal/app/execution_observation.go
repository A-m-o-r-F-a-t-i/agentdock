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
	binding   activity.Binding
	target    *workspace.ResolvedTarget
	originals map[string]string
	scoped    bool
	started   time.Time
}

func observedTool(name string) bool {
	switch name {
	case "exec_command", "file_edit", "mcp_tool_call", "browser_act", "browser_snapshot", "browser_session", "plugin_manage":
		return true
	}
	return false
}

func (r *Runtime) callObserved(ctx context.Context, spec ToolSpec, original map[string]any) (Result, error) {
	args := make(map[string]any, len(original)+4)
	for key, value := range original {
		args[key] = value
	}
	if spec.Name == "task_manage" {
		action := stringArg(args, "action")
		if action == "create" || stringArg(args, "workspace_id") != "" {
			selected, err := r.workspaceRegistry.Select(ctx, stringArg(args, "workspace_id"), stringArg(args, "project"))
			if err != nil {
				return nil, workspaceFailure(err, activity.Binding{}, nil)
			}
			args["workspace_id"] = selected.ID
		}
	}
	state := executionObservation{started: time.Now(), originals: map[string]string{}}
	if observedTool(spec.Name) {
		var err error
		state, err = r.prepareObservedExecution(ctx, spec.Name, args)
		if err != nil {
			return nil, err
		}
		ctx = activity.WithBinding(ctx, state.binding)
		if spec.Name != "exec_command" {
			r.recordObservedEvent(activity.Event{Binding: state.binding, Kind: "tool.started", ToolName: spec.Name, Status: "running", Title: state.binding.Label}, nil)
		}
	}
	result, err := spec.Handler(ctx, r, args)
	if observedTool(spec.Name) && spec.Name != "exec_command" {
		status := "success"
		if err != nil || resultReportsFailure(result) {
			status = "failed"
		}
		if result != nil && stringArg(result, "status") == "partial" {
			status = "partial"
		}
		event := activity.Event{Binding: state.binding, Kind: "tool.completed", ToolName: spec.Name, Status: status, Title: state.binding.Label, ElapsedMS: time.Since(state.started).Milliseconds()}
		if state.target != nil {
			event.Workdir = state.target.ResolvedPath
			event.Runtime = state.target.Runtime
		}
		r.recordObservedEvent(event, result)
		if spec.Name == "file_edit" && err == nil {
			r.recordFileChanges(args, result, state)
		}
	}
	if err != nil {
		var toolErr *ToolError
		if errors.As(err, &toolErr) {
			copy := *toolErr
			copy.Details = map[string]any{}
			for key, value := range toolErr.Details {
				copy.Details[key] = value
			}
			copy.Details["agentdock_guidance"] = r.executionGuidance(spec.Name, state, nil, true)
			return nil, &copy
		}
		return nil, err
	}
	if result == nil {
		result = Result{}
	}
	// Decorate an owned top-level result only. Nested MCP/plugin payloads are untrusted
	// data and cannot replace the server-generated guidance envelope.
	if observedTool(spec.Name) || spec.Name == "task_manage" || spec.Name == "workspace_manage" || spec.Name == "session_observe" || spec.Name == "session_act" || spec.Name == "read_file" || spec.Name == "search_text" {
		decorated := Result{}
		for key, value := range result {
			decorated[key] = value
		}
		if observedTool(spec.Name) {
			for key, value := range bindingArguments(state.binding) {
				if value != "" {
					decorated[key] = value
				}
			}
		}
		if state.target != nil {
			decorated["workspace_target"] = *state.target
		}
		decorated["agentdock_guidance"] = r.executionGuidance(spec.Name, state, result, false)
		return decorated, nil
	}
	return result, nil
}

func (r *Runtime) prepareObservedExecution(ctx context.Context, name string, args map[string]any) (executionObservation, error) {
	state := executionObservation{started: time.Now(), originals: map[string]string{}}
	state.binding = activity.Binding{TaskID: stringArg(args, "task_id"), ThreadID: stringArg(args, "thread_id"), StepID: stringArg(args, "step_id"), WorkspaceID: stringArg(args, "workspace_id"), Label: stringArg(args, "activity_label")}
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
			target, err := workspace.ResolveCommandDirectory(selected, workspace.TargetRequest{Kind: stringArg(args, "target_kind"), Path: stringArg(args, "workdir"), TaskID: state.binding.TaskID, ExternalPath: stringArg(args, "external_path")})
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
	binding, err = r.taskTools.ResolveBinding(state.binding, true)
	if err != nil {
		return state, toolErrorDetails("INVALID_ACTIVITY_BINDING", err.Error(), "validation", map[string]any{"task_id": state.binding.TaskID, "thread_id": state.binding.ThreadID})
	}
	state.binding = binding
	for key, value := range bindingArguments(binding) {
		if value != "" {
			args[key] = value
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
	return map[string]string{"task_id": binding.TaskID, "thread_id": binding.ThreadID, "step_id": binding.StepID, "workspace_id": binding.WorkspaceID, "activity_label": binding.Label}
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
