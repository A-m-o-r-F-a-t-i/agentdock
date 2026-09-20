package workspace

import (
	"github.com/uvwt/agentdock/internal/config"
	contract "github.com/uvwt/agentdock/internal/tool/contract"
)

func InputSchema(name string, _ config.Config) (map[string]any, bool) {
	if name != ToolManage {
		return nil, false
	}
	text := contract.String
	properties := map[string]any{
		"action":            map[string]any{"type": "string", "enum": []string{"list", "get", "register", "resolve"}},
		"workspace_id":      text("Stable workspace identifier. An update requires its current expected_revision."),
		"expected_revision": contract.BoundedInteger("Current rules_revision from get. Required when updating an existing workspace.", 1, 2147483647),
		"name":              text("Human-readable project name."), "project": text("Project identifier used for context-to-workspace matching."),
		"kind":             map[string]any{"type": "string", "enum": []string{"repository", "directory"}},
		"runtime":          map[string]any{"type": "string", "enum": []string{"windows", "unix", "wsl"}},
		"wsl_distribution": text("Explicit WSL distribution. WSL workspaces use absolute Linux paths."),
		"root":             text("Explicit absolute project directory. Never defaults to the process current directory."),
		"default_workdir":  text("Relative command directory within root, default '.' when registering."),
		"artifact_root":    text("Absolute root for delivery artifacts; task_id is appended during routing."),
		"scratch_root":     text("Absolute root for temporary task files; task_id is appended during routing."),
		"cache_root":       text("Absolute directory for replaceable cached downloads and dependencies."),
		"create_root":      contract.Boolean("Create the explicitly named native source root only after validating registration fields."),
		"target_kind":      map[string]any{"type": "string", "enum": []string{"source", "artifact", "scratch", "cache", "external"}},
		"path":             text("Relative logical path to resolve within the selected target kind."),
		"task_id":          text("Task identifier required for artifact and scratch routing."),
		"external_path":    text("Explicit absolute one-call external target. It is not saved as a default workspace."),
	}
	return contract.InputObject(properties, "action"), true
}
func OutputSchema(name string, _ config.Config) (map[string]any, bool) {
	if name != ToolManage {
		return nil, false
	}
	return contract.OutputObject(map[string]any{
		"action": contract.String("Completed registry action."), "workspace": contract.OpenObject("Registered workspace and its rules revision."),
		"workspaces": contract.ObjectArray("Registered workspaces."), "count": contract.Integer("Number of registered workspaces."),
		"default_workspace_id": contract.String("Registry default selected by the configured global workspace."),
		"target":               contract.OpenObject("Logical path, resolved absolute path, target kind and runtime."),
	}), true
}
