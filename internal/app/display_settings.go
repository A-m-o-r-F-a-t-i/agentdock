package app

import (
	"context"
	"errors"

	"github.com/uvwt/agentdock/internal/config"
)

func (r *Runtime) ChatGPTMCPUIEnabled() bool {
	if r.display == nil {
		return r.cfg.MCPAppsEnabled
	}
	return r.display.Snapshot().ChatGPTMCPUIEnabled
}

func (r *Runtime) OnDisplaySettingsChanged(listener func()) {
	if r.display != nil {
		r.display.Subscribe(listener)
	}
}

func (r *Runtime) RuntimeDisplaySettings(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return displayResult(r.display.Snapshot()), nil
}

func displayResult(settings config.DisplaySettings) Result {
	return Result{"schema_version": settings.SchemaVersion, "revision": settings.Revision,
		"chatgpt_mcp_ui_enabled": settings.ChatGPTMCPUIEnabled, "warning": settings.Warning,
		"server_policy_applied": true, "host_adoption": "unknown",
		"refresh_hint": "设置仅控制 AgentDock 提供的内嵌界面。已渲染的历史卡片不会删除；请刷新 ChatGPT 连接并在新对话中验证。"}
}

func (r *Runtime) RuntimeUpdateDisplaySettings(ctx context.Context, change config.DisplayChange) (Result, error) {
	if change.ChatGPTMCPUIEnabled == nil {
		return nil, toolError("MISSING_DISPLAY_VALUE", "chatgpt_mcp_ui_enabled is required", "validation")
	}
	settings, err := r.display.Update(ctx, change)
	if errors.Is(err, config.ErrDisplayRevision) {
		return nil, toolError("DISPLAY_REVISION_CONFLICT", err.Error(), "conflict")
	}
	if err != nil {
		return nil, toolError("DISPLAY_SAVE_FAILED", err.Error(), "runtime")
	}
	return displayResult(settings), nil
}
