package app

import (
	"github.com/uvwt/agentdock/internal/contextguide"
	"maps"

	"github.com/uvwt/agentdock/internal/agentinstructions"
	"github.com/uvwt/agentdock/internal/config"
)

type contextRequest struct {
	Workdir string `json:"workdir,omitempty"`
}

func contextToolSpecs() []ToolSpec {
	return []ToolSpec{{
		Name: "agentdock_context", Contract: contextToolContract, Title: "AgentDock context",
		Description: contextguide.Description,
		Handler:     ctxToolHandler((*Runtime).agentDockContextTool),
	}, {
		Name: "workspace_context", Contract: canonicalToolContract, Title: "Workspace context",
		Description: "Read only the selected workspace AGENTS.md rules and workspace-local Skill index. Use for a changed scope, not as a required second bootstrap. Selection is request-local and does not change command defaults.",
		Handler:     ctxToolHandler((*Runtime).workspaceContextTool),
	}}
}

// The standalone entrypoint adds optional local fields without changing the
// shared Nexus Bridge contract. All existing canonical fields remain identical.
func contextToolContract(name string, cfg config.Config) (ToolContract, bool) {
	contract, ok := canonicalToolContract(name, cfg)
	if !ok {
		return ToolContract{}, false
	}
	contract.InputSchema = maps.Clone(contract.InputSchema)
	input := maps.Clone(contract.InputSchema["properties"].(map[string]any))
	input["workdir"] = map[string]any{
		"type": "string", "maxLength": 4096,
		"description": "Existing host workspace directory. Omit or use an empty string to inherit this conversation's workspace, falling back to the device default only when unbound. A complete response coordinates subsequent conversation binding in this call; running sessions and device defaults remain unchanged.",
	}
	contract.InputSchema["properties"] = input
	contract.OutputSchema = maps.Clone(contract.OutputSchema)
	output := maps.Clone(contract.OutputSchema["properties"].(map[string]any))
	output["instruction_files"] = instructionFilesSchema()
	output["context_diagnostics"] = contextDiagnosticsSchema()
	output["plugins"] = pluginIndexSchema()
	output["tasks"] = taskIndexSchema()
	output["workspace"] = map[string]any{"type": "object", "additionalProperties": true, "required": []string{"workspace_id", "root", "runtime", "rules_revision"}}
	if cfg.NexusEndpoint == "" {
		if original, ok := output["dynamic_mcp"].(map[string]any); ok {
			index := maps.Clone(original)
			if originalItem, ok := index["items"].(map[string]any); ok {
				item := maps.Clone(originalItem)
				if originalProperties, ok := item["properties"].(map[string]any); ok {
					properties := maps.Clone(originalProperties)
					properties["revision"] = map[string]any{"type": "string"}
					properties["server_version"] = map[string]any{"type": "string"}
					properties["tool_count_known"] = map[string]any{"type": "boolean"}
					if _, exists := properties["source_type"]; !exists {
						properties["source_type"] = map[string]any{"type": "string"}
					}
					if _, exists := properties["plugin_name"]; !exists {
						properties["plugin_name"] = map[string]any{"type": "string"}
					}
					item["properties"] = properties
					index["items"] = item
					output["dynamic_mcp"] = index
				}
			}
		}
	}
	contract.OutputSchema["properties"] = output
	return contract, true
}

func instructionFilesSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"auto_load", "workdir", "workspace_root", "files"},
		"properties": map[string]any{
			"auto_load":      map[string]any{"type": "boolean"},
			"workdir":        map[string]any{"type": "string"},
			"workspace_root": map[string]any{"type": "string"},
			"files": map[string]any{
				"type": "array", "maxItems": agentinstructions.MaxDirectories + 1,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"scope", "path", "status"},
					"properties": map[string]any{
						"scope":        map[string]any{"type": "string", "enum": []string{"global", "workspace"}},
						"path":         map[string]any{"type": "string"},
						"status":       map[string]any{"type": "string", "enum": []string{"loaded", "not_found", "empty", "duplicate", "skipped", "error"}},
						"content":      map[string]any{"type": "string", "maxLength": agentinstructions.MaxFileBytes},
						"sha256":       map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
						"size_bytes":   map[string]any{"type": "integer", "minimum": 0},
						"reason":       map[string]any{"type": "string"},
						"duplicate_of": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}
