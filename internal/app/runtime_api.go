package app

import (
	"context"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/config"
	toolmcp "github.com/uvwt/agentdock/internal/tool/mcp"
	toolplugin "github.com/uvwt/agentdock/internal/tool/plugin"
	toolskill "github.com/uvwt/agentdock/internal/tool/skill"
)

const runtimeAPISource = "agentdock-api"

func (r *Runtime) RuntimeStatus() Result {
	tools := r.ToolNames()
	return Result{
		"ok":                    true,
		"source":                runtimeAPISource,
		"service":               config.ServerName,
		"version":               buildinfo.Version,
		"agentdock_home":        r.cfg.AgentDockHome,
		"agentdock_default_dir": r.cfg.AgentDockDefaultDir,
		"path_model":            config.PathModel,
		"auth_enabled":          r.cfg.AuthRequired(),
		"browser_enabled":       r.cfg.BrowserEnabled,
		"memory_enabled":        r.cfg.NexusEndpoint != "",
		"nexus_enabled":         strings.TrimSpace(r.cfg.NexusEndpoint) != "",
		"tool_count":            len(tools),
		"tools":                 tools,
	}
}

func (r *Runtime) RuntimeSkills() (Result, error) {
	return r.skills.RuntimeSkills()
}

func (r *Runtime) RuntimeSkillSummaries() (Result, error) {
	return r.skills.RuntimeSkillSummaries()
}

func (r *Runtime) RuntimeSkill(skill string) (Result, error) {
	return r.skills.RuntimeSkill(skill)
}

func (r *Runtime) RuntimeSkillManage(ctx context.Context, args map[string]any) (Result, error) {
	result, err := r.Call(WithLocalUserAction(ctx), toolskill.ToolPackage, args)
	if err != nil {
		return nil, err
	}
	result["ok"] = true
	result["source"] = runtimeAPISource
	return result, nil
}

func (r *Runtime) RuntimeSkillFiles(skill string) (Result, error) {
	return r.skills.RuntimeSkillFiles(skill)
}

func (r *Runtime) RuntimeSkillFile(skill, relativePath string) (Result, error) {
	return r.skills.RuntimeSkillFile(skill, relativePath)
}

func (r *Runtime) RuntimeTasks(status string, limit int) (Result, error) {
	return r.taskTools.RuntimeTasks(status, limit)
}

func (r *Runtime) RuntimeTask(id string) (Result, error) {
	return r.taskTools.RuntimeTask(id)
}

func (r *Runtime) RuntimeTaskDelete(id string) (Result, error) {
	selected, err := r.tasks.Get(id)
	if err != nil {
		return nil, toolError("TASK_NOT_FOUND", err.Error(), "not_found")
	}
	batch, err := r.RuntimeManagementBatch(context.Background(), "task", BatchRequest{IDs: []string{id}, Action: "delete", ConfirmPermanent: true})
	if err != nil {
		return nil, err
	}
	if len(batch.Items) != 1 {
		return nil, toolError("TASK_DELETE_FAILED", "No per-item deletion result was returned.", "runtime")
	}
	if batch.Items[0].Status != "succeeded" {
		return nil, toolError("TASK_DELETE_PROTECTED", batch.Items[0].Message, "conflict")
	}
	return Result{"ok": true, "source": runtimeAPISource, "action": "delete", "task_id": id, "management_only": true, "deleted_task": selected}, nil
}

func (r *Runtime) RuntimeCapabilities(ctx context.Context, refresh bool) (Result, error) {
	result, err := r.AgentDockContext(activity.WithDiagnostic(ctx))
	if err != nil {
		return nil, err
	}
	result["source"] = runtimeAPISource
	return result, nil
}

func (r *Runtime) RuntimeMCPServers(ctx context.Context) (Result, error) {
	return r.runtimeMCPManage(ctx, map[string]any{"action": "list"})
}

func (r *Runtime) RuntimeMCPServer(ctx context.Context, name string) (Result, error) {
	return r.runtimeMCPManage(ctx, map[string]any{"action": "inspect", "name": name})
}

func (r *Runtime) RuntimeMCPManage(ctx context.Context, args map[string]any) (Result, error) {
	result, err := r.Call(WithLocalUserAction(ctx), toolmcp.ToolManage, args)
	if err != nil {
		return nil, err
	}
	result["ok"] = true
	result["source"] = runtimeAPISource
	return result, nil
}

func (r *Runtime) RuntimePlugins(ctx context.Context) (Result, error) {
	return r.runtimePluginManage(ctx, map[string]any{"action": "list"})
}

func (r *Runtime) RuntimePlugin(ctx context.Context, name string) (Result, error) {
	return r.runtimePluginManage(ctx, map[string]any{"action": "inspect", "name": name})
}

func (r *Runtime) RuntimePluginManage(ctx context.Context, args map[string]any) (Result, error) {
	result, err := r.Call(WithLocalUserAction(ctx), toolplugin.ToolManage, args)
	if err != nil {
		return nil, err
	}
	result["ok"] = true
	result["source"] = runtimeAPISource
	return result, nil
}

func (r *Runtime) runtimePluginManage(ctx context.Context, args map[string]any) (Result, error) {
	if err := r.validateToolArguments(toolplugin.ToolManage, args); err != nil {
		return nil, err
	}
	var request toolplugin.ManageRequest
	if err := decodeToolInput(toolplugin.ToolManage, args, &request); err != nil {
		return nil, err
	}
	result, err := r.plugins.Manage(ctx, request)
	if err != nil {
		return nil, err
	}
	result["ok"] = true
	result["source"] = runtimeAPISource
	return result, nil
}

func (r *Runtime) runtimeMCPManage(ctx context.Context, args map[string]any) (Result, error) {
	if err := r.validateToolArguments(toolmcp.ToolManage, args); err != nil {
		return nil, err
	}
	var request toolmcp.ManageRequest
	if err := decodeToolInput("mcp_manage", args, &request); err != nil {
		return nil, err
	}
	result, err := r.dynamicMCP.Manage(ctx, request)
	if err != nil {
		return nil, err
	}
	result["ok"] = true
	result["source"] = runtimeAPISource
	return result, nil
}

func (r *Runtime) RuntimeCompletionNotifications(ctx context.Context, limit int) (Result, error) {
	if !activity.IsLocalManagement(ctx) {
		return nil, activity.ErrConversationOwner
	}
	items, err := r.tasks.ClaimCompletionNotifications(ctx, limit, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return Result{"notifications": items}, nil
}
