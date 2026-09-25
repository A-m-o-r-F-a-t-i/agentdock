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

func (r *Runtime) MCPPresentationSettings() config.DisplaySettings {
	if r.display == nil {
		return config.DisplaySettings{SchemaVersion: 2, Revision: 1, ChatGPTMCPUIEnabled: r.cfg.MCPAppsEnabled, ToolOutput: config.DefaultToolOutputSettings()}
	}
	return r.display.Snapshot()
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
	return displayResult(r.MCPPresentationSettings()), nil
}

func displayResult(settings config.DisplaySettings) Result {
	return Result{"schema_version": settings.SchemaVersion, "revision": settings.Revision,
		"chatgpt_mcp_ui_enabled": settings.ChatGPTMCPUIEnabled, "warning": settings.Warning,
		"tool_output": settings.ToolOutput, "tool_output_unit": "unicode_scalar",
		"server_policy_applied": true, "host_adoption": "unknown",
		"refresh_hint": "工具目录和模板策略已更新，后续请求使用当前设置。旧模板引用在限时兼容期内返回无脚本提示；已渲染的历史卡片不会删除，宿主采纳状态仍为未知。"}
}

func (r *Runtime) RuntimeUpdateDisplaySettings(ctx context.Context, change config.DisplayChange) (Result, error) {
	if change.ChatGPTMCPUIEnabled == nil && change.ToolOutput == nil {
		return nil, toolError("MISSING_DISPLAY_VALUE", "a display setting is required", "validation")
	}
	if change.ToolOutput != nil {
		if err := change.ToolOutput.Validate(); err != nil {
			return nil, toolError("INVALID_TOOL_OUTPUT", err.Error(), "validation")
		}
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
