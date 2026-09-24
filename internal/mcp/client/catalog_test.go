package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogPaginationSharedReadsAndInvalidation(t *testing.T) {
	var lists, calls atomic.Int32
	var notify atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
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
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{"listChanged": true}}, "serverInfo": map[string]any{"name": "catalog-fixture", "version": "1.0.0"}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			lists.Add(1)
			cursor, _ := rpc.Params["cursor"].(string)
			offset, _ := strconv.Atoi(cursor)
			tools := []map[string]any{}
			for i := offset; i < offset+75 && i < 200; i++ {
				tools = append(tools, map[string]any{"name": fmt.Sprintf("tool_%03d", i), "description": "Read the selected value.\nThe complete explanation stays in inspect.", "inputSchema": map[string]any{"type": "object", "$defs": map[string]any{"Number": map[string]any{"type": "integer", "minimum": 1}}, "properties": map[string]any{"value": map[string]any{"$ref": "#/$defs/Number"}}, "required": []string{"value"}}, "outputSchema": map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}}, "annotations": map[string]any{"readOnlyHint": true}, "_meta": map[string]any{"vendor/meaning": "kept"}})
			}
			result["tools"] = tools
			if offset+75 < 200 {
				result["nextCursor"] = strconv.Itoa(offset + 75)
			}
		case "tools/call":
			calls.Add(1)
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "business unchanged"}}, "structuredContent": map[string]any{"ok": true}}
		default:
			http.Error(w, "unexpected method", 404)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result}
		if rpc.Method == "tools/call" && notify.Swap(false) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\n")
			data, _ := json.Marshal(response)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer upstream.Close()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Add(ServerConfig{Name: "catalog", Description: "synthetic catalog fixture", Transport: TransportStreamableHTTP, URL: upstream.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 10; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-gate
			value, err := m.Catalog(t.Context(), "catalog")
			if err != nil || len(value.Tools) != 200 || !value.Complete {
				t.Errorf("incomplete catalog %d: %v", len(value.Tools), err)
			}
		}()
	}
	close(gate)
	workers.Wait()
	if lists.Load() != 3 {
		t.Fatalf("ten cold reads rediscovered pagination %d times, want exactly 3 pages", lists.Load())
	}
	value, err := m.Catalog(t.Context(), "catalog")
	if err != nil {
		t.Fatal(err)
	}
	tool := value.Tools["tool_000"]
	refreshedAt := value.RefreshedAt
	if refreshedAt.IsZero() || refreshedAt.After(time.Now()) {
		t.Fatal("catalog omitted its actual refresh time")
	}
	if tool.InputSchema["$defs"] == nil || tool.OutputSchema == nil || tool.Annotations == nil || tool.StandardMetadata["_meta"] == nil {
		t.Fatal("full schema or standard metadata was lost")
	}
	tool.InputSchema["$defs"].(map[string]any)["Number"].(map[string]any)["minimum"] = 999
	value, err = m.Catalog(t.Context(), "catalog")
	if err != nil {
		t.Fatal(err)
	}
	if value.Tools["tool_000"].InputSchema["$defs"].(map[string]any)["Number"].(map[string]any)["minimum"] != float64(1) {
		t.Fatal("returned schema corrupted the dispatch cache")
	}
	if !value.RefreshedAt.Equal(refreshedAt) {
		t.Fatal("a cached read changed the refresh time")
	}
	_, _, release, err := m.lockServer("catalog")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	cached, err := m.Catalog(ctx, "catalog")
	cancel()
	release()
	if err != nil || len(cached.Tools) != 200 {
		t.Fatalf("hot catalog waited for active business call: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.Call(t.Context(), "catalog:tool_000", map[string]any{"value": 1}); err != nil {
			t.Fatal(err)
		}
		summary, err := m.CachedSummary("catalog")
		if err != nil || summary["total"] != 200 {
			t.Fatalf("missing full summary: %v", err)
		}
		for _, item := range summary["tools"].([]map[string]any) {
			if len(item) != 2 || strings.ContainsAny(item["description"].(string), "\r\n") {
				t.Fatal("summary includes fields beyond name and one line")
			}
		}
	}
	if lists.Load() != 3 {
		t.Fatal("business calls or hot reads repeated tools/list")
	}
	notify.Store(true)
	if _, err := m.Call(t.Context(), "catalog:tool_000", map[string]any{"value": 1}); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	state := m.states["catalog"]
	m.mu.RUnlock()
	deadline := time.Now().Add(time.Second)
	for catalogFresh(state.snapshot.Load()) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if catalogFresh(state.snapshot.Load()) {
		t.Fatal("SDK tools/list_changed notification did not invalidate cache")
	}
	if _, err := m.Catalog(t.Context(), "catalog"); err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 6 {
		t.Fatalf("notification refresh pages=%d, want 6 total", lists.Load())
	}
	state.mu.Lock()
	state.refreshedAt = time.Now().Add(-2 * catalogMaxAge)
	publishStateLocked(state)
	state.mu.Unlock()
	stale, fresh, err := m.CachedCatalog("catalog")
	if err != nil || fresh || time.Since(stale.RefreshedAt) < catalogMaxAge {
		t.Fatalf("expired catalog lost its real age: %+v fresh=%v err=%v", stale.RefreshedAt, fresh, err)
	}
	refreshed, err := m.Catalog(t.Context(), "catalog")
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.RefreshedAt.After(stale.RefreshedAt) {
		t.Fatal("TTL refresh did not advance its timestamp")
	}
	if lists.Load() != 9 {
		t.Fatalf("expiry refresh pages=%d", lists.Load())
	}
	if _, err := m.Call(t.Context(), "catalog:tool_000", map[string]any{"value": 0}); err == nil {
		t.Fatal("current $ref/minimum validation was bypassed")
	}
	if calls.Load() != 3 {
		t.Fatal("invalid arguments were forwarded or business retried")
	}
	if _, err := m.SetEnabled("catalog", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CachedCatalog("catalog"); err == nil {
		t.Fatal("disabled catalog leaked from cache")
	}
}

func TestOneLineCatalogDescription(t *testing.T) {
	for text, want := range map[string]string{"读取对象。\n返回完整说明。": "读取对象。", "First sentence.\nSecond sentence.": "First sentence.", "# Heading without punctuation\nextra": "Heading without punctuation extra", "```json\n{\"example\":true}\n```\nActual action.": "Actual action."} {
		if got := OneLineDescription(text); got != want {
			t.Errorf("summary %q != %q", got, want)
		}
	}
}
