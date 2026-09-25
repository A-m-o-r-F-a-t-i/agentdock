package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/permission"
	"github.com/uvwt/agentdock/internal/workspace"
)

// Only runtime-owned classifications enter the evaluator. MCP annotations,
// caller claims and command spelling cannot prove an effect is constrained.
func (r *Runtime) profileFacts(f permission.Facts, args map[string]any, state executionObservation) permission.Facts {
	switch f.Tool {
	case "read_file", "list_dir", "search_text", "file_edit":
		f.Filesystem = permission.FileRead
		if f.Tool == "file_edit" && !f.ReadOnly {
			f.Filesystem = permission.FileWrite
		}
		runtimeName := stringArg(args, "runtime")
		if runtimeName == "wsl" || state.selected != nil && state.selected.Runtime == "wsl" {
			return f
		}
		f.EffectsKnown = true
		if f.Tool == "file_edit" && (state.selected == nil || len(state.originals) == 0) {
			f.EffectsKnown = false
		}
		logical := stringArg(args, "path")
		if strings.HasPrefix(logical, "skill://") || strings.HasPrefix(logical, "activity://") {
			// These are host control-plane resource aliases, not workspace paths.
			f.WorkspaceBound = false
			return f
		}
		paths := []string{logical}
		if f.Tool == "file_edit" {
			paths = nil
			for actual := range state.originals {
				paths = append(paths, actual)
			}
			if len(paths) == 0 {
				paths = []string{logical}
				if next := stringArg(args, "new_path"); next != "" {
					paths = append(paths, next)
				}
			}
		}
		f.WorkspaceBound = state.selected != nil && len(paths) > 0
		for _, target := range paths {
			// UNC access is network access even though it enters a file tool.
			if strings.HasPrefix(target, `\\`) || strings.HasPrefix(target, "//") {
				f.Network = true
			}
			if state.selected == nil {
				continue
			}
			if strings.HasPrefix(state.selected.Root, `\\`) || strings.HasPrefix(state.selected.Root, "//") {
				f.Network = true
			}
			if target == "" && f.Tool == "file_edit" {
				f.WorkspaceBound = false
				continue
			}
			if strings.HasPrefix(target, "~") {
				f.WorkspaceBound = false
				continue
			}
			resolved, err := workspace.ResolveTarget(*state.selected, workspace.TargetRequest{Kind: "source", Path: target})
			if err != nil {
				f.WorkspaceBound = false
				continue
			}
			if f.Tool == "file_edit" && resolved.ResolvedPath == resolved.Root {
				f.WorkspaceBound = false
			}
			if resolved.Root != state.selected.Root {
				f.WorkspaceBound = false
			}
			if strings.HasPrefix(resolved.ResolvedPath, `\\`) || strings.HasPrefix(resolved.ResolvedPath, "//") {
				f.Network = true
			}
			// A confined write must not modify its own permissions or host config.
			if f.Filesystem == permission.FileWrite && pathOverlaps(resolved.ResolvedPath, r.cfg.AgentDockHome) {
				f.EffectsKnown = false
				f.WorkspaceBound = false
			}
			if f.Tool == "file_edit" && stringArg(args, "action") == "patch" && len(state.originals) == 0 {
				f.WorkspaceBound = false
			}
		}
	case "task_manage":
		// Task metadata is host-owned control state, not arbitrary filesystem I/O.
		f.EffectsKnown = f.ReadOnly || f.Management
		f.WorkspaceBound = f.EffectsKnown
	case "workspace_manage":
		f.EffectsKnown = f.Action == "get" || f.Action == "list" || f.Action == "resolve"
		f.WorkspaceBound = f.EffectsKnown
	case "session_observe", "insertion_ack":
		// Receipt bookkeeping is authenticated control-plane metadata, like
		// task state. It cannot execute/retry business tools or change policy.
		f.EffectsKnown, f.WorkspaceBound = true, true
	case "session_act":
		// Stopping a process cannot grant it new filesystem/network access.
		f.EffectsKnown = f.Action == "kill" || f.Action == "kill_all"
		f.WorkspaceBound = f.EffectsKnown
	case "exec_command", "mcp_tool_call", "browser_act", "browser_snapshot", "agentdock_context", "workspace_context", "plugin_load", "mcp_tool_search", "mcp_tool_list", "mcp_tool_inspect":
		// Discovery may start external MCP processes or open remote connections.
		f.Network = true
	case "view_image", "file_publish":
		// Media conversion/publication can create files and start helpers.
		f.Network = stringArg(args, "url") != "" || f.Tool == "file_publish"
	}
	return f
}

func pathOverlaps(a, b string) bool {
	left, leftErr := workspace.CanonicalNativePath(a)
	right, rightErr := workspace.CanonicalNativePath(b)
	if leftErr != nil || rightErr != nil {
		return true
	} // Unknown identity must not authorize a protected write.
	a, b = left, right
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (r *Runtime) revalidatePermission(ctx context.Context, p *preparedExecution) error {
	current, err := r.permissions.Decide(ctx, r.executionFacts(p.spec.Name, p.args, p.state))
	if err != nil {
		return err
	}
	if current.Revision != p.decision.Revision {
		return permission.ErrRevision
	}
	if current.Effect == permission.Deny {
		return errors.New(current.Reason)
	}
	return nil
}
