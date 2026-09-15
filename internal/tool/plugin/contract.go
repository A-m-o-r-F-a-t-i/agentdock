package plugin

import toolcontract "github.com/uvwt/agentdock/internal/tool/contract"

const (
	ToolManage = "plugin_manage"
	ToolLoad   = "plugin_load"
)

func InputSchema(name string) (map[string]any, bool) {
	stringProp := toolcontract.String
	switch name {
	case ToolManage:
		return toolcontract.InputObject(map[string]any{
			"action": map[string]any{
				"type": "string", "description": "Standard plugin package or host-state action.",
				"enum": []string{"list", "inspect", "validate", "install", "update", "remove", "enable", "disable", "heavy_enable", "heavy_disable", "member_enable", "member_disable"},
			},
			"name":        stringProp("Installed plugin identifier for inspect, remove, switch, or member actions."),
			"source":      stringProp("Local plugin directory or ZIP archive for validate, install, or update. It must contain root plugin.json using Agent Plugins 1.0.0."),
			"member_type": map[string]any{"type": "string", "enum": []string{"skill", "mcp_server"}, "description": "Plugin member kind for member_enable/member_disable."},
			"member":      stringProp("Plugin-owned Skill or MCP server name for member_enable/member_disable."),
		}, "action"), true
	case ToolLoad:
		return toolcontract.InputObject(map[string]any{
			"name": stringProp("Enabled plugin name from agentdock_context."),
		}, "name"), true
	default:
		return nil, false
	}
}

func OutputSchema(name string) (map[string]any, bool) {
	stringProp := toolcontract.String
	intProp := toolcontract.Integer
	boolProp := toolcontract.Boolean
	arrayProp := toolcontract.ObjectArray
	stringArrayProp := toolcontract.StringArray
	objectProp := toolcontract.OpenObject
	switch name {
	case ToolManage:
		return toolcontract.OutputObject(map[string]any{
			"action":      stringProp("Completed direct-plugin package action."),
			"plugins":     arrayProp("Installed self-contained plugin definitions."),
			"plugin":      objectProp("Installed or validated plugin definition."),
			"count":       intProp("Installed plugin count."),
			"name":        stringProp("Plugin name."),
			"member_type": stringProp("Changed plugin member type."),
			"member":      stringProp("Changed plugin member name."),
			"valid":       boolProp("Whether a plugin source passed validation."),
			"removed":     boolProp("Whether the plugin directory was removed."),
		}), true
	case ToolLoad:
		return toolcontract.OutputObject(map[string]any{
			"plugin":              objectProp("Loaded plugin summary, version, and installed path."),
			"skills":              arrayProp("Enabled plugin-contained Skill descriptions and skill:// entry points."),
			"mcp_servers":         arrayProp("Enabled plugin-contained MCP server descriptions and lazily loaded tool index."),
			"unavailable_members": arrayProp("Plugin members that are disabled, missing, or whose MCP tool discovery failed."),
			"instructions":        stringArrayProp("Progressive-disclosure next actions."),
		}), true
	default:
		return nil, false
	}
}
