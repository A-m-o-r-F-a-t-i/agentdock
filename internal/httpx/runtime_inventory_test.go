package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/auth"
)

func TestRuntimeInventorySummaryAndSkillToggleHTTPBody(t *testing.T) {
	cfg := testConfig(t)
	runtime, err := newHTTPUnrestrictedRuntime(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	source := filepath.Join(cfg.AgentDockDefaultDir, "inventory-skill")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "SKILL.md"), "---\nname: inventory-skill\ndescription: Inventory test.\nversion: 1.0.0\n---\n# Inventory\n")
	writeTestFile(t, filepath.Join(source, "references", "guide.md"), "guide")
	if _, err := runtime.Call(context.Background(), "skill_package", map[string]any{"action": "install", "source": source}); err != nil {
		t.Fatal(err)
	}
	handler := runtimeAPIHandler(runtime, cfg, auth.NewOAuthStore())
	for _, action := range []string{"disable", "enable"} {
		request := httptest.NewRequest(http.MethodPost, "/internal/runtime/skills", strings.NewReader(`{"action":"`+action+`","skill":"inventory-skill"}`))
		response := httptest.NewRecorder()
		request.RemoteAddr = "127.0.0.1:12000"
		request.Host = "127.0.0.1"
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("HTTP skill %s lost its body: %d %s", action, response.Code, response.Body.String())
		}
		for _, summary := range []bool{true, false} {
			path := "/internal/runtime/skills"
			if summary {
				path += "?summary=true"
			}
			list := requestRuntimeAPI(t, handler, path)
			var payload struct {
				Summary bool             `json:"summary"`
				Skills  []map[string]any `json:"skills"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Summary != summary || len(payload.Skills) != 1 {
				t.Fatalf("bad inventory: %s", list.Body.String())
			}
			item := payload.Skills[0]
			if item["enabled"] != (action == "enable") {
				t.Fatalf("toggle not reflected: %#v", item)
			}
			_, counted := item["file_count"]
			if counted == summary {
				t.Fatalf("wrong summary file behavior: %#v", item)
			}
		}
	}
}

func TestRuntimePluginToggleHTTPBody(t *testing.T) {
	cfg := testConfig(t)
	runtime, err := newHTTPUnrestrictedRuntime(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	source := filepath.Join(cfg.AgentDockDefaultDir, "inventory-plugin")
	if err := os.MkdirAll(filepath.Join(source, "skills", "inventory-member"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"inventory-plugin","version":"1.0.0","description":"Inventory regression."}`)
	writeTestFile(t, filepath.Join(source, "skills", "inventory-member", "SKILL.md"), "---\nname: inventory-member\ndescription: Inventory member.\n---\n# Member\n")
	if _, err := runtime.Call(context.Background(), "plugin_manage", map[string]any{"action": "install", "source": source}); err != nil {
		t.Fatal(err)
	}
	handler := runtimeAPIHandler(runtime, cfg, auth.NewOAuthStore())
	for _, action := range []string{"heavy_enable", "heavy_disable", "disable", "enable"} {
		request := httptest.NewRequest(http.MethodPost, "/internal/runtime/plugins", strings.NewReader(`{"action":"`+action+`","name":"inventory-plugin"}`))
		response := httptest.NewRecorder()
		request.RemoteAddr = "127.0.0.1:12000"
		request.Host = "127.0.0.1"
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("HTTP plugin %s lost its body: %d %s", action, response.Code, response.Body.String())
		}
	}
}
