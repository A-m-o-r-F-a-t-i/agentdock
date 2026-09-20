package contract

// ActivityProperties retains optional advanced overrides on existing execution
// tools. Conversation identity is exclusively resolved by the trusted ingress.
func ActivityProperties(properties map[string]any) {
	properties["retry_of_call_id"] = map[string]any{"type": "string", "pattern": "^call_[a-f0-9]{32}$", "maxLength": 37, "description": "Explicit retry of a previous call in this conversation. A new call_id is generated. Never automatically retry a call whose side-effect result is unknown."}
	for name, description := range map[string]string{
		"task_id":      "Optional advanced task override, validated against the server's current conversation binding. Normally omit: task creation, resume or set_current establishes the binding once.",
		"thread_id":    "Optional advanced task-thread override. Normally omit: the server inherits a fixed snapshot of the conversation's current task thread.",
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
