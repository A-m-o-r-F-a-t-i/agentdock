package mcp

import (
	"reflect"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/config"
)

func TestDisplayToggleKeepsSameConnectionAndBusinessTools(t *testing.T) {
	h := newMCPAppTestHarness(t, config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()})
	before, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]*mcpsdk.Tool{}
	for _, tool := range before.Tools {
		metadata[tool.Name] = tool
	}
	enabled := false
	if _, err = h.runtime.RuntimeUpdateDisplaySettings(t.Context(), config.DisplayChange{ExpectedRevision: 1, ChatGPTMCPUIEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	after, err := h.session.ListTools(t.Context(), nil)
	if err != nil || len(after.Tools) != len(before.Tools) {
		t.Fatalf("directory lost business tools: %v", err)
	}
	for _, tool := range after.Tools {
		previous := metadata[tool.Name]
		if previous == nil || !reflect.DeepEqual(previous.Annotations, tool.Annotations) || !reflect.DeepEqual(previous.InputSchema, tool.InputSchema) {
			t.Fatalf("business contract changed: %s", tool.Name)
		}
		if _, mounted := tool.Meta["openai/outputTemplate"]; mounted {
			t.Fatalf("legacy mount retained: %s", tool.Name)
		}
		if ui, ok := tool.Meta["ui"].(map[string]any); ok && ui["resourceUri"] != nil {
			t.Fatalf("UI mount retained: %s", tool.Name)
		}
		for key, value := range previous.Meta {
			if key != "ui" && key != "openai/outputTemplate" && !reflect.DeepEqual(value, tool.Meta[key]) {
				t.Fatalf("non-UI metadata removed: %s %s", tool.Name, key)
			}
		}
	}
	resources, err := h.session.ListResources(t.Context(), nil)
	if err != nil || len(resources.Resources) != 0 {
		t.Fatalf("disabled UI resource directory still exposed: %#v %v", resources, err)
	}
	result, err := h.session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "list_dir", Arguments: map[string]any{"path": h.runtime.Config().AgentDockDefaultDir}})
	if err != nil || result.IsError || len(result.Content) == 0 || result.StructuredContent == nil {
		t.Fatalf("ordinary data tool stopped working: %#v %v", result, err)
	}
	enabled = true
	if _, err = h.runtime.RuntimeUpdateDisplaySettings(t.Context(), config.DisplayChange{ExpectedRevision: 2, ChatGPTMCPUIEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	restored, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range restored.Tools {
		if ui, ok := tool.Meta["ui"].(map[string]any); ok && ui["resourceUri"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("re-enabling UI did not update existing SDK session")
	}
}

func TestDisplayPolicyPreservesAuthenticationAndUIVisibility(t *testing.T) {
	original := mcpsdk.Meta{
		"openai/outputTemplate": "ui://owned/template.html",
		"mcp/www_authenticate":  []string{"Bearer realm=agentdock"},
		"openai/fileParams":     map[string]any{"path": true},
		"ui":                    map[string]any{"resourceUri": "ui://owned/template.html", "visibility": []string{"app"}},
	}
	filtered := withoutOwnedUIMount(original)
	if filtered["openai/outputTemplate"] != nil || filtered["ui"].(map[string]any)["resourceUri"] != nil {
		t.Fatal("UI mounts were not removed")
	}
	for _, key := range []string{"mcp/www_authenticate", "openai/fileParams"} {
		if !reflect.DeepEqual(filtered[key], original[key]) {
			t.Fatalf("non-UI metadata removed: %s", key)
		}
	}
	if !reflect.DeepEqual(filtered["ui"].(map[string]any)["visibility"], []string{"app"}) {
		t.Fatal("app-only visibility was widened")
	}
	if original["ui"].(map[string]any)["resourceUri"] == nil {
		t.Fatal("policy mutated a shared metadata map")
	}
}
