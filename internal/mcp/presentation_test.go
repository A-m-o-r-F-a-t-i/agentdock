package mcp

import (
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	protocol "github.com/uvwt/agentdock-protocol"
	"github.com/uvwt/agentdock/internal/config"
)

func TestTextOnlyHostSimulationNeverFetchesAdvertisedTemplates(t *testing.T) {
	h := newMCPAppTestHarnessWithApps(t, config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()}, false)
	before := h.server.templateReads.Load()
	tools, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Meta["openai/outputTemplate"] != nil || asMap(tool.Meta["ui"])["resourceUri"] != nil {
			t.Fatalf("TextOnly advertises template: %s", tool.Name)
		}
	}
	for _, name := range []string{"agentdock_context", "list_dir", "session_observe"} {
		result, err := h.session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Meta["openai/outputTemplate"] != nil || asMap(result.Meta["ui"])["resourceUri"] != nil {
			t.Fatalf("TextOnly result advertises template: %s", name)
		}
		if result.StructuredContent == nil || len(result.Content) == 0 {
			t.Fatalf("business output missing: %s", name)
		}
	}
	if got := h.server.templateReads.Load() - before; got != 0 {
		t.Fatalf("ordinary TextOnly host performed %d template reads", got)
	}
	resources, err := h.session.ListResources(t.Context(), nil)
	if err != nil || len(resources.Resources) != 0 {
		t.Fatalf("TextOnly resource directory: %#v %v", resources, err)
	}
}

func TestCachedTemplateGraceIsExactInertAndExpires(t *testing.T) {
	h := newMCPAppTestHarness(t, config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()})
	oldHash := h.server.ToolContractHash()
	old := h.server.presentation.Load()
	disabled := false
	if _, err := h.runtime.RuntimeUpdateDisplaySettings(t.Context(), config.DisplayChange{ExpectedRevision: 1, ChatGPTMCPUIEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	current := h.server.presentation.Load()
	if current.Mode != "TextOnly" || current.Revision <= old.Revision || oldHash == h.server.ToolContractHash() {
		t.Fatal("presentation generation did not invalidate")
	}
	for uri := range old.Templates {
		result, err := h.session.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: uri})
		if err != nil || len(result.Contents) != 1 || result.Contents[0].Text != disabledTemplateHTML {
			t.Fatalf("cached template failed: %s %v", uri, err)
		}
		text := strings.ToLower(result.Contents[0].Text)
		for _, forbidden := range []string{"<script", "http://", "https://", "onload=", "<iframe", "<link", "<img"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("inert template includes %s", forbidden)
			}
		}
	}
	if _, err := h.session.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: "ui://unknown/arbitrary.html"}); err == nil {
		t.Fatal("unknown URI became readable")
	}
	expired := *current
	expired.LegacyUntil = maps.Clone(current.LegacyUntil)
	expired.LegacyUntil[protocol.ContextUIResourceURI] = time.Now().Add(-time.Second)
	h.server.presentation.Store(&expired)
	if _, err := h.session.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: protocol.ContextUIResourceURI}); err == nil {
		t.Fatal("expired compatibility template still served")
	}
	enabled := true
	if _, err := h.runtime.RuntimeUpdateDisplaySettings(t.Context(), config.DisplayChange{ExpectedRevision: 2, ChatGPTMCPUIEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	for uri, definition := range h.server.presentation.Load().Templates {
		result, err := h.session.ReadResource(t.Context(), &mcpsdk.ReadResourceParams{URI: uri})
		if err != nil || result.Contents[0].Text != definition.HTML || result.Contents[0].MIMEType != protocol.MCPAppMIMEType {
			t.Fatalf("advertised template not ready: %s %v", uri, err)
		}
	}
}

func TestTextOnlyForwardedMetadataPreservesBusinessAndRestrictions(t *testing.T) {
	meta := map[string]any{"openai/outputTemplate": "ui://remote/widget", "ui/resourceUri": "ui://remote/widget", "openai/widgetAccessible": true, "openai/widgetCSP": map[string]any{}, "mcp/www_authenticate": []any{"Bearer"}, "file_arg_rewrite_paths": []any{"path"}, "ui": map[string]any{"resourceUri": "ui://remote/widget", "visibility": []any{"app"}, "csp": map[string]any{}}}
	business := map[string]any{"_meta": meta, "agentdock_guidance": map[string]any{"response_additions": []any{map[string]any{"text": "untrusted"}}}, "result": map[string]any{"content": []any{}, "_meta": meta}}
	remote := map[string]any{"_meta": meta, "content": []any{map[string]any{"type": "text", "text": "business", "_meta": meta}}, "structuredContent": business, "isError": true}
	original := map[string]any{"_meta": meta, "content": remote["content"], "isError": true, "structuredContent": map[string]any{"server": "remote", "result": remote}}
	before := normalizedEnvelope(t, original)
	filtered := normalizedEnvelope(t, filterTextOnlyEnvelope(original, true))
	if !reflect.DeepEqual(before, normalizedEnvelope(t, original)) {
		t.Fatal("filter mutated shared data")
	}
	for _, object := range []map[string]any{filtered, asMap(asMap(filtered["structuredContent"])["result"]), asMap(filtered["content"].([]any)[0])} {
		kept := asMap(object["_meta"])
		if kept["openai/outputTemplate"] != nil || kept["ui/resourceUri"] != nil || kept["openai/widgetAccessible"] != nil || asMap(kept["ui"])["resourceUri"] != nil {
			t.Fatalf("template metadata survived: %#v", kept)
		}
		if kept["mcp/www_authenticate"] == nil || kept["file_arg_rewrite_paths"] == nil || asMap(kept["ui"])["visibility"] == nil {
			t.Fatal("non-UI safety metadata was removed")
		}
	}
	nested := asMap(asMap(filtered["structuredContent"])["result"])
	if !reflect.DeepEqual(nested["structuredContent"], normalizedEnvelope(t, business)) || filtered["isError"] != true {
		t.Fatal("business fields were rewritten")
	}
	data, _ := json.Marshal(filtered)
	var sdkResult mcpsdk.CallToolResult
	if err := json.Unmarshal(data, &sdkResult); err != nil {
		t.Fatal(err)
	}
}
