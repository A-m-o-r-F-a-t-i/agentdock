package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/uvwt/agentdock/internal/envstore"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

type Service struct {
	mcpClients       *mcpclient.Manager
	envs             *envstore.Store
	pluginMembership PluginMembershipLookup
	pluginHeavy      func(string) (bool, error)
}

type PluginMembershipLookup func(string) (plugin string, enabled bool, owned bool, err error)

func New(manager *mcpclient.Manager, envs *envstore.Store) *Service {
	return &Service{mcpClients: manager, envs: envs}
}

func (s *Service) SetPluginMembershipLookup(lookup PluginMembershipLookup) {
	s.pluginMembership = lookup
}

func (s *Service) SetHeavyPluginLookup(lookup func(string) (bool, error)) {
	s.pluginHeavy = lookup
}

type CapabilityItem struct {
	Revision       string
	ServerVersion  string
	ToolCountKnown bool
	Name           string
	Description    string
	Plugin         string
	Enabled        bool
	Status         string
	ToolCount      int
	LastErrorCode  string
	ToolLoadError  string
}

type CapabilityToolItem struct {
	Name          string
	QualifiedName string
	Title         string
	Description   string
	Server        string
}

func (s *Service) CapabilityItems() []CapabilityItem {
	servers := s.mcpClients.EnabledIndex()
	items := make([]CapabilityItem, 0, len(servers))
	for _, server := range servers {
		if err := s.ensureAvailable(server.Name); err != nil {
			continue
		}
		items = append(items, CapabilityItem{
			Name: server.Name, Description: server.Description, Plugin: s.pluginName(server.Name),
			Revision: server.Revision, ServerVersion: server.ServerVersion, ToolCountKnown: server.ToolCountKnown,
			Enabled: server.Enabled, Status: server.Status,
			ToolCount: server.ToolCount, LastErrorCode: server.LastErrorCode,
		})
	}
	return items
}

func (s *Service) CapabilityItem(name string) (CapabilityItem, bool, error) {
	_, server, err := s.mcpClients.Inspect(strings.TrimSpace(name))
	if err != nil {
		var mcpErr *mcpclient.Error
		if errors.As(err, &mcpErr) && mcpErr.Code == "MCP_SERVER_NOT_FOUND" {
			return CapabilityItem{}, false, nil
		}
		return CapabilityItem{}, false, err
	}
	return CapabilityItem{
		Name: server.Name, Description: server.Description, Plugin: s.pluginName(server.Name),
		Revision: server.Revision, ServerVersion: server.ServerVersion, ToolCountKnown: server.ToolCountKnown,
		Enabled: server.Enabled, Status: server.Status,
		ToolCount: server.ToolCount, LastErrorCode: server.LastErrorCode,
	}, true, nil
}

func (s *Service) pluginName(name string) string {
	if s.pluginMembership == nil {
		return ""
	}
	plugin, _, owned, err := s.pluginMembership(strings.TrimSpace(name))
	if err != nil || !owned {
		return ""
	}
	return plugin
}

// PluginCapabilityItem expands the complete MCP tool-description index only
// after the owning plugin has been explicitly loaded.
func (s *Service) PluginCapabilityItem(ctx context.Context, name string) (CapabilityItem, []CapabilityToolItem, bool, error) {
	item, found, err := s.CapabilityItem(name)
	if err != nil || !found || !item.Enabled {
		return item, nil, found, err
	}
	if err := s.ensureAvailable(name); err != nil {
		return CapabilityItem{}, nil, false, err
	}
	tools, err := s.mcpClients.ListTools(ctx, name)
	if err != nil {
		item.Status = "error"
		item.ToolLoadError = err.Error()
		var mcpErr *mcpclient.Error
		if errors.As(err, &mcpErr) {
			item.LastErrorCode = mcpErr.Code
		}
		return item, nil, true, nil
	}
	items := make([]CapabilityToolItem, 0, len(tools))
	for _, tool := range tools {
		items = append(items, CapabilityToolItem{
			Name: tool.Name, QualifiedName: tool.QualifiedName, Title: tool.Title,
			Description: tool.Description, Server: tool.Server,
		})
	}
	if refreshed, ok, refreshErr := s.CapabilityItem(name); refreshErr == nil && ok {
		item = refreshed
	}
	item.Status = "ready"
	item.ToolCountKnown = true
	item.ToolCount = len(items)
	item.LastErrorCode = ""
	return item, items, true, nil
}

func (s *Service) ensureAvailable(name string) error {
	if s.pluginMembership == nil {
		return nil
	}
	plugin, enabled, owned, err := s.pluginMembership(strings.TrimSpace(name))
	if err != nil {
		return toolErrorCause("PLUGIN_STATE_INVALID", "read MCP plugin availability", "runtime", map[string]any{"server": name}, err)
	}
	if owned && !enabled {
		return toolErrorDetails("PLUGIN_DISABLED", "MCP server belongs to a disabled plugin", "validation", map[string]any{"server": name, "plugin": plugin})
	}
	return nil
}

// PermissionTargetFingerprint freezes the installed destination and scoped
// environment without connecting or dispatching the third-party tool.
func (s *Service) PermissionTargetFingerprint(ctx context.Context, name string) (string, error) {
	return s.mcpClients.PermissionTargetFingerprint(ctx, name)
}

func (s *Service) Close() error {
	if s == nil || s.mcpClients == nil {
		return nil
	}
	return s.mcpClients.Close()
}

func (s *Service) envAction(kind envstore.ScopeKind, name, action string, request ManageRequest) (Result, error) {
	scope := envstore.Scope{Kind: kind, Name: strings.TrimSpace(name)}
	switch action {
	case "env_set":
		key := strings.TrimSpace(request.Key)
		if key == "" || request.Value == nil {
			return nil, toolErrorDetails("VALIDATION_ERROR", "key and value are required for env_set", "validation", map[string]any{"scope": scope.Name})
		}
		text := *request.Value
		if err := s.envs.Set(scope, key, text); err != nil {
			return nil, scopedEnvToolError(scope, err)
		}
		return Result{"action": action, "name": scope.Name, "key": key, "configured": text != ""}, nil
	case "env_unset":
		key := strings.TrimSpace(request.Key)
		if key == "" {
			return nil, toolErrorDetails("VALIDATION_ERROR", "key is required for env_unset", "validation", map[string]any{"scope": scope.Name})
		}
		removed, err := s.envs.Unset(scope, key)
		if err != nil {
			return nil, scopedEnvToolError(scope, err)
		}
		return Result{"action": action, "name": scope.Name, "key": key, "removed": removed}, nil
	case "env_list":
		items, err := s.envs.List(scope)
		if err != nil {
			return nil, scopedEnvToolError(scope, err)
		}
		return Result{"action": action, "name": scope.Name, "items": items, "count": len(items)}, nil
	default:
		return nil, toolErrorDetails("INVALID_ACTION", "unsupported environment action", "validation", map[string]any{"action": action})
	}
}

func scopedEnvToolError(scope envstore.Scope, err error) error {
	return toolErrorDetails("ENV_STORE_ERROR", "manage scoped environment", "validation", map[string]any{
		"kind": scope.Kind, "name": scope.Name, "reason": err.Error(),
	})
}
