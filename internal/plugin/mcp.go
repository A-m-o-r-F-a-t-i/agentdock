package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"

	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

var nativeServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var httpFieldNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// Portable server identifiers are not restricted to native registry tokens.
// An opaque identifier is mapped deterministically without changing mcp.json.
func nativeServerName(pluginName, name string) string {
	if nativeServerNamePattern.MatchString(name) {
		return name
	}
	digest := sha256.Sum256([]byte(pluginName + "\x00" + name))
	return "plugin-" + hex.EncodeToString(digest[:16])
}

func readMCP(root, pluginName string) (map[string]mcpclient.ServerConfig, []string) {
	configs := map[string]mcpclient.ServerConfig{}
	path := filepath.Join(root, MCPFilename)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return configs, nil
	}
	path, err := containedPath(root, path, false)
	if err != nil {
		return configs, []string{"mcp.json: rejected filesystem path"}
	}
	object, err := readJSONObject(path)
	if err != nil {
		return configs, []string{"mcp.json: invalid configuration: " + err.Error()}
	}
	servers, ok := object["mcpServers"].(map[string]any)
	if !ok || object["$schema"] != MCPSchema || len(object) != 2 {
		return configs, []string{"mcp.json: expected the supported $schema and an mcpServers object only"}
	}
	diagnostics := []string{}
	for _, rawName := range sortedKeys(servers) {
		config, err := parseMCPServer(root, pluginName, rawName, servers[rawName])
		if err != nil {
			diagnostics = append(diagnostics, "MCP "+rawName+": "+err.Error())
			continue
		}
		if _, exists := configs[config.Name]; exists {
			diagnostics = append(diagnostics, "MCP "+rawName+": native identifier collision")
			continue
		}
		configs[config.Name] = config
	}
	return configs, diagnostics
}

