package app

import "github.com/uvwt/agentdock/internal/taskstate"

func taskIndexSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"active_count", "blocked_count", "returned_count", "truncated", "items"},
		"properties": map[string]any{
			"active_count":   map[string]any{"type": "integer", "minimum": 0},
			"blocked_count":  map[string]any{"type": "integer", "minimum": 0},
			"returned_count": map[string]any{"type": "integer", "minimum": 0, "maximum": taskstate.MaxTaskIndexItems},
			"truncated":      map[string]any{"type": "boolean"},
			"warnings":       map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string"}},
			"items": map[string]any{"type": "array", "maxItems": taskstate.MaxTaskIndexItems, "items": map[string]any{
				"type": "object", "required": []string{"task_id", "title", "status", "active_thread", "updated_at"},
				"properties": map[string]any{
					"task_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
					"status":  map[string]any{"type": "string", "enum": []string{"active", "blocked"}},
					"project": map[string]any{"type": "string"}, "workspace_id": map[string]any{"type": "string"},
					"updated_at":    map[string]any{"type": "string"},
					"active_thread": map[string]any{"type": "object", "additionalProperties": true, "required": []string{"thread_id", "title", "status"}},
				},
			}},
		},
	}
}
