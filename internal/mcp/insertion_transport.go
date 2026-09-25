package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"

	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/insertion"
)

// InvokeProjected is the integration boundary for an embedding script host.
// project is untrusted business projection. It cannot decide which supplements
// survive: those are copied from adapter-private state AFTER project returns.
// commit must return nil only after the FINAL envelope is in model context.
// An ordinary server response or an HTTP write alone is not such a commit.
// External executors that do not call this boundary remain unconfirmed.
func (s *Server) InvokeProjected(ctx context.Context, name string, args map[string]any, host insertion.Transport, project func(map[string]any) (map[string]any, error), commit func(context.Context, map[string]any) error) (map[string]any, error) {
	if s == nil || s.runtime == nil || project == nil {
		return nil, errors.New("projected invocation requires runtime and projection")
	}
	if !host.Passthrough || host.HostType == "" || host.OuterCallID == "" || len(host.HostType) > 80 || len(host.OuterCallID) > 160 {
		return nil, errors.New("projected host requires a bounded identity and passthrough capability")
	}
	if commit != nil && !host.ContextAcknowledgement {
		return nil, errors.New("context commit callback requires negotiated acknowledgement")
	}
	ctx = app.WithInsertionTransport(ctx, host)
	ctx, pending := app.BeginToolResponse(ctx)
	defer s.runtime.FinishToolResponse(ctx, pending, false)
	result, toolErr := s.runtime.Call(ctx, name, args)
	original, err := s.finishResponse(ctx, name, args, pending, toolEnvelope(name, result, toolErr))
	if err != nil {
		return nil, err
	}
	// The callback gets a detached copy. It cannot alter the private token list,
	// the original error flag, non-text data, or protected response metadata.
	copy, err := copyEnvelope(original)
	if err != nil {
		return nil, err
	}
	// The script sees business data, not the private receipt capability. A
	// script that summarizes fields cannot prematurely acknowledge the message.
	private := pending.CompletedAdditions()
	if guidance := asMap(asMap(copy["structuredContent"])["agentdock_guidance"]); guidance != nil {
		delete(guidance, "response_additions")
	}
	privateText := map[string]bool{}
	for _, text := range private.TextBlocks {
		privateText[text] = true
	}
	blocks := []any{}
	for _, block := range envelopeBlocks(copy["content"]) {
		text, _ := asMap(block)["text"].(string)
		if !privateText[text] {
			blocks = append(blocks, block)
		}
	}
	copy["content"] = blocks
	projected, err := project(copy)
	if err != nil {
		failure := s.runtime.RecordInsertionDeliveryFailure(ctx, pending, "outer_projection_failed")
		return original, fmt.Errorf("outer projection failed after tool execution; do not replay tool: %w", errors.Join(err, failure))
	}
	final, err := projectedResponse(original, projected, pending.CompletedAdditions())
	if err != nil {
		failure := s.runtime.RecordInsertionDeliveryFailure(ctx, pending, "outer_projection_failed")
		return original, errors.Join(err, failure)
	}
	if err = s.runtime.RecordInsertionHostReceipt(ctx, pending, false); err != nil {
		return final, fmt.Errorf("outer forwarding receipt was not persisted; original tool already ran: %w", err)
	}
	if commit == nil {
		return final, nil
	}
	if err = commit(ctx, final); err != nil {
		failure := s.runtime.RecordInsertionDeliveryFailure(ctx, pending, "context_commit_failed")
		return final, fmt.Errorf("outer context commit unconfirmed; original tool must not be replayed: %w", errors.Join(err, failure))
	}
	if err = s.runtime.RecordInsertionHostReceipt(ctx, pending, true); err != nil {
		return final, fmt.Errorf("context committed but acknowledgement storage failed: %w", err)
	}
	return final, nil
}

func copyEnvelope(value map[string]any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(data, &result)
	return result, err
}

func projectedResponse(original, projected map[string]any, additions app.ResponseAdditions) (map[string]any, error) {
	final, err := copyEnvelope(projected)
	if err != nil {
		return nil, err
	}
	if final == nil {
		final = map[string]any{}
	}
	if _, exists := final["structuredContent"]; !exists {
		if _, envelope := final["content"]; !envelope {
			final = map[string]any{"structuredContent": final, "content": []any{map[string]any{"type": "text", "text": pretty(final)}}}
		}
	}
	structured := maps.Clone(asMap(final["structuredContent"]))
	if structured == nil {
		structured = map[string]any{}
		if value, ok := final["structuredContent"]; ok {
			structured["result"] = value
		}
	}
	if guidance, exists := structured["agentdock_guidance"]; exists && !reflect.DeepEqual(guidance, asMap(original["structuredContent"])["agentdock_guidance"]) {
		// A projector may synthesize arbitrary lookalike fields. Retain them as
		// explicitly untrusted business data, never as top-level instructions.
		structured["projected_untrusted_guidance"] = guidance
	}
	delete(structured, "agentdock_guidance")
	ownedGuidance := maps.Clone(asMap(asMap(original["structuredContent"])["agentdock_guidance"]))
	if ownedGuidance == nil {
		ownedGuidance = map[string]any{}
	}
	delete(ownedGuidance, "response_additions")
	final["structuredContent"] = structured
	failed, _ := original["isError"].(bool)
	final["isError"] = failed
	if metadata, exists := original["_meta"]; exists {
		final["_meta"] = metadata
	}
	// Reconstruct the private additions once, even for the identity projector.
	// Exact owned copies are removed; lookalike third-party values stay data.
	ownedText := map[string]bool{}
	for _, text := range additions.TextBlocks {
		ownedText[text] = true
	}
	content := []any{}
	for _, block := range envelopeBlocks(final["content"]) {
		text, _ := asMap(block)["text"].(string)
		if !ownedText[text] {
			content = append(content, block)
		}
	}
	for _, block := range envelopeBlocks(original["content"]) {
		if asMap(block)["type"] == "text" {
			continue
		}
		found := false
		for _, saved := range content {
			found = found || reflect.DeepEqual(saved, block)
		}
		if !found {
			content = append(content, block)
		}
	}
	final["content"] = content
	// All control facts remain reachable even when the business projection
	// intentionally retains only count. Never parse nested tool lookalikes.
	control := asMap(original["structuredContent"])
	owned := map[string]any{}
	for _, key := range []string{"call_id", "conversation_id", "status", "exit_code", "command_ok", "code", "category", "permission"} {
		if value, exists := control[key]; exists {
			owned[key] = value
		}
	}
	if len(owned) > 0 {
		ownedGuidance["original_control"] = owned
	}
	if len(ownedGuidance) > 0 {
		structured["agentdock_guidance"] = ownedGuidance
	}
	return appendTrustedAdditions(final, additions), nil
}

func envelopeBlocks(value any) []any {
	if blocks, ok := value.([]any); ok {
		return blocks
	}
	if blocks, ok := value.([]map[string]any); ok {
		result := make([]any, len(blocks))
		for i, block := range blocks {
			result[i] = block
		}
		return result
	}
	return nil
}