func parseMCPServer(root, pluginName, rawName string, value any) (mcpclient.ServerConfig, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return mcpclient.ServerConfig{}, errors.New("expected an object")
	}
	kind, ok := object["type"].(string)
	if !ok {
		return mcpclient.ServerConfig{}, errors.New("type is required")
	}
	allowed := map[string]bool{"type": true}
	switch kind {
	case "stdio":
		for _, key := range []string{"command", "args", "env", "cwd"} {
			allowed[key] = true
		}
	case "streamable-http", "sse":
		for _, key := range []string{"url", "headers"} {
			allowed[key] = true
		}
	default:
		return mcpclient.ServerConfig{}, errors.New("unsupported transport type")
	}
	for key := range object {
		if !allowed[key] {
			return mcpclient.ServerConfig{}, fmt.Errorf("unknown field %s", key)
		}
	}
	for _, key := range []string{"command", "cwd", "url"} {
		if v, exists := object[key]; exists {
			if _, ok := v.(string); !ok {
				return mcpclient.ServerConfig{}, fmt.Errorf("%s must be a string", key)
			}
		}
	}
	if args, exists := object["args"]; exists {
		items, ok := args.([]any)
		if !ok {
			return mcpclient.ServerConfig{}, errors.New("args must be an array")
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return mcpclient.ServerConfig{}, errors.New("args entries must be strings")
			}
		}
	}
	for _, key := range []string{"env", "headers"} {
		if v, exists := object[key]; exists {
			fields, ok := v.(map[string]any)
			if !ok {
				return mcpclient.ServerConfig{}, fmt.Errorf("%s must be an object", key)
			}
			for _, entry := range fields {
				if _, ok := entry.(string); !ok {
					return mcpclient.ServerConfig{}, fmt.Errorf("%s values must be strings", key)
				}
			}
		}
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return mcpclient.ServerConfig{}, err
	}
	var server MCPServer
	if err := json.Unmarshal(raw, &server); err != nil {
		return mcpclient.ServerConfig{}, err
	}
	resolvedRoot, err := resolvedPath(root, false)
	if err != nil {
		return mcpclient.ServerConfig{}, err
	}
	dataRoot, err := resolvedPath(filepath.Join(filepath.Dir(root), ".data", pluginName), true)
	if err != nil {
		return mcpclient.ServerConfig{}, err
	}
	name := nativeServerName(pluginName, rawName)
	config := mcpclient.ServerConfig{
		SourceType: "plugin", PluginName: pluginName, DisplayName: rawName, StorageKey: nativeServerName(pluginName, rawName), Name: name, Description: rawName, Enabled: true, TimeoutMS: 30000, PluginRoot: resolvedRoot, PluginData: dataRoot}
	if config.Description == "" {
		config.Description = pluginName + " MCP"
	}
	if kind == "stdio" {
		config.Transport = mcpclient.TransportStdio
		command := server.Command
		if command == "" || strings.ContainsRune(command, 0) {
			return config, errors.New("command is required")
		}
		if strings.HasPrefix(command, "./") {
			config.Command, err = containedPath(root, filepath.Join(root, filepath.FromSlash(command)), true)
			if err != nil {
				return config, errors.New("command escapes the plugin root")
			}
		} else {
			if strings.ContainsAny(command, "/\\:") || strings.IndexFunc(command, unicode.IsSpace) >= 0 || command == "." || command == ".." {
				return config, errors.New("command must be a bare executable token or ./ path")
			}
			config.Command = command
		}
		expand := strings.NewReplacer("${PLUGIN_ROOT}", resolvedRoot, "${PLUGIN_DATA}", dataRoot)
		for _, arg := range server.Args {
			if strings.ContainsRune(arg, 0) {
				return config, errors.New("argument contains NUL")
			}
			config.Args = append(config.Args, expand.Replace(arg))
		}
		config.PackageEnv = map[string]string{}
		seen := map[string]bool{}
		for _, key := range sortedKeys(server.Env) {
			value := server.Env[key]
			canonical := key
			if runtime.GOOS == "windows" {
				canonical = strings.ToUpper(key)
			}
			if canonical == "PLUGIN_ROOT" || canonical == "PLUGIN_DATA" {
				return config, errors.New("env may not define reserved PLUGIN_ROOT or PLUGIN_DATA")
			}
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) || seen[canonical] {
				return config, errors.New("invalid or duplicate environment name/value")
			}
			seen[canonical] = true
			config.PackageEnv[key] = expand.Replace(value)
		}
		cwd := server.Cwd
		if _, explicit := object["cwd"]; !explicit {
			cwd = "${PLUGIN_ROOT}"
		}
		base := resolvedRoot
		switch {
		case strings.HasPrefix(cwd, "./"):
			cwd = filepath.Join(resolvedRoot, filepath.FromSlash(expand.Replace(cwd)))
		case cwd == "${PLUGIN_ROOT}" || strings.HasPrefix(cwd, "${PLUGIN_ROOT}/"):
			cwd = expand.Replace(cwd)
		case cwd == "${PLUGIN_DATA}" || strings.HasPrefix(cwd, "${PLUGIN_DATA}/"):
			base = dataRoot
			cwd = expand.Replace(cwd)
		default:
			return config, errors.New("cwd must begin with ./, ${PLUGIN_ROOT}, or ${PLUGIN_DATA}")
		}
		config.Cwd, err = containedPath(base, filepath.FromSlash(cwd), true)
		if err != nil {
			return config, errors.New("cwd escapes its permitted root")
		}
	} else {
		endpoint, err := url.Parse(server.URL)
		if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
			return config, errors.New("URL must be an absolute HTTP(S) endpoint without user info or fragment")
		}
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme == "http" && endpoint.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return config, errors.New("non-loopback endpoints require HTTPS")
		}
		seen := map[string]bool{}
		for key, value := range server.Headers {
			canonical := strings.ToLower(key)
			if !httpFieldNamePattern.MatchString(key) || seen[canonical] || !validHeaderValue(value) {
				return config, errors.New("invalid or case-duplicate HTTP header")
			}
			seen[canonical] = true
		}
		if kind == "sse" {
			return config, errors.New("legacy SSE transport is not supported by this host")
		}
		config.Transport = mcpclient.TransportStreamableHTTP
		config.URL = server.URL
		config.PackageHeaders = server.Headers
	}
	return config, nil
}

func validHeaderValue(value string) bool {
	for _, b := range []byte(value) {
		if b == 127 || (b < 32 && b != '\t') {
			return false
		}
	}
	return true
}

// applySourceEnvironmentBindings is used only after an explicitly reviewed
// source adapter installation. Legacy raw packages retain opaque text values.
func applySourceEnvironmentBindings(cfg mcpclient.ServerConfig) mcpclient.ServerConfig {
	for key, value := range cfg.PackageEnv {
		if name, ok := exactEnvironmentReference(value); ok && name != "PLUGIN_ROOT" && name != "PLUGIN_DATA" {
			if cfg.EnvFromEnv == nil {
				cfg.EnvFromEnv = map[string]string{}
			}
			cfg.EnvFromEnv[key] = name
			delete(cfg.PackageEnv, key)
		}
	}
	for key, value := range cfg.PackageHeaders {
		if name, ok := exactEnvironmentReference(value); ok {
			if cfg.HeaderEnv == nil {
				cfg.HeaderEnv = map[string]string{}
			}
			cfg.HeaderEnv[key] = name
			delete(cfg.PackageHeaders, key)
		}
	}
	return cfg
}
