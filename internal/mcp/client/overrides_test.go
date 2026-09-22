package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func overrideFixture(t *testing.T) (*Manager, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	calls := &atomic.Int32{}
	failure := &atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(400)
			return
		}
		switch req.Method {
		case "initialize":
			calls.Add(1)
			if failure.Load() {
				w.WriteHeader(503)
				return
			}
			writeRPCResult(t, w, req.ID, map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "2.5.1"}})
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			writeRPCResult(t, w, req.ID, map[string]any{"tools": []map[string]any{{"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"}}}})
		case "tools/call":
			writeRPCResult(t, w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "not found"}})
		}
	}))
	t.Cleanup(server.Close)
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err = m.SetExternalServerProvider(func() (map[string]ServerConfig, error) {
		return map[string]ServerConfig{"managed": {Name: "managed", Description: "old 2.3.0 / 20 tools", Transport: TransportStreamableHTTP, URL: server.URL, Enabled: true, TimeoutMS: 1000}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return m, calls, failure
}
func descriptionPatch(value string) ConfigPatch {
	raw, _ := json.Marshal(value)
	return ConfigPatch{"description": raw}
}
func TestManagedMetadataOverlayPreservesPinnedConnectionAndCAS(t *testing.T) {
	m, calls, _ := overrideFixture(t)
	summary, _, err := m.Refresh(context.Background(), "managed")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ServerVersion != "2.5.1" || !summary.ToolCountKnown || summary.ToolCount != 1 {
		t.Fatalf("facts=%+v", summary)
	}
	_, state, release, err := m.lockServer("managed")
	if err != nil {
		t.Fatal(err)
	}
	previous := state.client
	done := make(chan error, 1)
	go func() {
		_, err := m.Update(context.Background(), "managed", summary.Revision, "runtime", descriptionPatch("current tools"), false)
		done <- err
	}()
	select {
	case err = <-done:
		release()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		release()
		t.Fatal("metadata waited for active call")
	}
	_, updated, err := m.Inspect("managed")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description != "current tools" || calls.Load() != 1 || m.states["managed"] != state || state.client != previous {
		t.Fatal("metadata restarted connection")
	}
	_, err = m.Update(context.Background(), "managed", summary.Revision, "runtime", descriptionPatch("stale"), false)
	var issue *Error
	if !errors.As(err, &issue) || issue.Code != "MCP_REVISION_CONFLICT" {
		t.Fatalf("stale CAS=%v", err)
	}
	if _, err = m.Add(ServerConfig{Name: "managed", Transport: TransportStreamableHTTP, URL: "https://example.invalid", Enabled: true}); err == nil {
		t.Fatal("plugin ownership protection removed")
	}
}
func TestManagedPersistentAndRuntimeLayerReset(t *testing.T) {
	m, _, _ := overrideFixture(t)
	_, current, err := m.Inspect("managed")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := m.Update(context.Background(), "managed", current.Revision, "persistent", descriptionPatch("saved"), false)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := m.Update(context.Background(), "managed", saved.Revision, "runtime", descriptionPatch("runtime"), false)
	if err != nil {
		t.Fatal(err)
	}
	reset, err := m.Update(context.Background(), "managed", runtime.Revision, "runtime", nil, true)
	if err != nil || reset.Description != "saved" || reset.OverrideSource != "persistent" {
		t.Fatalf("reset=%+v %v", reset, err)
	}
	reset, err = m.Update(context.Background(), "managed", reset.Revision, "persistent", nil, true)
	if err != nil || reset.Description != "old 2.3.0 / 20 tools" {
		t.Fatalf("default=%+v %v", reset, err)
	}
	for _, field := range []string{"enabled", "tool_count", "server_version", "headers"} {
		if _, err = m.Update(context.Background(), "managed", reset.Revision, "runtime", ConfigPatch{field: json.RawMessage(`true`)}, false); err == nil {
			t.Fatalf("forbidden field %s accepted", field)
		}
	}
}
func TestManagedFailedRefreshRetainsLastGoodTools(t *testing.T) {
	m, _, failure := overrideFixture(t)
	summary, _, err := m.Refresh(context.Background(), "managed")
	if err != nil {
		t.Fatal(err)
	}
	previous := m.states["managed"].client
	failure.Store(true)
	if _, _, err = m.Refresh(context.Background(), "managed"); err == nil {
		t.Fatal("broken refresh succeeded")
	}
	_, current, err := m.Inspect("managed")
	if err != nil {
		t.Fatal(err)
	}
	if m.states["managed"].client != previous || !current.LastGoodAvailable || current.ToolCount != 1 || current.Revision != summary.Revision {
		t.Fatalf("last good lost=%+v", current)
	}
	if _, err = m.Call(context.Background(), "managed:echo", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(m.overridePath, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.Inspect("managed"); err == nil {
		t.Fatal("corrupt override silently accepted")
	}
	data, _ := os.ReadFile(m.overridePath)
	if string(data) != "{broken" {
		t.Fatal("corrupt override overwritten")
	}
}

func TestMetadataUpdateDoesNotWaitForQueuedCallRegistryRead(t *testing.T) {
	m, _, _ := overrideFixture(t)
	summary, _, err := m.Refresh(context.Background(), "managed")
	if err != nil {
		t.Fatal(err)
	}
	_, _, release, err := m.lockServer("managed")
	if err != nil {
		t.Fatal(err)
	}
	queued := make(chan error, 1)
	go func() {
		_, _, unlock, err := m.lockServer("managed")
		if err == nil {
			unlock()
		}
		queued <- err
	}()
	// The scheduling delay merely lets the queued state lock be exercised; no
	// production timeout or outcome depends on this wall-clock value.
	time.Sleep(20 * time.Millisecond)
	updated := make(chan error, 1)
	go func() {
		_, err := m.Update(context.Background(), "managed", summary.Revision, "runtime", descriptionPatch("updated during queue"), false)
		updated <- err
	}()
	select {
	case err := <-updated:
		release()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		release()
		<-queued
		t.Fatal("metadata blocked behind a queued state lock")
	}
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
}
