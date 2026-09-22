package app

func contextToolSpecs() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "agentdock_context",
			Contract:    canonicalToolContract,
			Title:       "AgentDock context",
			Description: "Return structured AgentDock bootstrap context including available capabilities, integrations, rules, and high-priority context.",
			Handler:     ctxToolHandler((*Runtime).agentDockContextTool),
		},
		{
			Name:        "workspace_context",
			Contract:    canonicalToolContract,
			Title:       "Workspace context",
			Description: "Read the current workspace AGENTS.md inheritance chain and workspace-local Skill index. Call before project operations, after switching workspaces, or when workspace rules may have changed. workdir selection is request-local and does not change command defaults.",
			Handler:     ctxToolHandler((*Runtime).workspaceContextTool),
		},
	}
}
