package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/app"
)

// finishResponse is the one adapter boundary for SDK and direct Bridge calls.
// Validate business content before committing the local queue reservation. The
// final append contains only locally constructed, JSON-safe strings and numbers.
func (s *Server) finishResponse(ctx context.Context, name string, arguments map[string]any, pending *app.ToolResponse, original map[string]any) (out map[string]any, returnErr error) {
	defer func() {
		if returnErr != nil {
			s.runtime.RecordToolResponse(pending, map[string]any{"isError": true, "error": returnErr.Error(), "output_state": "not_stored"})
		}
	}()
	encoded, err := json.Marshal(original)
	if err != nil {
		return nil, fmt.Errorf("encode MCP tool result: %w", err)
	}
	var check mcpsdk.CallToolResult
	if err := json.Unmarshal(encoded, &check); err != nil {
		return nil, fmt.Errorf("decode MCP tool result: %w", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return nil, err
	}
	meta := maps.Clone(asMap(envelope["_meta"]))
	if def, ok := s.runtime.ToolDefinition(name); ok {
		if meta == nil {
			meta = map[string]any{}
		}
		for key, value := range toolResultMetadata(def, arguments, s.uiEnabled()) {
			meta[key] = value
		}
	}
	if !s.uiEnabled() {
		meta = map[string]any(withoutOwnedUIMount(mcpsdk.Meta(meta)))
	}
	if len(meta) > 0 {
		envelope["_meta"] = meta
	} else {
		delete(envelope, "_meta")
	}
	_ = s.runtime.FinishToolResponse(ctx, pending, true)
	envelope = appendTrustedAdditions(envelope, pending.CompletedAdditions())
	if !s.uiEnabled() {
		envelope = filterTextOnlyEnvelope(envelope, name == "mcp_tool_call")
	}
	s.runtime.RecordToolResponse(pending, envelope)
	return envelope, nil
}

// Only callers holding adapter-owned ToolResponse state supply additions. Never
// scan third-party content or its nested result for guidance or marker strings.
func appendTrustedAdditions(original map[string]any, additions app.ResponseAdditions) map[string]any {
	if len(additions.TextBlocks) == 0 && len(additions.UserMessages) == 0 {
		return original
	}
	envelope := maps.Clone(original)
	structured := maps.Clone(asMap(original["structuredContent"]))
	if len(structured) == 0 {
		structured = map[string]any{}
		if original["structuredContent"] != nil {
			structured["result"] = original["structuredContent"]
		}
	}
	guidance := maps.Clone(asMap(structured["agentdock_guidance"]))
	if guidance == nil {
		guidance = map[string]any{}
	}
	// An unexpected non-object reserved field stays in business data rather than
	// being discarded. Normal Runtime results already use the owned object form.
	if previous, exists := structured["agentdock_guidance"]; exists && len(asMap(previous)) == 0 && previous != nil {
		if _, object := previous.(map[string]any); !object {
			structured = map[string]any{"result": original["structuredContent"]}
		}
	}
	seen := map[string]bool{}
	existing := []any{}
	switch values := guidance["response_additions"].(type) {
	case []any:
		existing = append(existing, values...)
	case []app.UserResponseAddition:
		for _, value := range values {
			existing = append(existing, value)
		}
	}
	for _, value := range existing {
		switch item := value.(type) {
		case app.UserResponseAddition:
			seen[item.InsertionID] = true
		case map[string]any:
			id, _ := item["insertion_id"].(string)
			if id != "" {
				seen[id] = true
			}
		}
	}
	newMessages := 0
	for _, item := range additions.UserMessages {
		if item.InsertionID == "" || seen[item.InsertionID] {
			continue
		}
		seen[item.InsertionID] = true
		existing = append(existing, item)
		newMessages++
	}
	if len(additions.UserMessages) > 0 {
		guidance["response_additions"] = existing
		if _, ok := guidance["source"]; !ok {
			guidance["source"] = "agentdock"
		}
		structured["agentdock_guidance"] = guidance
		envelope["structuredContent"] = structured
	}
	// Reconstructing an already augmented response returns the same additions,
	// not another queue claim or a second trailing compatibility copy.
	if len(additions.UserMessages) > 0 && newMessages == 0 {
		return envelope
	}
	return appendResponseBlocks(envelope, additions.TextBlocks)
}
