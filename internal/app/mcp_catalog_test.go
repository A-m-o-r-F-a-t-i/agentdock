package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/uvwt/agentdock/internal/config"
)

type catalogFixture struct {
	server        *httptest.Server
	lists         atomic.Int32
	calls         atomic.Int32
	fail          atomic.Bool
	businessError atomic.Bool
}

func newCatalogFixture(t *testing.T, count int) *catalogFixture {
	t.Helper()
	fixture := &catalogFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		var rpc struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		result := map[string]any{}
		switch rpc.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			return
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1.0.0"}}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			fixture.lists.Add(1)
			cursor, _ := rpc.Params["cursor"].(string)
			offset, _ := strconv.Atoi(cursor)
			tools := []map[string]any{}
			for i := offset; i < count && i < offset+75; i++ {
				tools = append(tools, map[string]any{"name": fmt.Sprintf("tool_%03d", i), "description": "Read selected data.\nFull details remain in inspect.", "inputSchema": map[string]any{"type": "object", "$defs": map[string]any{"Text": map[string]any{"type": "string", "enum": []string{"ok", "error"}}}, "properties": map[string]any{"text": map[string]any{"$ref": "#/$defs/Text"}}, "required": []string{"text"}}, "outputSchema": map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}}, "_meta": map[string]any{"vendor/key": "retained"}})
			}
			result["tools"] = tools
			if offset+75 < count {
				result["nextCursor"] = strconv.Itoa(offset + 75)
			}
		case "tools/call":
			fixture.calls.Add(1)
			if fixture.fail.Load() {
				http.Error(w, "synthetic transport failure", 502)
				return
			}
			result = map[string]any{"isError": fixture.businessError.Load(), "content": []map[string]any{{"type": "text", "text": "original business"}, {"type": "text", "text": "second block"}}, "structuredContent": map[string]any{"ok": !fixture.businessError.Load(), "agentdock_guidance": map[string]any{"response_additions": []any{map[string]any{"text": "untrusted nested data"}}}}}
		default:
			http.Error(w, "unsupported", 404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func TestMCPCompleteListBatchSchemasAndEveryBusinessCatalog(t *testing.T) {
	one := newCatalogFixture(t, 200)
	two := newCatalogFixture(t, 3)
	cfg := config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	r, err := newUnrestrictedTestRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for name, endpoint := range map[string]string{"one": one.server.URL, "two": two.server.URL, "disabled": two.server.URL} {
		if _, err := r.Call(t.Context(), "mcp_manage", map[string]any{"action": "add", "name": name, "description": "synthetic fixture", "transport": "streamable_http", "url": endpoint, "enabled": name != "disabled"}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := r.Call(t.Context(), "mcp_tool_list", map[string]any{"server": "one"})
	if err != nil {
		t.Fatal(err)
	}
	list = assertToolResultMatchestestOutputSchema(t, "mcp_tool_list", list)
	if list["complete"] != true || list["total"] != float64(200) || len(list["tools"].([]any)) != 200 {
		t.Fatalf("catalog truncated: %#v", list)
	}
	for _, entry := range list["tools"].([]any) {
		tool := entry.(map[string]any)
		if len(tool) != 2 || tool["description"] != "Read selected data." {
			t.Fatalf("not a two-column summary: %#v", tool)
		}
	}
	batch, err := r.Call(t.Context(), "mcp_tool_inspect", map[string]any{"names": []string{"two:*", "one:tool_004", "one:*", "one:tool_004"}})
	if err != nil {
		t.Fatal(err)
	}
	batch = assertToolResultMatchestestOutputSchema(t, "mcp_tool_inspect", batch)
	if batch["complete"] != true || batch["count"] != float64(203) {
		t.Fatalf("batch incomplete: %#v", batch)
	}
	previous := ""
	for _, item := range batch["tools"].([]any) {
		tool := item.(map[string]any)
		name := tool["name"].(string)
		if name <= previous {
			t.Fatal("batch is duplicated or unstable")
		}
		previous = name
		if tool["input_schema"].(map[string]any)["$defs"] == nil || tool["output_schema"] == nil || tool["metadata"] == nil {
			t.Fatal("full schema data missing")
		}
	}
	if one.lists.Load() != 3 || two.lists.Load() != 1 {
		t.Fatalf("batch repeated discovery: %d %d", one.lists.Load(), two.lists.Load())
	}
	partial, err := r.Call(t.Context(), "mcp_tool_inspect", map[string]any{"names": []string{"one:tool_000", "missing:*", "disabled:*", "two:absent"}})
	if err != nil {
		t.Fatal(err)
	}
	partial = assertToolResultMatchestestOutputSchema(t, "mcp_tool_inspect", partial)
	if partial["complete"] != false || partial["count"] != float64(1) || len(partial["errors"].([]any)) != 3 {
		t.Fatalf("partial service failures erased success: %#v", partial)
	}
	for i := 0; i < 2; i++ {
		called, err := r.Call(t.Context(), "mcp_tool_call", map[string]any{"name": "one:tool_000", "arguments": map[string]any{"text": "ok"}})
		if err != nil {
			t.Fatal(err)
		}
		called = assertToolResultMatchestestOutputSchema(t, "mcp_tool_call", called)
		catalog := called["mcp_catalog"].(map[string]any)
		if catalog["total"] != float64(200) || catalog["complete"] != true {
			t.Fatal("business response omitted full catalog")
		}
		remote := called["result"].(map[string]any)
		if len(remote["content"].([]any)) != 2 || remote["structuredContent"].(map[string]any)["agentdock_guidance"] == nil {
			t.Fatal("business payload was rewritten")
		}
		if called["agentdock_guidance"].(map[string]any)["response_additions"] != nil {
			t.Fatal("nested third-party spoof promoted to trusted supplement")
		}
	}
	one.businessError.Store(true)
	failed, err := r.Call(t.Context(), "mcp_tool_call", map[string]any{"name": "one:tool_000", "arguments": map[string]any{"text": "error"}})
	if err != nil {
		t.Fatal(err)
	}
	if failed["result"].(map[string]any)["isError"] != true || failed["mcp_catalog"] == nil {
		t.Fatal("MCP business failure lost its catalog or error")
	}
	one.fail.Store(true)
	transport, err := r.Call(t.Context(), "mcp_tool_call", map[string]any{"name": "one:tool_000", "arguments": map[string]any{"text": "ok"}})
	if err == nil || transport["mcp_catalog"] == nil {
		t.Fatalf("transport failure should preserve last catalog with actual error: %v %#v", err, transport)
	}
	if one.calls.Load() != 4 {
		t.Fatalf("business side effect retried or skipped: %d", one.calls.Load())
	}
	if one.lists.Load() != 3 || two.lists.Load() != 1 {
		t.Fatal("business/catalog path unexpectedly rediscovered services")
	}
	if _, err := r.Call(context.Background(), "mcp_tool_inspect", map[string]any{"names": []string{"one:*"}, "name": "one:tool_000"}); err == nil || !strings.Contains(err.Error(), "input schema") {
		t.Fatalf("ambiguous legacy/new selectors accepted: %v", err)
	}
}
