package app

import (
	"context"

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
