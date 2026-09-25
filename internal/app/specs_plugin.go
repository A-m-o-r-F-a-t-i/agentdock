package app

import (
	"context"
	"strings"

	toolplugin "github.com/uvwt/agentdock/internal/tool/plugin"
)

func pluginToolSpecs() []ToolSpec {
	return []ToolSpec{
		{
			Name: "plugin_manage", Contract: pluginToolContract,
			Title:       "Manage plugins",
			Description: "Validate, install, update, inspect, or remove Agent Plugins 1.0.0 directories or ZIP packages. Manage enable/member/Heavy switches in host state outside the portable package. Only Heavy plugins defer their members until plugin_load.",
			Annotations: mutatingToolAnnotations(true, false),
			Handler: typedToolHandler("plugin_manage", func(ctx context.Context, r *Runtime, request toolplugin.ManageRequest) (Result, error) {
				// Local sources resolve through the selected Host workspace,
				// never through the server process's incidental working directory.
				raw := strings.TrimSpace(request.Source)
				if raw != "" && !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "git@") {
					resolved, pathErr := r.ws.ResolveExisting(raw)
					if pathErr == nil {
						request.Source = resolved.Abs
					} else if request.SourceType != "git" {
						return nil, toolErrorDetails("PLUGIN_SOURCE_INVALID", "Plugin source cannot be resolved in the workspace", "validation", map[string]any{"source": raw})
					}
				}
				return r.plugins.Manage(ctx, request)
			}),
		},
		{
			Name: "plugin_load", Contract: pluginToolContract,
			Title:       "Expand plugin",
			Description: "Expand an enabled plugin and reveal its Skill and MCP members. Only Heavy plugins require this step; ordinary plugin members already appear in agentdock_context and can be used directly. Explicit expansion is read-only and never enables Heavy mode.",
			Annotations: readOnlyToolAnnotations(false),
			Handler: typedToolHandler("plugin_load", func(ctx context.Context, r *Runtime, request toolplugin.LoadRequest) (Result, error) {
				return r.plugins.Load(ctx, request)
			}),
		},
	}
}
