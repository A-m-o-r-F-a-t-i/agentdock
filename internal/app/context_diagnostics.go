package app

// Diagnostics are a standalone extension; the shared Bridge schema is unchanged.
// Build metrics describe the referenced build, not work charged to every waiter.
func contextDiagnosticsSchema() map[string]any {
	milliseconds := func() map[string]any { return map[string]any{"type": "number", "minimum": 0} }
	count := func() map[string]any { return map[string]any{"type": "integer", "minimum": 0} }
	snapshot := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"cache_hit", "shared_build", "build_id", "shared_wait_ms"},
			"properties": map[string]any{
				"cache_hit": map[string]any{"type": "boolean"}, "shared_build": map[string]any{"type": "boolean"},
				"build_id": count(), "shared_wait_ms": milliseconds(),
				"build_document_reads": count(),
			}}
	}
	properties := map[string]any{
		"complete":            map[string]any{"type": "boolean"},
		"components_complete": map[string]any{"type": "boolean"},
		"binding_status":      map[string]any{"type": "string", "enum": []string{"pending", "updated", "unchanged", "unattributed", "context_incomplete", "conflict", "failed"}},
		"plugin_revision":     map[string]any{"type": "string"},
		"plugin_snapshot":     snapshot(), "skill_snapshot": snapshot(),
		"common_snapshot": snapshot(),
		"plugin_build": map[string]any{"type": "object", "additionalProperties": false,
			"required":   []string{"plugin_scans", "skill_documents", "lock_wait_ms", "build_elapsed_ms"},
			"properties": map[string]any{"plugin_scans": count(), "skill_documents": count(), "lock_wait_ms": milliseconds(), "build_elapsed_ms": milliseconds()}},
	}
	for _, name := range []string{"rules_ms", "plugins_ms", "skills_ms", "mcp_catalog_ms", "serialization_ms", "context_ms", "common_skills_ms", "task_index_ms"} {
		properties[name] = milliseconds()
	}
	return map[string]any{"type": "object", "additionalProperties": false,
		"required":   []string{"complete", "plugin_snapshot", "skill_snapshot", "plugin_build", "plugin_revision", "rules_ms", "plugins_ms", "skills_ms", "mcp_catalog_ms", "serialization_ms", "context_ms"},
		"properties": properties}
}
