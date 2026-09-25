package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConversationAttachUsesCurrentRevision(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/runtime/conversations/conv_demo":
			_, _ = io.WriteString(w, `{"conversation":{"state":{"binding_revision":7}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/internal/runtime/conversations/conv_demo/current-task":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"binding_updated":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"conversation", "attach", "conv_demo",
		"--endpoint", server.URL, "--task", "tsk_demo", "--task-thread", "branch", "--json",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if posted["task_id"] != "tsk_demo" || posted["task_thread_id"] != "branch" || posted["binding_revision"] != json.Number("7") && posted["binding_revision"] != float64(7) {
		t.Fatalf("binding request = %#v", posted)
	}
}

func TestPermissionFullRequiresConfirmationBeforeNetwork(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	err := run(context.Background(), []string{
		"permission", "set", "--scope", "global", "--mode", "full", "--expected-revision", "3",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || commandExitCode(err) != controlExitInvalid || !strings.Contains(err.Error(), "--confirm-full") {
		t.Fatalf("unexpected error: %v exit=%d", err, commandExitCode(err))
	}
}

func TestPermissionSetSendsOptimisticRevision(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/runtime/permissions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"revision":10}`)
	}))
	defer server.Close()

	err := run(context.Background(), []string{
		"permission", "set", "--endpoint", server.URL,
		"--scope", "workspace", "--scope-id", "wsp_demo", "--mode", "rules", "--expected-revision", "9", "--json",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if posted["scope"] != "workspace" || posted["scope_id"] != "wsp_demo" || posted["mode"] != "rules" || posted["expected_revision"] != float64(9) {
		t.Fatalf("permission request = %#v", posted)
	}
}

func TestInsertionEnqueueGeneratesSubmissionID(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/runtime/conversations/conv_demo/insertions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"insertion":{"status":"queued"}}`)
	}))
	defer server.Close()

	err := run(context.Background(), []string{
		"insertion", "enqueue", "conv_demo", "--endpoint", server.URL, "--text", "new direction", "--json",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if posted["text"] != "new direction" || !strings.HasPrefix(posted["submission_id"].(string), "cli_") {
		t.Fatalf("insertion request = %#v", posted)
	}
}

func TestInterspersedControlFlagsKeepSingleIdentifier(t *testing.T) {
	var raw controlOptions
	flags := newControlFlagSet("demo", &bytes.Buffer{}, &raw)
	value := flags.String("value", "", "value")
	if err := flags.Parse([]string{"object-1", "--json", "--value", "set"}); err != nil {
		t.Fatal(err)
	}
	if got := flags.Args(); len(got) != 1 || got[0] != "object-1" || !raw.jsonOutput || *value != "set" {
		t.Fatalf("positionals=%v json=%v value=%q", got, raw.jsonOutput, *value)
	}
}
