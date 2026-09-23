package mcp

import (
	"github.com/uvwt/agentdock/internal/contextguide"
	"strings"
)

const (
	baseServerInstructions  = contextguide.Reuse + " 处理多步骤任务时使用 task_manage 记录有价值的执行断点。"
	nexusServerInstructions = contextguide.Reuse + " 需要长期记忆时使用 recall_*，需要 Workflow 模板时使用 workflow_template_manage；多步骤任务使用 task_manage。"
)

func serverInstructions(nexusEnabled bool, custom string) string {
	instructions := baseServerInstructions
	if nexusEnabled {
		instructions = nexusServerInstructions
	}
	custom = strings.TrimSpace(custom)
	if custom == "" {
		return instructions
	}
	return instructions + "\n\nAdditional operator instructions:\n" + custom
}
