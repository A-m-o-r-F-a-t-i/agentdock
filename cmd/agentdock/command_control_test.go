package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlStatusUsesTokenFileAndJSON(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("secret-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/runtime/status" || r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("unexpected request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"service":"AgentDock"}`)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"status", "--endpoint", server.URL, "--token-file", tokenPath, "--json"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result["ok"] != true {
		t.Fatalf("status output = %q err=%v", stdout.String(), err)
	}
	if strings.Contains(stdout.String()+stderr.String(), "secret-token") {
		t.Fatal("token leaked to output")
	}
}

func TestControlTaskCreateSendsTaskManageShape(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/runtime/tasks" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"task_id":"tsk_demo"}`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"task", "create", "--endpoint", server.URL, "--json",
		"--title", "Demo", "--goal", "Ship CLI", "--condition", "Actions pass",
		"--task-step", "S1=Implement", "--workspace", "wsp_demo",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if body["action"] != "create" || body["title"] != "Demo" || body["workspace_id"] != "wsp_demo" {
		t.Fatalf("task body = %#v", body)
	}
	conditions, ok := body["completion_conditions"].([]any)
	if !ok || len(conditions) != 1 || conditions[0] != "Actions pass" {
		t.Fatalf("conditions = %#v", body["completion_conditions"])
	}
}

func TestControlActivityWatchEmitsJSONL(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/runtime/activity/stream" || r.URL.Query().Get("after") != "41" || r.Header.Get("Last-Event-ID") != "41" {
			t.Fatalf("unexpected stream request: %s?%s last=%q", r.URL.Path, r.URL.RawQuery, r.Header.Get("Last-Event-ID"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 42\nevent: activity\ndata: {\"seq\":42,\"status\":\"succeeded\"}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var stdout bytes.Buffer
	err := run(ctx, []string{"activity", "watch", "--endpoint", server.URL, "--jsonl", "--after", "41", "--timeout", "2s"}, &stdout, &bytes.Buffer{})
	if err == nil || commandExitCode(err) != controlExitTimeout || !controlErrorReported(err) {
		t.Fatalf("stream error=%v exit=%d reported=%v", err, commandExitCode(err), controlErrorReported(err))
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stream output lines = %d: %q", len(lines), stdout.String())
	}
	var event controlSSEEvent
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("stream output = %q: %v", stdout.String(), err)
	}
	if event.ID != "42" || event.Event != "activity" {
		t.Fatalf("event = %#v", event)
	}
	var terminal map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &terminal); err != nil || terminal["code"] != "STREAM_TIMEOUT" || terminal["exit_code"] != float64(controlExitTimeout) {
		t.Fatalf("terminal stream event = %#v err=%v", terminal, err)
	}
}

func TestLegacyPluginListWithHomeDoesNotUseControlClient(t *testing.T) {
	home := t.TempDir()
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"plugin", "list", "--home", home}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("legacy plugin list output is not JSON: %q", stdout.String())
	}
}

func TestControlConfigPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTDOCK_HOME", home)
	config := `{"default_profile":"phone","profiles":{"phone":{"endpoint":"http://127.0.0.1:9000","workspace_id":"wsp_profile","timeout":"3s"}}}`
	if err := os.WriteFile(filepath.Join(home, "cli.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTDOCK_ENDPOINT", "http://127.0.0.1:9001")
	options, err := resolveControlOptions(controlOptions{endpoint: "http://127.0.0.1:9002", workspaceID: "wsp_flag"})
	if err != nil {
		t.Fatal(err)
	}
	if options.endpoint != "http://127.0.0.1:9002" || options.workspaceID != "wsp_flag" || options.timeout.String() != "3s" {
		t.Fatalf("resolved options = %#v", options)
	}
}

func TestControlHTTPStatusExitMapping(t *testing.T) {
	tests := []struct {
		status int
		want   int
	}{
		{http.StatusBadRequest, controlExitInvalid},
		{http.StatusNotFound, controlExitNotFound},
		{http.StatusUnauthorized, controlExitPermission},
		{http.StatusConflict, controlExitConflict},
		{http.StatusMultiStatus, controlExitPartial},
		{http.StatusNotImplemented, controlExitUnsupported},
		{http.StatusGatewayTimeout, controlExitTimeout},
		{http.StatusServiceUnavailable, controlExitService},
	}
	for _, test := range tests {
		err := decodeControlHTTPError(test.status, []byte(`{"code":"DEMO","error":"failed"}`))
		if got := commandExitCode(err); got != test.want {
			t.Fatalf("status %d exit = %d, want %d", test.status, got, test.want)
		}
	}
}
