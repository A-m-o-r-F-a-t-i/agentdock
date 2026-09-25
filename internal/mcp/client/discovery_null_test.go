package client

import (
	"context"
	"encoding/json"
	"errors"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMalformedDiscoveryRetainsLastGoodCatalog(t *testing.T) {
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad fixture request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch request.Method {
		case "server/discover":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			return
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "null-fixture", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			tool := map[string]any{"name": "kept", "inputSchema": map[string]any{"type": "object"}, "description": "original data"}
			switch mode.Load() {
			case 1:
				result = map[string]any{"tools": []any{nil}}
			case 2:
				result = nil
			case 3:
				tool["inputSchema"] = nil
				result = map[string]any{"tools": []any{tool}}
			case 4:
				result = map[string]any{"tools": nil}
			case 5:
				result = map[string]any{"tools": []any{}}
			default:
				result = map[string]any{"tools": []any{tool}}
			}
		default:
			http.Error(w, "unexpected fixture method", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.Add(ServerConfig{Name: "null-fixture", Description: "isolated discovery fixture", Transport: TransportStreamableHTTP, URL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before, err := manager.Catalog(t.Context(), "null-fixture")
	if err != nil || len(before.Tools) != 1 {
		t.Fatal("initial catalog", err)
	}
	for _, broken := range []int32{1, 2, 3, 4} {
		mode.Store(broken)
		summary, _, err := manager.Refresh(t.Context(), "null-fixture")
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != "MCP_INVALID_RESPONSE" {
			t.Fatalf("mode %d did not produce structured invalid response: %v", broken, err)
		}
		if !summary.LastGoodAvailable || summary.ToolCount != 1 || summary.Revision != before.Revision {
			t.Fatalf("mode %d overwrote known-good summary: %+v", broken, summary)
		}
		cached, _, err := manager.CachedCatalog("null-fixture")
		if err != nil || len(cached.Tools) != 1 || cached.Revision != before.Revision || cached.Tools["kept"].Description != "original data" {
			t.Fatalf("last-good catalog lost: %+v %v", cached, err)
		}
	}
	mode.Store(5)
	summary, _, err := manager.Refresh(t.Context(), "null-fixture")
	if err != nil || summary.ToolCount != 0 || !summary.ToolCountKnown {
		t.Fatalf("valid empty list must remain known zero: %+v %v", summary, err)
	}
}

func TestDiscoveryMiddlewareNilResultsAndOptionalRequest(t *testing.T) {
	c := newStreamableHTTPClient(ServerConfig{Name: "fixture"})
	var typedNil *mcpsdk.ListToolsResult
	for _, result := range []mcpsdk.Result{nil, typedNil, &mcpsdk.ListToolsResult{}, &mcpsdk.ListToolsResult{Tools: []*mcpsdk.Tool{nil}}} {
		handler := c.validateDiscoveryResponse(func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) { return result, nil })
		got, err := handler(t.Context(), "tools/list", nil)
		var failure *Error
		if got != nil || !errors.As(err, &failure) || failure.Code != "MCP_INVALID_RESPONSE" {
			t.Fatalf("invalid result accepted: %v %v", got, err)
		}
	}
	original := &mcpsdk.ListToolsResult{Tools: []*mcpsdk.Tool{{Name: "valid", InputSchema: map[string]any{"type": "object"}}}}
	handler := c.validateDiscoveryResponse(func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) { return original, nil })
	got, err := handler(t.Context(), "tools/list", nil)
	if err != nil || got != original {
		t.Fatalf("valid optional metadata changed: %v %v", got, err)
	}
	if _, err := convertSDKTool(original.Tools[0]); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("transport failure")
	handler = c.validateDiscoveryResponse(func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) { return nil, sentinel })
	if _, err := handler(t.Context(), "tools/list", nil); err != sentinel {
		t.Fatal("transport error replaced")
	}
}
