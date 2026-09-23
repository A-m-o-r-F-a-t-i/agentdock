package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	protocol "github.com/uvwt/agentdock-protocol"
)

const legacyTemplateLifetime = 30 * time.Minute
const disabledTemplateHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'none'; connect-src 'none'; img-src 'none'; style-src 'none'; form-action 'none'; base-uri 'none'"><title>AgentDock</title></head><body><p>AgentDock 已关闭内嵌界面。本次结果请查看工具文本。</p></body></html>`

type presentationState struct {
	Mode           string
	Revision       uint64
	Templates      map[string]appResourceDefinition
	TemplateHashes map[string]string
	LegacyUntil    map[string]time.Time
}

// Exact previously shipped built-in identifiers are the only upgrade grace
// allow-list. No arbitrary ui:// URI, remote URI or filename becomes readable.
func legacyTemplateURIs() []string {
	return []string{protocol.ContextUIResourceURI, protocol.TaskProgressUIResourceURI, protocol.FileChangeUIResourceURI, protocol.DynamicMCPUIResourceURI, protocol.ArtifactUIResourceURI, protocol.RecallUIResourceURI, protocol.WorkflowUIResourceURI, protocol.ACPStatusUIResourceURI}
}

func (s *Server) refreshPresentationState() {
	s.presentationMu.Lock()
	defer s.presentationMu.Unlock()
	if s.sdk == nil || s.runtime == nil {
		return
	}
	settings := s.runtime.MCPPresentationSettings()
	previous := s.presentation.Load()
	state := &presentationState{Mode: "TextOnly", Revision: settings.Revision, Templates: map[string]appResourceDefinition{}, TemplateHashes: map[string]string{}, LegacyUntil: map[string]time.Time{}}
	now := time.Now()
	if previous == nil {
		// Upgrade grace covers the exact built-ins advertised by earlier releases.
		// They are unlisted, inert and time-bounded even when starting TextOnly.
		for _, uri := range legacyTemplateURIs() {
			state.LegacyUntil[uri] = now.Add(legacyTemplateLifetime)
		}
	} else {
		for uri, until := range previous.LegacyUntil {
			if now.Before(until) {
				state.LegacyUntil[uri] = until
			}
		}
		for uri := range previous.Templates {
			state.LegacyUntil[uri] = now.Add(legacyTemplateLifetime)
		}
	}
	definitions := s.configuredAppResourceDefinitions()
	ready := true
	for _, definition := range definitions {
		_, known := protocol.UIResourceContract(definition.URI)
		if !known || strings.TrimSpace(definition.HTML) == "" || state.Templates[definition.URI].URI != "" {
			ready = false
			break
		}
		state.Templates[definition.URI] = definition
		state.TemplateHashes[definition.URI] = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(definition.HTML)))
	}
	for _, definition := range s.runtime.ToolDefinitions() {
		if definition.UIBinding != nil && state.Templates[definition.UIBinding.ResourceURI].URI == "" {
			ready = false
		}
	}
	if settings.ChatGPTMCPUIEnabled && ready {
		// Register readable templates before descriptors can advertise them.
		s.registerAppResourceDefinitions(definitions)
		state.Mode = "App"
	} else {
		state.Templates = map[string]appResourceDefinition{}
		state.TemplateHashes = map[string]string{}
	}
	// This single immutable generation governs reads, outgoing results and all
	// directories; stale SDK descriptors are filtered by presentationMiddleware.
	s.presentation.Store(state)
	if state.Mode == "TextOnly" {
		if len(s.registeredUIResources) > 0 {
			s.sdk.RemoveResources(s.registeredUIResources...)
		}
		s.registeredUIResources = nil
	} else {
		s.registeredUIResources = nil
		for _, definition := range definitions {
			s.registeredUIResources = append(s.registeredUIResources, definition.URI)
		}
	}
	// SDK AddTool/RemoveResources coalesce list_changed notifications. Stateless
	// HTTP requests and direct Bridge calls always read this current generation.
	for _, definition := range s.runtime.ToolDefinitions() {
		s.registerTool(definition)
	}
	slog.Info("MCP presentation updated", "mode", state.Mode, "revision", state.Revision, "template_references", len(state.Templates), "templates_ready", ready)
}

