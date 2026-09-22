package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	registry "github.com/uvwt/agentdock/internal/plugin"
)

type SkillItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
	Bundled     bool   `json:"bundled"`
	Enabled     bool   `json:"enabled"`
	Plugin      string `json:"plugin,omitempty"`
}

type MCPItem struct {
	Revision       string        `json:"revision"`
	ServerVersion  string        `json:"server_version,omitempty"`
	ToolCountKnown bool          `json:"tool_count_known"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	Plugin         string        `json:"plugin,omitempty"`
	Status         string        `json:"status"`
	ToolCount      int           `json:"tool_count"`
	LastErrorCode  string        `json:"last_error_code,omitempty"`
	ToolLoadError  string        `json:"tool_load_error,omitempty"`
	Enabled        bool          `json:"enabled"`
	Tools          []MCPToolItem `json:"tools"`
}

type MCPToolItem struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	Server        string `json:"server"`
}

type SkillLookup func(string) (SkillItem, bool, error)
type MCPLookup func(context.Context, string, bool) (MCPItem, bool, error)

type Service struct {
	store       *registry.Store
	skillLookup SkillLookup
	mcpLookup   MCPLookup
}

func New(store *registry.Store, skillLookup SkillLookup, mcpLookup MCPLookup) *Service {
	return &Service{store: store, skillLookup: skillLookup, mcpLookup: mcpLookup}
}

func (s *Service) Definitions() ([]registry.Definition, error) {
	return s.store.List()
}

func (s *Service) CapabilityItems() ([]registry.Definition, error) {
	definitions, err := s.store.List()
	if err != nil {
		return nil, err
	}
	items := make([]registry.Definition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Enabled && definition.Heavy {
			items = append(items, definition)
		}
	}
	return items, nil
}

func (s *Service) SkillMembership(name string) (registry.Membership, bool, error) {
	return s.store.SkillMembership(name)
}

func (s *Service) MCPMembership(name string) (registry.Membership, bool, error) {
	return s.store.MCPMembership(name)
}

func (s *Service) Manage(_ context.Context, request ManageRequest) (Result, error) {
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list":
		items, err := s.store.List()
		if err != nil {
			return nil, pluginToolError(err)
		}
		return Result{"action": action, "plugins": items, "count": len(items)}, nil
	case "inspect":
		definition, err := s.store.Get(request.Name)
		if err != nil {
			return nil, pluginToolError(err)
		}
		return Result{"action": action, "plugin": definition}, nil
	case "validate":
		definition, err := s.store.Validate(request.Source)
		if err != nil {
			return nil, pluginToolError(err)
		}
		return Result{"action": action, "valid": true, "plugin": definition}, nil
	case "install", "update":
		candidate, validateErr := s.store.Validate(request.Source)
		if validateErr != nil {
			return nil, pluginToolError(validateErr)
		}
		if action == "update" {
			if strings.TrimSpace(request.Name) == "" {
				return nil, toolErrorDetails("PLUGIN_NAME_REQUIRED", "plugin name is required for update", "validation", nil)
			}
			if candidate.Name != strings.TrimSpace(request.Name) {
				return nil, toolErrorDetails("PLUGIN_UPDATE_NAME_MISMATCH", "selected package does not match the plugin being updated", "validation", map[string]any{"expected": strings.TrimSpace(request.Name), "actual": candidate.Name})
			}
		}
		if ownershipErr := s.validateInstallOwnership(candidate); ownershipErr != nil {
			return nil, ownershipErr
		}
		definition, err := s.store.Install(request.Source, action == "update")
		if err != nil {
			return nil, pluginToolError(err)
		}
		return Result{"action": action, "plugin": definition}, nil
	case "remove":
		if err := s.store.Remove(request.Name); err != nil {
			return nil, pluginToolError(err)
		}
		return Result{"action": action, "name": strings.TrimSpace(request.Name), "removed": true}, nil
	case "enable", "disable":
		definition, err := s.store.SetEnabled(request.Name, action == "enable")
		if err != nil {
			return nil, pluginToolError(err)
		}
		return Result{"action": action, "plugin": definition}, nil
	case "heavy_enable", "heavy_disable":
		definition, err := s.store.SetHeavy(request.Name, action == "heavy_enable")
		if err != nil {
			return nil, pluginToolError(err)
		}
		return Result{"action": action, "plugin": definition}, nil
	case "member_enable", "member_disable":
		definition, err := s.store.SetMemberEnabled(request.Name, request.MemberType, request.Member, action == "member_enable")
		if err != nil {
			return nil, pluginToolError(err)
		}
		return Result{"action": action, "plugin": definition, "member_type": request.MemberType, "member": request.Member}, nil
	default:
		return nil, toolErrorDetails("INVALID_ACTION", "unsupported plugin_manage action", "validation", map[string]any{
			"action":  action,
			"allowed": []string{"list", "inspect", "validate", "install", "update", "remove", "enable", "disable", "heavy_enable", "heavy_disable", "member_enable", "member_disable"},
		})
	}
}

func (s *Service) validateInstallOwnership(candidate registry.Definition) error {
	for _, name := range candidate.Skills {
		item, found, err := s.skillLookup(name)
		if err != nil {
			return toolErrorCause("PLUGIN_MEMBER_LOOKUP_FAILED", "check Skill ownership before plugin install", "runtime", map[string]any{"skill": name}, err)
		}
		if found && item.Plugin != candidate.Name {
			return toolErrorDetails("PLUGIN_MEMBER_CONFLICT", "plugin Skill conflicts with an existing standalone or plugin-owned Skill", "validation", map[string]any{"member_type": "skill", "member": name, "owner": item.Plugin})
		}
	}
	for _, name := range candidate.MCPServers {
		item, found, err := s.mcpLookup(context.Background(), name, false)
		if err != nil {
			return toolErrorCause("PLUGIN_MEMBER_LOOKUP_FAILED", "check MCP ownership before plugin install", "runtime", map[string]any{"mcp_server": name}, err)
		}
		if found && item.Plugin != candidate.Name {
			return toolErrorDetails("PLUGIN_MEMBER_CONFLICT", "plugin MCP server conflicts with an existing standalone or plugin-owned server", "validation", map[string]any{"member_type": "mcp_server", "member": name, "owner": item.Plugin})
		}
	}
	return nil
}

func (s *Service) Load(ctx context.Context, request LoadRequest) (Result, error) {
	definition, err := s.store.Get(request.Name)
	if err != nil {
		return nil, pluginToolError(err)
	}
	if !definition.Enabled {
		return nil, toolErrorDetails("PLUGIN_DISABLED", "plugin is disabled", "validation", map[string]any{"plugin": definition.Name})
	}

	skillItems := make([]SkillItem, 0, len(definition.Skills))
	mcpServers := make([]MCPItem, 0, len(definition.MCPServers))
	unavailable := make([]map[string]any, 0)
	for _, name := range definition.Skills {
		item, found, lookupErr := s.skillLookup(name)
		if lookupErr != nil {
			return nil, toolErrorCause("PLUGIN_MEMBER_LOOKUP_FAILED", "load plugin Skill member", "runtime", map[string]any{"plugin": definition.Name, "skill": name}, lookupErr)
		}
		if !found || !item.Enabled {
			reason := "missing"
			if found {
				reason = "disabled"
			}
			unavailable = append(unavailable, map[string]any{"type": "skill", "name": name, "reason": reason})
			continue
		}
		skillItems = append(skillItems, item)
	}
	for _, name := range definition.MCPServers {
		item, found, lookupErr := s.mcpLookup(ctx, name, true)
		if lookupErr != nil {
			return nil, toolErrorCause("PLUGIN_MEMBER_LOOKUP_FAILED", "load plugin MCP member", "runtime", map[string]any{"plugin": definition.Name, "mcp_server": name}, lookupErr)
		}
		if !found || !item.Enabled {
			reason := "missing"
			if found {
				reason = "disabled"
			}
			unavailable = append(unavailable, map[string]any{"type": "mcp_server", "name": name, "reason": reason})
			continue
		}
		if item.ToolLoadError != "" {
			unavailable = append(unavailable, map[string]any{
				"type": "mcp_server", "name": name, "reason": "tool_discovery_failed",
				"code": item.LastErrorCode, "message": item.ToolLoadError,
			})
		}
		mcpServers = append(mcpServers, item)
	}

	instructions := []string{}
	if len(skillItems) > 0 {
		instructions = append(instructions, "Read a returned Skill entry point before applying its domain workflow.")
	}
	if len(mcpServers) > 0 {
		instructions = append(instructions, "Select a returned MCP tool description, inspect its qualified_name, then call it. Use mcp_tool_search with the returned server name to refresh or narrow the index.")
	}
	return Result{
		"plugin": map[string]any{
			"name": definition.Name, "description": definition.Description,
			"version": definition.Version, "path": definition.Path, "enabled": definition.Enabled,
		},
		"skills": skillItems, "mcp_servers": mcpServers,
		"unavailable_members": unavailable, "instructions": instructions,
	}, nil
}

func pluginToolError(err error) error {
	var registryErr *registry.Error
	if !errors.As(err, &registryErr) {
		return toolErrorCause("PLUGIN_ERROR", err.Error(), "runtime", nil, err)
	}
	category := "validation"
	switch registryErr.Code {
	case "PLUGIN_STORE_READ_FAILED", "PLUGIN_STORE_WRITE_FAILED", "PLUGIN_STORE_LOCK_FAILED", "PLUGIN_STATE_WRITE_FAILED", "PLUGIN_INSTALL_FAILED":
		category = "runtime"
	case "PLUGIN_NOT_FOUND", "PLUGIN_MEMBER_NOT_FOUND":
		category = "not_found"
	}
	message := registryErr.Message
	if message == "" {
		message = fmt.Sprintf("plugin operation failed: %s", registryErr.Code)
	}
	return toolErrorCause(registryErr.Code, message, category, registryErr.Details, err)
}
