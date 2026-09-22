package mcp

import (
	"github.com/uvwt/agentdock/internal/activity"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

// ManageRequest 是 mcp_manage 进入动态 MCP capability 后的稳定输入契约。
type ManageRequest struct {
	Patch            mcpclient.ConfigPatch `json:"patch,omitempty"`
	Scope            string                `json:"scope,omitempty"`
	ExpectedRevision string                `json:"expected_revision,omitempty"`
	Action           string                `json:"action"`
	Name             string                `json:"name,omitempty"`
	Description      string                `json:"description,omitempty"`
	Transport        string                `json:"transport,omitempty"`
	URL              string                `json:"url,omitempty"`
	Command          string                `json:"command,omitempty"`
	Args             []string              `json:"args,omitempty"`
	CWD              string                `json:"cwd,omitempty"`
	HeaderEnv        map[string]string     `json:"header_env,omitempty"`
	EnvFromEnv       map[string]string     `json:"env_from_env,omitempty"`
	Key              string                `json:"key,omitempty"`
	Value            *string               `json:"value,omitempty"`
	Enabled          *bool                 `json:"enabled,omitempty"`
	TimeoutMS        *int                  `json:"timeout_ms,omitempty"`
}

type SearchRequest struct {
	Query  string `json:"query"`
	Server string `json:"server,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

type InspectRequest struct {
	Name string `json:"name"`
}

// CallRequest.Arguments 是第三方 MCP 工具 schema 决定的动态叶子，必须保持开放对象。
type CallRequest struct {
	activity.Binding `json:"-"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
}

func intValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
