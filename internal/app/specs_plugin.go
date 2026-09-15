package app

import (
	"context"

	toolplugin "github.com/uvwt/agentdock/internal/tool/plugin"
)

func pluginToolSpecs() []ToolSpec {
	return []ToolSpec{
		{
			Name: "plugin_manage", Contract: pluginToolContract,
			Title:       "Manage heavy plugins",
			Description: "Validate, install, update, inspect, switch, or remove self-contained heavy-plugin directories or ZIP packages. Each installed plugin directly owns its Skills, MCP definitions, implementation files, and state without a cache layer.",
			Annotations: mutatingToolAnnotations(true, false),
			Handler: typedToolHandler("plugin_manage", func(ctx context.Context, r *Runtime, request toolplugin.ManageRequest) (Result, error) {
				return r.plugins.Manage(ctx, request)
			}),
		},
		{
			Name: "plugin_load", Contract: pluginToolContract,
			Title:       "Load a heavy plugin",
			Description: "Expand one enabled plugin from agentdock_context and reveal its contained Skill descriptions and dynamic MCP server descriptions. Load the plugin before using a plugin-owned member.",
			Annotations: readOnlyToolAnnotations(false),
			Handler: typedToolHandler("plugin_load", func(ctx context.Context, r *Runtime, request toolplugin.LoadRequest) (Result, error) {
				return r.plugins.Load(ctx, request)
			}),
		},
	}
}
