package mcp

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

func (s *Service) List(ctx context.Context, request ListRequest) (Result, error) {
	server := strings.TrimSpace(request.Server)
	if err := s.ensureAvailable(server); err != nil {
		return nil, err
	}
	catalog, err := s.mcpClients.Catalog(ctx, server)
	if err != nil {
		return nil, dynamicMCPToolError(err)
	}
	return catalogResult(catalog, true), nil
}

func catalogResult(catalog mcpclient.Catalog, fresh bool) Result {
	tools := make([]map[string]any, 0, len(catalog.Tools))
	for _, name := range catalog.Names() {
		tool := catalog.Tools[name]
		tools = append(tools, map[string]any{"name": catalog.Server + ":" + name, "description": mcpclient.OneLineDescription(tool.Description)})
	}
	return Result{"server": catalog.Server, "catalog_revision": catalog.Revision, "complete": catalog.Complete, "total": len(tools), "tools": tools, "stale": !fresh}
}

// Inspect normalizes the legacy single selector once, then runs the same grouped
// reader. No tool discovery is repeated per member; returned schema maps are
// independent of the dispatch validator's immutable catalog.
func (s *Service) Inspect(ctx context.Context, request InspectRequest) (Result, error) {
	legacy := len(request.Names) == 0 && strings.TrimSpace(request.Name) != ""
	names := append([]string{}, request.Names...)
	if legacy {
		names = []string{request.Name}
	}
	if len(names) == 0 || (len(request.Names) > 0 && request.Name != "") {
		return nil, toolErrorDetails("MCP_TOOL_NAMES_REQUIRED", "use names for tool selectors or service:*", "validation", nil)
	}
	selected := map[string][]string{}
	invalid := []map[string]any{}
	seen := map[string]bool{}
	for _, selector := range names {
		selector = strings.TrimSpace(selector)
		if seen[selector] {
			continue
		}
		seen[selector] = true
		server, tool, ok := strings.Cut(selector, ":")
		server, tool = strings.TrimSpace(server), strings.TrimSpace(tool)
		if !ok || server == "" || tool == "" || strings.Contains(server, "*") || (strings.Contains(tool, "*") && tool != "*") {
			invalid = append(invalid, map[string]any{"selector": selector, "code": "MCP_TOOL_NAME_INVALID", "status": "invalid", "message": "use server:tool or server:*"})
			continue
		}
		selected[server] = append(selected[server], tool)
	}
	type serverResult struct {
		catalog mcpclient.Catalog
		err     error
	}
	results := map[string]serverResult{}
	var mutex sync.Mutex
	var workers sync.WaitGroup
	limit := make(chan struct{}, 4)
	for server := range selected {
		workers.Add(1)
		go func(server string) {
			defer workers.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				mutex.Lock()
				results[server] = serverResult{err: ctx.Err()}
				mutex.Unlock()
				return
			}
			var value serverResult
			value.err = s.ensureAvailable(server)
			if value.err == nil {
				value.catalog, value.err = s.mcpClients.Catalog(ctx, server)
			}
			mutex.Lock()
			results[server] = value
			mutex.Unlock()
		}(server)
	}
	workers.Wait()
	servers := make([]string, 0, len(selected))
	for name := range selected {
		servers = append(servers, name)
	}
	sort.Strings(servers)
	tools := []map[string]any{}
	catalogs := []map[string]any{}
	problems := invalid
	var singleError error
	for _, server := range servers {
		value := results[server]
		if value.err != nil {
			if legacy {
				singleError = dynamicMCPToolError(value.err)
			}
			code := "MCP_ERROR"
			var typed *mcpclient.Error
			if errors.As(value.err, &typed) {
				code = typed.Code
			}
			var toolErr *ToolError
			if errors.As(value.err, &toolErr) {
				code = toolErr.Code
			}
			status := "error"
			if strings.Contains(code, "DISABLED") {
				status = "disabled"
			}
			if strings.Contains(code, "NOT_FOUND") {
				status = "not_found"
			}
			if strings.Contains(code, "AUTH") {
				status = "unauthorized"
			}
			for _, name := range selected[server] {
				problems = append(problems, map[string]any{"selector": server + ":" + name, "code": code, "status": status, "message": value.err.Error()})
			}
			continue
		}
		catalog := value.catalog
		catalogs = append(catalogs, map[string]any{"server": server, "catalog_revision": catalog.Revision, "server_version": catalog.ServerVersion, "complete": true, "total": len(catalog.Tools)})
		chosen := map[string]bool{}
		for _, name := range selected[server] {
			if name == "*" {
				for item := range catalog.Tools {
					chosen[item] = true
				}
			} else {
				chosen[name] = true
			}
		}
		ordered := make([]string, 0, len(chosen))
		for name := range chosen {
			ordered = append(ordered, name)
		}
		sort.Strings(ordered)
		for _, name := range ordered {
			tool, ok := catalog.Tools[name]
			if !ok {
				problems = append(problems, map[string]any{"selector": server + ":" + name, "code": "MCP_TOOL_NOT_FOUND", "status": "not_found", "message": "MCP tool not found"})
				if legacy {
					singleError = toolErrorDetails("MCP_TOOL_NOT_FOUND", "MCP tool not found", "validation", map[string]any{"tool": server + ":" + name})
				}
				continue
			}
			entry := map[string]any{"name": server + ":" + name, "server": server, "tool_name": name, "catalog_revision": catalog.Revision, "server_version": catalog.ServerVersion, "title": tool.Title, "description": tool.Description, "input_schema": tool.InputSchema}
			if tool.OutputSchema != nil {
				entry["output_schema"] = tool.OutputSchema
			}
			if tool.Annotations != nil {
				entry["annotations"] = tool.Annotations
			}
			if len(tool.StandardMetadata) > 0 {
				entry["metadata"] = tool.StandardMetadata
			}
			tools = append(tools, entry)
		}
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i]["selector"].(string) < problems[j]["selector"].(string) })
	if legacy {
		if singleError != nil {
			return nil, singleError
		}
		if len(tools) == 1 && len(problems) == 0 {
			return Result(tools[0]), nil
		}
		return nil, toolErrorDetails("MCP_TOOL_NAME_INVALID", "use server:tool for a legacy single selector", "validation", nil)
	}
	return Result{"tools": tools, "count": len(tools), "total": len(tools), "complete": len(problems) == 0, "errors": problems, "catalogs": catalogs}, nil
}
