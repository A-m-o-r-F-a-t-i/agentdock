package contract

// ActivityProperties extends only the explicit execution entrypoints that own this contract.
func ActivityProperties(properties map[string]any) {
	for name, description := range map[string]string{
		"task_id":      "Persistent task associated with this execution. Omit only for a legacy unassigned operation.",
		"thread_id":    "Explicit task thread. Omit to resolve the selected task's active thread once at command start.",
		"step_id":      "Step within the selected thread; inherited from its checkpoint when omitted.",
		"workspace_id": "Registered workspace. Explicit selection takes priority over thread and task defaults.",
	} {
		properties[name] = map[string]any{"type": "string", "description": description, "maxLength": 80, "pattern": "^[A-Za-z0-9_-]{1,80}$"}
	}
	properties["activity_label"] = map[string]any{"type": "string", "description": "Short display label; it never replaces the actual command or execution status.", "maxLength": 512}
}

func TargetProperties(properties map[string]any) {
	properties["target_kind"] = map[string]any{"type": "string", "enum": []string{"source", "artifact", "scratch", "cache", "external"}, "description": "Semantic target under the selected workspace. artifact and scratch require task_id; external requires an explicit one-call external_path."}
	properties["external_path"] = String("Explicit absolute external target for this call only. It does not change the workspace or any default path.")
}
