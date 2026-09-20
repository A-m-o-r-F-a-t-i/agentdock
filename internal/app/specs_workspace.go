package app

import (
	"context"
	"github.com/uvwt/agentdock/internal/config"
	toolworkspace "github.com/uvwt/agentdock/internal/tool/workspace"
)

func workspaceToolSpecs() []ToolSpec {
	return []ToolSpec{{Name: toolworkspace.ToolManage, Title: "Manage workspaces", Description: "Register and inspect project workspaces, resolve source/artifact/scratch/cache paths, and declare one-call external targets. Bind returned workspace_id to tasks and execution tools; this is path routing, not a shell sandbox.", Contract: func(name string, cfg config.Config) (ToolContract, bool) {
		input, ok := toolworkspace.InputSchema(name, cfg)
		if !ok {
			return ToolContract{}, false
		}
		output, ok := toolworkspace.OutputSchema(name, cfg)
		return ToolContract{InputSchema: input, OutputSchema: output}, ok
	}, Annotations: mutatingToolAnnotations(false, false), Handler: typedToolHandler(toolworkspace.ToolManage, func(ctx context.Context, r *Runtime, request toolworkspace.Request) (Result, error) {
		return r.workspaceTools.Manage(ctx, request)
	})}}
}
