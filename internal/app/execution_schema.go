package app

import "maps"

// executionOutputSchema preserves the complete business success contract and
// adds a separate, unmistakable not-yet-executed approval alternative.
func executionOutputSchema(original map[string]any) map[string]any {
	success := maps.Clone(original)
	if success == nil {
		success = map[string]any{"type": "object"}
	}
	properties, _ := success["properties"].(map[string]any)
	properties = maps.Clone(properties)
	if properties == nil {
		properties = map[string]any{}
	}
	for _, name := range []string{"conversation_id", "call_id", "parent_call_id", "retry_of_call_id", "task_id", "thread_id", "step_id", "workspace_id", "activity_label", "activity_warning", "binding_quality", "binding_warning"} {
		if _, exists := properties[name]; !exists {
			properties[name] = map[string]any{"type": "string"}
		}
	}
	for _, name := range []string{"agentdock_guidance", "permission", "workspace_target", "conversation_state"} {
		if _, exists := properties[name]; !exists {
			properties[name] = map[string]any{"type": "object", "additionalProperties": true}
		}
	}
	properties["binding_updated"] = map[string]any{"type": "boolean"}
	properties["agentdock_guidance"] = responseGuidanceSchema(properties["agentdock_guidance"])
	success["properties"] = properties
	pending := map[string]any{"type": "object", "properties": map[string]any{
		"status": map[string]any{"const": "pending_approval"}, "executed": map[string]any{"const": false},
		"approval_id": map[string]any{"type": "string", "pattern": "^approval_[a-f0-9]{32}$"},
		"call_id":     map[string]any{"type": "string", "pattern": "^call_[a-f0-9]{32}$"},
	}, "required": []string{"status", "executed", "approval_id", "call_id"}, "additionalProperties": true}
	visible := maps.Clone(properties)
	if old, exists := visible["status"]; exists {
		visible["status"] = map[string]any{"anyOf": []any{old, map[string]any{"const": "pending_approval"}}}
	}
	for _, name := range []string{"approval_id", "next_required_action"} {
		if _, exists := visible[name]; !exists {
			visible[name] = map[string]any{"type": "string"}
		}
	}
	visible["executed"] = map[string]any{"type": "boolean"}
	visible["approval"] = map[string]any{"type": "object"}
	result := map[string]any{"type": "object", "properties": visible, "anyOf": []any{success, pending}}
	for _, key := range []string{"$defs", "definitions"} {
		if value, exists := original[key]; exists {
			result[key] = value
		}
	}
	return result
}
