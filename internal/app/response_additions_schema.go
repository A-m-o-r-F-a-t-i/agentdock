package app

import "maps"

func responseGuidanceSchema(original any) map[string]any {
	input, _ := original.(map[string]any)
	result := maps.Clone(input)
	if result == nil {
		result = map[string]any{"type": "object", "additionalProperties": true}
	}
	inputProperties, _ := result["properties"].(map[string]any)
	properties := maps.Clone(inputProperties)
	if properties == nil {
		properties = map[string]any{}
	}
	text := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	properties["response_additions"] = map[string]any{"type": "array", "items": map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"type", "version", "insertion_id", "sequence", "conversation_id", "text"},
		"properties": map[string]any{
			"type":         map[string]any{"type": "string", "const": "activity_center_user"},
			"version":      map[string]any{"type": "integer", "const": 1},
			"insertion_id": text(), "conversation_id": text(), "text": text(),
			"sequence":         map[string]any{"type": "integer", "minimum": 1},
			"receipt_token":    map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$"},
			"delivery_attempt": map[string]any{"type": "integer", "minimum": 1, "maximum": 6},
		},
	}}
	result["properties"] = properties
	return result
}
