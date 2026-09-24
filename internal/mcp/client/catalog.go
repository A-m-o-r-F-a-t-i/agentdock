package client

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const catalogMaxAge = time.Minute

// Catalog is one immutable discovered generation. Exported copies do not share
// schema maps with the validator or with another request's returned data.
type Catalog struct {
	Server        string
	Revision      string
	ServerVersion string
	Complete      bool
	RefreshedAt   time.Time
	Tools         map[string]Tool
}

func protocolGeneration(client protocolClient) uint64 {
	if source, ok := client.(interface{ ToolsRevision() uint64 }); ok {
		return source.ToolsRevision()
	}
	return 0
}

func catalogFresh(view *indexSnapshot) bool {
	return view != nil && view.Ready && view.Known && !view.RefreshedAt.IsZero() &&
		time.Since(view.RefreshedAt) < catalogMaxAge && protocolGeneration(view.Client) == view.Generation
}

func (m *Manager) catalogSnapshot(server string) (Catalog, bool, error) {
	if m.closed.Load() {
		return Catalog{}, false, newError("MCP_MANAGER_CLOSED", "dynamic MCP manager is closed", false, nil, nil)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, exists := m.servers[server]
	if !exists {
		return Catalog{}, false, newError("MCP_SERVER_NOT_FOUND", "dynamic MCP server not found", false, map[string]any{"server": server}, nil)
	}
	if !cfg.Enabled {
		return Catalog{}, false, newError("MCP_SERVER_DISABLED", "dynamic MCP server is disabled", false, map[string]any{"server": server}, nil)
	}
	state := m.states[server]
	if state == nil {
		return Catalog{}, false, nil
	}
	view := state.snapshot.Load()
	if view == nil || !view.Known {
		return Catalog{}, false, nil
	}
	catalog := Catalog{Server: server, Revision: fmt.Sprintf("%s:%d", cfg.revision, view.Revision), ServerVersion: view.Version, Complete: true, RefreshedAt: view.RefreshedAt, Tools: view.Tools}
	return catalog, catalogFresh(view), nil
}

// CachedCatalog never connects, waits for a running business call or changes
// permissions. It returns the last discovered complete directory with an explicit
// freshness flag; disabled/removed services are not exposed.
func (m *Manager) CachedCatalog(server string) (Catalog, bool, error) {
	catalog, fresh, err := m.catalogSnapshot(strings.TrimSpace(server))
	if err != nil {
		return Catalog{}, false, err
	}
	catalog.Tools = cloneTools(catalog.Tools)
	return catalog, fresh, nil
}

// Catalog shares the same full tools/list generation as inspect and dispatch.
// A hot directory is readable while an unrelated business call owns state.mu.
func (m *Manager) Catalog(ctx context.Context, server string) (Catalog, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return Catalog{}, newError("MCP_SERVER_REQUIRED", "one dynamic MCP server is required", false, nil, nil)
	}
	if err := m.syncRegistryContext(ctx); err != nil {
		return Catalog{}, err
	}
	catalog, fresh, err := m.catalogSnapshot(server)
	if err != nil {
		return Catalog{}, err
	}
	if !fresh {
		if _, err := m.ensureTools(ctx, server); err != nil {
			return Catalog{}, err
		}
		catalog, fresh, err = m.catalogSnapshot(server)
		if err != nil {
			return Catalog{}, err
		}
		if !fresh {
			return Catalog{}, newError("MCP_CATALOG_CHANGED", "tool directory changed while it was being read", true, map[string]any{"server": server}, nil)
		}
	}
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	catalog.Tools = cloneTools(catalog.Tools)
	return catalog, nil
}

func (catalog Catalog) Names() []string {
	names := make([]string, 0, len(catalog.Tools))
	for name := range catalog.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// compileCatalog validates one full paginated response before publishing any
// member. A malformed page never overwrites the last known good generation.
func compileCatalog(ctx context.Context, cfg ServerConfig, listed []Tool) (map[string]Tool, error) {
	tools := make(map[string]Tool, len(listed))
	for _, tool := range listed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tool.Name = strings.TrimSpace(tool.Name)
		if tool.Name == "" {
			return nil, newError("MCP_INVALID_RESPONSE", "MCP tools/list returned an empty tool name", false, map[string]any{"server": cfg.Name}, nil)
		}
		if _, exists := tools[tool.Name]; exists {
			return nil, newError("MCP_INVALID_RESPONSE", "MCP tools/list returned duplicate tool names", false, map[string]any{"server": cfg.Name, "tool": tool.Name}, nil)
		}
		if tool.InputSchema == nil {
			return nil, newError("MCP_INVALID_RESPONSE", "MCP tools/list omitted the required inputSchema", false, map[string]any{"server": cfg.Name, "tool": tool.Name}, nil)
		}
		validator, err := compileToolInputSchema(tool.InputSchema)
		if err != nil {
			return nil, newError("MCP_SCHEMA_INVALID", "MCP tools/list returned an invalid input schema", false, map[string]any{"server": cfg.Name, "tool": tool.Name, "reason": err.Error()}, err)
		}
		tool.inputValidator = validator
		tools[tool.Name] = cloneTool(tool)
	}
	return tools, nil
}

func reloadCatalogLocked(ctx context.Context, cfg ServerConfig, state *serverState) (map[string]Tool, error) {
	generation := protocolGeneration(state.client)
	listed, err := state.client.listTools(ctx)
	if err != nil {
		recordStateError(state, err)
		return nil, err
	}
	tools, err := compileCatalog(ctx, cfg, listed)
	if err == nil && generation != protocolGeneration(state.client) {
		err = newError("MCP_CATALOG_CHANGED", "tool directory changed during pagination", true, map[string]any{"server": cfg.Name}, nil)
	}
	if err != nil {
		recordStateError(state, err)
		return nil, err
	}
	if !catalogEqual(state.tools, tools) {
		state.indexRevision++
	}
	state.tools = tools
	state.catalogGeneration = generation
	state.refreshedAt = time.Now().UTC()
	state.lastError, state.lastErrorCode = "", ""
	publishStateLocked(state)
	return cloneTools(tools), nil
}

func cloneTool(tool Tool) Tool {
	tool.InputSchema = cloneJSONMap(tool.InputSchema)
	tool.OutputSchema = cloneJSONMap(tool.OutputSchema)
	tool.Annotations = cloneJSONMap(tool.Annotations)
	tool.StandardMetadata = cloneJSONMap(tool.StandardMetadata)
	return tool
}

func cloneJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = cloneJSONValue(item)
	}
	return out
}
func cloneJSONValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return cloneJSONMap(item)
	case []any:
		out := make([]any, len(item))
		for i, v := range item {
			out[i] = cloneJSONValue(v)
		}
		return out
	case []string:
		return append([]string{}, item...)
	default:
		return item
	}
}
