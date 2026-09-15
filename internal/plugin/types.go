package plugin

import mcpclient "github.com/uvwt/agentdock/internal/mcp/client"

const (
	ManifestDirectory = ".agentdock-plugin"
	ManifestFilename  = "plugin.json"
	StateFilename     = "state.json"
)

// Manifest is the portable, self-contained heavy-plugin descriptor. The
// plugin directory is the installation unit: document Skills live under
// skills/<name>, MCP implementations and assets may live anywhere else in the
// same package, and MCP server definitions stay in this manifest.
type Manifest struct {
	SchemaVersion int                               `json:"schema_version"`
	Name          string                            `json:"name"`
	Description   string                            `json:"description"`
	Version       string                            `json:"version"`
	MCPServers    map[string]mcpclient.ServerConfig `json:"mcpServers,omitempty"`
}

// State is installation-local and is never accepted from an untrusted plugin
// package. Missing member entries default to enabled so adding a new member in
// a plugin update does not silently hide it.
type State struct {
	Enabled    bool            `json:"enabled"`
	Skills     map[string]bool `json:"skills,omitempty"`
	MCPServers map[string]bool `json:"mcpServers,omitempty"`
}

// Definition is the management/API view derived from one installed plugin
// directory. Skills and MCP servers are discovered from package contents, not
// from a central logical grouping registry.
type Definition struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Path        string   `json:"path"`
	Enabled     bool     `json:"enabled"`
	Skills      []string `json:"skills,omitempty"`
	MCPServers  []string `json:"mcp_servers,omitempty"`
}

type Membership struct {
	Plugin  string `json:"plugin"`
	Enabled bool   `json:"enabled"`
}

type SkillMember struct {
	Name    string `json:"name"`
	Plugin  string `json:"plugin"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
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
