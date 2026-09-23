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
		props := map[string]any{
			"action": map[string]any{
				"type": "string", "description": "Standard plugin package or host-state action.",
				"enum": []string{"list", "inspect", "validate", "install", "update", "remove", "enable", "disable", "heavy_enable", "heavy_disable", "member_enable", "member_disable", "catalog"},
			},
			"name": stringProp("Installed plugin identifier for inspect, remove, switch, or member actions."),
			"source_type": map[string]any{
				"type": "string", "description": "Source transport. auto detects local/Git/ZIP; catalog resolves one read-only catalog entry.",
				"enum": []string{"auto", "local", "git", "archive", "catalog"},
			},
			"source_adapter": map[string]any{
				"type": "string", "description": "Plugin format adapter. auto detects portable/OpenAI/Claude.",
				"enum": []string{"auto", "portable", "openai", "claude"},
			},
			"source_version": toolcontract.String("SemVer fallback for an external manifest that omits version."),
			"git_ref":        toolcontract.String("Optional Git branch/tag to resolve. A full git_commit pin takes precedence for identity verification."),
			"git_commit":     toolcontract.String("Optional full 40-character Git commit pin."),
			"subdir":         toolcontract.String("Optional safe subdirectory inside a Git/archive source."),
			"sha256":         toolcontract.String("Optional expected SHA-256 pin for an HTTPS ZIP archive."),
			"catalog": map[string]any{
				"type": "string", "description": "Catalog format for action=catalog or source_type=catalog.",
				"enum": []string{"auto", "openai", "claude"},
			},
			"catalog_item":            toolcontract.String("Catalog entry name when source_type=catalog."),
			"enabled":                 toolcontract.Boolean("Initial enabled state for install. Defaults to true."),
			"confirmed":               toolcontract.Boolean("Required for install/update after reviewing plugin_manage validate output."),
			"confirmed_source_change": toolcontract.Boolean("For update, explicitly confirm rebinding an installed Plugin to a different source."),
			"source":                  stringProp("Local directory, ZIP, Git or catalog source. External OpenAI/Claude formats are normalized into the same direct plugin store."),
			"member_type":             map[string]any{"type": "string", "enum": []string{"skill", "mcp_server"}, "description": "Plugin member kind for member_enable/member_disable."},
			"member":                  stringProp("Plugin-owned Skill or MCP server name for member_enable/member_disable."),
		}
		toolcontract.ActivityProperties(props)
		return toolcontract.InputObject(props, "action"), true
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
			"review":      objectProp("Static source, compatibility and security review."),
			"catalog":     objectProp("Read-only external catalog."),
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
