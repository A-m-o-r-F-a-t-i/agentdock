package runtimeapi

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/app"
)

type runtimeStub struct {
	skillTarget   string
	skillFilePath string
	taskStatus    string
	taskLimit     int
	skillArgs     map[string]any
	pluginArgs    map[string]any
	mcpArgs       map[string]any
}

func (r *runtimeStub) RuntimeStatus() app.Result          { return app.Result{"status": "ok"} }
func (r *runtimeStub) RuntimeSkills() (app.Result, error) { return app.Result{}, nil }
func (r *runtimeStub) RuntimeSkill(skill string) (app.Result, error) {
	r.skillTarget = skill
	return app.Result{}, nil
}
func (r *runtimeStub) RuntimeSkillManage(_ context.Context, args map[string]any) (app.Result, error) {
	r.skillArgs = args
	return app.Result{"changed": true}, nil
}
func (r *runtimeStub) RuntimeSkillFiles(skill string) (app.Result, error) {
	r.skillTarget = skill
	return app.Result{}, nil
}
func (r *runtimeStub) RuntimeSkillFile(skill, filePath string) (app.Result, error) {
	r.skillTarget, r.skillFilePath = skill, filePath
	return app.Result{}, nil
}
func (r *runtimeStub) RuntimeTasks(status string, limit int) (app.Result, error) {
	r.taskStatus, r.taskLimit = status, limit
	return app.Result{"status": status, "limit": limit}, nil
}
func (r *runtimeStub) RuntimeTask(string) (app.Result, error)       { return app.Result{}, nil }
func (r *runtimeStub) RuntimeTaskDelete(string) (app.Result, error) { return app.Result{}, nil }
func (r *runtimeStub) RuntimeCapabilities(context.Context, bool) (app.Result, error) {
	return app.Result{}, nil
}
func (r *runtimeStub) RuntimeMCPServers(context.Context) (app.Result, error) {
	return app.Result{}, nil
}
func (r *runtimeStub) RuntimeMCPServer(context.Context, string) (app.Result, error) {
	return app.Result{}, nil
}
func (r *runtimeStub) RuntimeMCPManage(_ context.Context, args map[string]any) (app.Result, error) {
	r.mcpArgs = args
	return app.Result{"changed": true}, nil
}
func (r *runtimeStub) RuntimePlugins(context.Context) (app.Result, error) {
	return app.Result{}, nil
}
func (r *runtimeStub) RuntimePlugin(context.Context, string) (app.Result, error) {
	return app.Result{}, nil
}
func (r *runtimeStub) RuntimePluginManage(_ context.Context, args map[string]any) (app.Result, error) {
	r.pluginArgs = args
	return app.Result{"changed": true}, nil
}
func (r *runtimeStub) RuntimeEvolve(context.Context, map[string]any) (app.Result, error) {
	return app.Result{}, nil
}

func TestMethodContract(t *testing.T) {
	tests := []struct {
		method string
		path   string
		allow  string
		ok     bool
	}{
		{"GET", "/internal/runtime/status", "GET", true},
		{"POST", "/internal/runtime/capabilities", "GET, POST", true},
		{"POST", "/internal/runtime/skills", "GET, POST", true},
		{"POST", "/internal/runtime/plugins", "GET, POST", true},
		{"GET", "/internal/runtime/plugins/pcb", "GET", true},
		{"DELETE", "/internal/runtime/tasks/task-1", "GET, DELETE", true},
		{"POST", "/internal/runtime/tasks/task-1", "GET, DELETE", false},
		{"GET", "/internal/runtime/evolve", "POST", true},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			if got := MethodAllowed(test.method, test.path); got != test.ok {
				t.Fatalf("MethodAllowed() = %v, want %v", got, test.ok)
			}
			if got := AllowHeader(test.path); got != test.allow {
				t.Fatalf("AllowHeader() = %q, want %q", got, test.allow)
			}
		})
	}
}

