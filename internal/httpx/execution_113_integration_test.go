package httpx

import (
	"context"
	"encoding/json"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

func Test113HTTPConversationLifecycleAndSourceState(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	initial := f.call(t, "lifecycle-http", "list_dir", map[string]any{"path": "."})
	conversation := initial["conversation_id"].(string)
	connection, status := f.request(t, "GET", "/internal/runtime/execution/connection", nil)
	if status != 200 || connection["state"] == "unobserved" || connection["state"] == "reauthorization_required" {
		t.Fatalf("real authenticated MCP evidence missing: %d %+v", status, connection)
	}
	result, status := f.request(t, "POST", "/internal/runtime/conversations/"+conversation+"/terminate", map[string]any{"confirm": true})
	if status != 200 || result["terminated"] != true {
		t.Fatalf("HTTP stop failed: %d %+v", status, result)
	}
	call, err := f.session.CallTool(context.Background(), &sdk.CallToolParams{Meta: sdk.Meta{"openai/session": "lifecycle-http"}, Name: "list_dir", Arguments: map[string]any{"path": "."}})
	if err != nil || call == nil || !call.IsError {
		t.Fatalf("stopped MCP call was allowed: %+v %v", call, err)
	}
	raw, _ := json.Marshal(call)
	if !strings.Contains(string(raw), "CONVERSATION_TERMINATED") || !strings.Contains(string(raw), "用户已终止此对话，请停止执行。") {
		t.Fatalf("termination message not carried across MCP: %s", raw)
	}
	if _, status := f.request(t, "GET", "/internal/runtime/conversations/"+conversation, nil); status != 200 {
		t.Fatal("stopped history is inaccessible")
	}
	result, status = f.request(t, "POST", "/internal/runtime/conversations/"+conversation+"/resume", map[string]any{"confirm": true})
	if status != 200 || result["terminated"] != false {
		t.Fatalf("HTTP recovery failed: %d %+v", status, result)
	}
	resumed := f.call(t, "lifecycle-http", "list_dir", map[string]any{"path": "."})
	if resumed["conversation_id"] != conversation {
		t.Fatal("recovery created a new conversation")
	}
	for _, action := range []string{"trash", "delete"} {
		result, status = f.request(t, "POST", "/internal/runtime/conversations/batch", map[string]any{"ids": []string{conversation}, "action": action, "confirm_permanent": action == "delete"})
		if status != 200 || result["failed"].(float64) != 0 {
			t.Fatalf("%s: %d %+v", action, status, result)
		}
	}
	if _, status := f.request(t, "GET", "/internal/runtime/conversations/"+conversation, nil); status != 410 {
		t.Fatalf("deleted source must be distinguishable from unavailable: %d", status)
	}
}

func Test113HTTPStableCallManagement(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	call := f.call(t, "management-http", "list_dir", map[string]any{"path": "."})
	id := call["call_id"].(string)
	for _, action := range []string{"archive", "unarchive", "isolate", "unisolate", "trash", "restore", "trash", "delete"} {
		result, status := f.request(t, "POST", "/internal/runtime/calls/batch", map[string]any{"ids": []string{id}, "action": action, "confirm_permanent": action == "delete"})
		if status != 200 || result["failed"].(float64) != 0 {
			t.Fatalf("call %s failed: %d %+v", action, status, result)
		}
	}
	if _, status := f.request(t, "GET", "/internal/runtime/calls/"+id, nil); status != 404 {
		t.Fatal("deleted call detail remained visible")
	}
}
