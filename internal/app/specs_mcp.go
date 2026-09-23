package app

import (
	"context"

	toolmcp "github.com/uvwt/agentdock/internal/tool/mcp"
)

func dynamicMCPToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Name: "mcp_manage", Contract: mcpToolContract, Title: "Manage dynamic MCP servers", Description: "Register, inspect, enable, disable, refresh, remove, or manage the isolated environment of dynamic MCP servers. Dynamic MCP tools remain separate from AgentDock built-in tools.", Annotations: mutatingToolAnnotations(true, true), Handler: typedToolHandler("mcp_manage", func(ctx context.Context, r *Runtime, request toolmcp.ManageRequest) (Result, error) {
			return r.dynamicMCP.Manage(ctx, request)
		})},
		{Name: "mcp_tool_list", Contract: mcpToolContract, Title: "List an MCP catalog", Description: "Return a known server's entire two-column tool catalog. Use this directly when the server is known; skip it when a prior business response already supplied the catalog.", Annotations: readOnlyToolAnnotations(true), Handler: typedToolHandler("mcp_tool_list", func(ctx context.Context, r *Runtime, request toolmcp.ListRequest) (Result, error) {
			return r.dynamicMCP.List(ctx, request)
		})},
		{Name: "mcp_tool_search", Contract: mcpToolContract, Title: "Search dynamic MCP tools", Description: "Search lightweight tool summaries only when the service or capability location is unknown. For a known service use mcp_tool_list; do not search before every call.", Annotations: readOnlyToolAnnotations(true), Handler: typedToolHandler("mcp_tool_search", func(ctx context.Context, r *Runtime, request toolmcp.SearchRequest) (Result, error) {
			return r.dynamicMCP.Search(ctx, request)
		})},
		{Name: "mcp_tool_inspect", Contract: mcpToolContract, Title: "Inspect a dynamic MCP tool", Description: "Read full schemas in one request using names: [server:tool, server:*, other:*]. Services share one cached catalog; skip when valid schemas are already known.", Annotations: readOnlyToolAnnotations(true), Handler: typedToolHandler("mcp_tool_inspect", func(ctx context.Context, r *Runtime, request toolmcp.InspectRequest) (Result, error) {
			return r.dynamicMCP.Inspect(ctx, request)
		})},
		{Name: "mcp_tool_call", Contract: mcpToolContract, Title: "Call a dynamic MCP tool", Description: "Execute a dynamic MCP tool identified as server:tool, validating against its current cached schema. Prior inspect is not mandatory. Every response includes the service complete two-column catalog, followed by any trusted user insertion.", Annotations: mutatingToolAnnotations(true, true), Handler: typedToolHandler("mcp_tool_call", func(ctx context.Context, r *Runtime, request toolmcp.CallRequest) (Result, error) {
			return r.dynamicMCP.Call(ctx, request)
		})},
	}
}