func TestDispatchParsesTaskQuery(t *testing.T) {
	runtime := &runtimeStub{}
	result, err := Dispatch(context.Background(), runtime, Request{
		Method: "GET",
		Path:   "/internal/runtime/tasks",
		Query:  url.Values{"status": {"active"}, "limit": {"25"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.taskStatus != "active" || runtime.taskLimit != 25 {
		t.Fatalf("captured tasks query = status %q limit %d", runtime.taskStatus, runtime.taskLimit)
	}
	if result["status"] != "active" || result["limit"] != 25 {
		t.Fatalf("result = %#v", result)
	}
}

func TestDispatchCapabilityManagementRequests(t *testing.T) {
	runtime := &runtimeStub{}
	if _, err := Dispatch(context.Background(), runtime, Request{
		Method: "POST", Path: "/internal/runtime/skills",
		Body: []byte(`{"action":"disable","skill":"layout"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"action": "disable", "skill": "layout"}; !reflect.DeepEqual(runtime.skillArgs, want) {
		t.Fatalf("Skill args = %#v, want %#v", runtime.skillArgs, want)
	}

	if _, err := Dispatch(context.Background(), runtime, Request{
		Method: "POST", Path: "/internal/runtime/plugins",
		Body: []byte(`{"action":"install","source":"D:\\packages\\pcb.zip"}`),
	}); err != nil {
		t.Fatal(err)
	}
	wantPlugin := map[string]any{
		"action": "install", "source": `D:\packages\pcb.zip`,
	}
	if !reflect.DeepEqual(runtime.pluginArgs, wantPlugin) {
		t.Fatalf("plugin args = %#v, want %#v", runtime.pluginArgs, wantPlugin)
	}
}

func TestDispatchMCPManagePreservesOnlyProvidedFields(t *testing.T) {
	runtime := &runtimeStub{}
	_, err := Dispatch(context.Background(), runtime, Request{
		Method: "POST",
		Path:   "/internal/runtime/mcp",
		Body:   []byte(`{"action":"add","name":"demo","transport":"stdio","args":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"action": "add", "name": "demo", "transport": "stdio", "args": []string{}}
	if !reflect.DeepEqual(runtime.mcpArgs, want) {
		t.Fatalf("MCP args = %#v, want %#v", runtime.mcpArgs, want)
	}
}

func TestDispatchKeepsRouteSpecificValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		code string
	}{
		{
			name: "task limit",
			req:  Request{Method: "GET", Path: "/internal/runtime/tasks", Query: url.Values{"limit": {"201"}}},
			code: "INVALID_LIMIT",
		},
		{
			name: "MCP body limit",
			req:  Request{Method: "POST", Path: "/internal/runtime/mcp", Body: []byte(strings.Repeat("x", 64*1024+1))},
			code: "INVALID_MCP_REQUEST",
		},
		{
			name: "Skill unsupported action",
			req:  Request{Method: "POST", Path: "/internal/runtime/skills", Body: []byte(`{"action":"install","skill":"demo"}`)},
			code: "SKILL_ACTION_UNSUPPORTED",
		},
		{
			name: "plugin unknown field",
			req:  Request{Method: "POST", Path: "/internal/runtime/plugins", Body: []byte(`{"action":"enable","name":"demo","unknown":true}`)},
			code: "INVALID_PLUGIN_REQUEST",
		},
		{
			name: "evolve unknown field",
			req:  Request{Method: "POST", Path: "/internal/runtime/evolve", Body: []byte(`{"intent":"propose","unknown":true}`)},
			code: "INVALID_EVOLVE_REQUEST",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Dispatch(context.Background(), &runtimeStub{}, test.req)
			var toolErr *app.ToolError
			if !errors.As(err, &toolErr) || toolErr.Code != test.code {
				t.Fatalf("error = %#v, want ToolError code %s", err, test.code)
			}
		})
	}
}

func TestDispatchSkillRefQueryUsesExactRuntimeReference(t *testing.T) {
	runtime := &runtimeStub{}
	ref := "skill://plugin/demo.plugin/plugin-skill"
	_, err := Dispatch(context.Background(), runtime, Request{
		Method: "GET",
		Path:   "/internal/runtime/skills/plugin-skill/files/references/guide.md",
		Query:  url.Values{"skill_ref": {ref}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.skillTarget != ref || runtime.skillFilePath != "references/guide.md" {
		t.Fatalf("captured Skill target = %q file=%q", runtime.skillTarget, runtime.skillFilePath)
	}

	runtime = &runtimeStub{}
	_, err = Dispatch(context.Background(), runtime, Request{
		Method: "GET",
		Path:   "/internal/runtime/skills/demo-skill",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.skillTarget != "demo-skill" {
		t.Fatalf("legacy Skill target = %q", runtime.skillTarget)
	}
}
