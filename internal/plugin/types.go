package plugin

import mcpclient "github.com/uvwt/agentdock/internal/mcp/client"

const (
	ManifestDirectory  = ""
	ManifestFilename   = "plugin.json"
	MCPFilename        = "mcp.json"
	StateFilename      = "state.json"
	ManifestSchema     = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	MCPSchema          = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
	ExtensionNamespace = "io.github.uvwt.agentdock"
)

// Manifest contains only the portable Agent Plugins 1.0.0 fields.
// Installation state and credential references belong to the host store.
type Manifest struct {
	Schema      string            `json:"$schema"`
	Name        string            `json:"name"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	Author      map[string]string `json:"author,omitempty"`
	Homepage    string            `json:"homepage,omitempty"`
	Repository  string            `json:"repository,omitempty"`
	License     string            `json:"license,omitempty"`
	Keywords    []string          `json:"keywords,omitempty"`
	Extensions  map[string]any    `json:"extensions,omitempty"`
}

type MCPConfig struct {
	Schema     string               `json:"$schema"`
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

type MCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// HostConfig is stored outside the package. Actual secrets remain in env/mcp.
type HostConfig struct {
	MCPServers map[string]MCPHostConfig `json:"mcp_servers,omitempty"`
}

type MCPHostConfig struct {
	Description string            `json:"description,omitempty"`
	HeaderEnv   map[string]string `json:"header_env,omitempty"`
	EnvFromEnv  map[string]string `json:"env_from_env,omitempty"`
	TimeoutMS   int               `json:"timeout_ms,omitempty"`
	Command     string            `json:"command,omitempty"`
}

// State is never imported from an untrusted plugin package. Heavy is a host
// override; nil follows the default supplied in the namespaced extension.
type State struct {
	Source        *Source         `json:"source,omitempty"`
	Compatibility *Compatibility  `json:"compatibility,omitempty"`
	Enabled       bool            `json:"enabled"`
	Heavy         *bool           `json:"heavy,omitempty"`
	Skills        map[string]bool `json:"skills,omitempty"`
	MCPServers    map[string]bool `json:"mcpServers,omitempty"`
}

type Definition struct {
	Source        *Source        `json:"source,omitempty"`
	Compatibility *Compatibility `json:"compatibility,omitempty"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Version       string         `json:"version"`
	Path          string         `json:"path"`
	Enabled       bool           `json:"enabled"`
	Heavy         bool           `json:"heavy"`
	Skills        []string       `json:"skills,omitempty"`
	MCPServers    []string       `json:"mcp_servers,omitempty"`
	Diagnostics   []string       `json:"diagnostics,omitempty"`
}

type Membership struct {
	Plugin  string `json:"plugin"`
	Enabled bool   `json:"enabled"`
	Heavy   bool   `json:"heavy"`
}

type SkillMember struct {
	Heavy       bool   `json:"heavy,omitempty"`
	Description string `json:"description,omitempty"`
	Name        string `json:"name"`
	Plugin      string `json:"plugin"`
	Path        string `json:"path"`
	Enabled     bool   `json:"enabled"`
}

type MCPMember struct {
	Plugin string
	Config mcpclient.ServerConfig
}

type Error struct {
	Code    string
	Message string
	Details map[string]any
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func newError(code, message string, details map[string]any, cause error) *Error {
	if details == nil {
		details = map[string]any{}
	}
	return &Error{Code: code, Message: message, Details: details, Cause: cause}
}