func (s *Server) legacyTemplate(uri string) (*mcpsdk.ReadResourceResult, bool) {
	state := s.presentation.Load()
	if state == nil || state.Mode != "TextOnly" {
		return nil, false
	}
	until, known := state.LegacyUntil[uri]
	if !known || !time.Now().Before(until) {
		return nil, false
	}
	slog.Debug("MCP cached template compatibility read", "mode", state.Mode, "revision", state.Revision, "mime_type", protocol.MCPAppMIMEType)
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: uri, MIMEType: protocol.MCPAppMIMEType, Text: disabledTemplateHTML}}}, true
}

func (s *Server) presentationMiddleware(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, request mcpsdk.Request) (mcpsdk.Result, error) {
		if method == "resources/read" && request != nil {
			s.templateReads.Add(1)
			if params, ok := request.GetParams().(*mcpsdk.ReadResourceParams); ok && params != nil {
				if resource, ok := s.legacyTemplate(params.URI); ok {
					return resource, nil
				}
			}
		}
		result, err := next(ctx, method, request)
		if err != nil || s.uiEnabled() {
			return result, err
		}
		switch value := result.(type) {
		case *mcpsdk.ReadResourceResult:
			if request != nil {
				if params, ok := request.GetParams().(*mcpsdk.ReadResourceParams); ok && params != nil {
					if resource, ok := s.legacyTemplate(params.URI); ok {
						return resource, nil
					}
					return nil, mcpsdk.ResourceNotFoundError(params.URI)
				}
			}
		case *mcpsdk.ListToolsResult:
			copy := *value
			copy.Tools = make([]*mcpsdk.Tool, 0, len(value.Tools))
			for _, tool := range value.Tools {
				clone := *tool
				clone.Meta = withoutOwnedUIMount(tool.Meta)
				copy.Tools = append(copy.Tools, &clone)
			}
			return &copy, nil
		case *mcpsdk.ListResourcesResult:
			copy := *value
			copy.Resources = []*mcpsdk.Resource{}
			copy.NextCursor = ""
			return &copy, nil
		case *mcpsdk.CallToolResult:
			// A display switch may race a long in-flight call. Apply the same final
			// boundary to the completed response, not its start-time mode.
			data, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return result, nil
			} // preserve a successful business result
			var envelope map[string]any
			if json.Unmarshal(data, &envelope) != nil {
				return result, nil
			}
			forwarding := false
			if request != nil {
				if params, ok := request.GetParams().(*mcpsdk.CallToolParamsRaw); ok && params != nil {
					forwarding = params.Name == "mcp_tool_call"
				}
			}
			envelope = filterTextOnlyEnvelope(envelope, forwarding)
			filtered, _ := json.Marshal(envelope)
			var copy mcpsdk.CallToolResult
			if json.Unmarshal(filtered, &copy) != nil {
				return result, nil
			}
			return &copy, nil
		}
		return result, nil
	}
}

// Filter only known protocol envelope locations, never arbitrary structured
// business fields. Auth/file/visibility restrictions remain effective.
func filterTextOnlyEnvelope(original map[string]any, forwarded ...bool) map[string]any {
	result := maps.Clone(original)
	filterMeta := func(object map[string]any) map[string]any {
		copy := maps.Clone(object)
		if value, ok := copy["_meta"]; ok {
			meta := withoutOwnedUIMount(mcpsdk.Meta(asMap(value)))
			if len(meta) > 0 {
				copy["_meta"] = map[string]any(meta)
			} else {
				delete(copy, "_meta")
			}
		}
		return copy
	}
	result = filterMeta(result)
	if values, ok := result["content"].([]any); ok {
		content := make([]any, 0, len(values))
		for _, value := range values {
			if object, ok := value.(map[string]any); ok {
				content = append(content, filterMeta(object))
			} else {
				content = append(content, value)
			}
		}
		result["content"] = content
	}
	if len(forwarded) > 0 && forwarded[0] {
		if structured, ok := result["structuredContent"].(map[string]any); ok {
			copy := maps.Clone(structured)
			// The forwarding boundary, not a matching key inside arbitrary business
			// data, identifies the nested MCP protocol result. Descend exactly once.
			if nested, ok := structured["result"].(map[string]any); ok {
				copy["result"] = filterTextOnlyEnvelope(nested)
			}
			result["structuredContent"] = copy
		}
	}
	return result
}
