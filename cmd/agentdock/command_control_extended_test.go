package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestControlMachineErrorEnvelope(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"code":"UNAUTHORIZED","error":"token required"}`)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"status", "--endpoint", server.URL, "--json"}, &stdout, &stderr)
	if err == nil || commandExitCode(err) != controlExitPermission || !controlErrorReported(err) {
		t.Fatalf("error=%v exit=%d reported=%v", err, commandExitCode(err), controlErrorReported(err))
	}
	var envelope map[string]any
	if decodeErr := json.Unmarshal(stdout.Bytes(), &envelope); decodeErr != nil {
		t.Fatalf("machine error output = %q: %v", stdout.String(), decodeErr)
	}
	if envelope["ok"] != false || envelope["code"] != "UNAUTHORIZED" || envelope["exit_code"] != float64(controlExitPermission) {
		t.Fatalf("machine error envelope = %#v", envelope)
	}
	if stderr.Len() != 0 {
		t.Fatalf("machine error wrote duplicate stderr: %q", stderr.String())
	}
}

func TestWorkspaceUpdateSendsOnlyExplicitFields(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/runtime/workspaces" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"workspace":{"workspace_id":"wsp_demo","rules_revision":8}}`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"workspace", "update", "wsp_demo", "--endpoint", server.URL,
		"--expected-revision", "7", "--name", "Renamed", "--json",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if body["action"] != "register" || body["workspace_id"] != "wsp_demo" || body["expected_revision"] != float64(7) || body["name"] != "Renamed" {
		t.Fatalf("workspace update body = %#v", body)
	}
	for _, unexpected := range []string{"root", "runtime", "artifact_root", "scratch_root", "cache_root"} {
		if _, ok := body[unexpected]; ok {
			t.Fatalf("workspace update unexpectedly sent %s: %#v", unexpected, body)
		}
	}
}

func TestCallWaitPreservesFailedTerminalStatus(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/runtime/calls/call_demo" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"call":{"call_id":"call_demo","status":"failed","error":"boom"}}`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"call", "wait", "call_demo", "--endpoint", server.URL,
		"--wait-timeout", "1s", "--poll-interval", "100ms", "--json",
	}, &stdout, &bytes.Buffer{})
	if err == nil || commandExitCode(err) != controlExitFailure || !controlErrorReported(err) {
		t.Fatalf("error=%v exit=%d reported=%v", err, commandExitCode(err), controlErrorReported(err))
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) {
		t.Fatalf("failed call result not preserved: %q", stdout.String())
	}
}

func TestInsertionWaitPreservesDeliveryUnknown(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/runtime/conversations/conv_demo/insertions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"insertions":[{"insertion_id":"ins_demo","status":"delivery_unknown"}]}`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"insertion", "wait", "conv_demo", "ins_demo", "--endpoint", server.URL,
		"--wait-timeout", "1s", "--poll-interval", "100ms", "--json",
	}, &stdout, &bytes.Buffer{})
	if err == nil || commandExitCode(err) != controlExitConflict || !controlErrorReported(err) {
		t.Fatalf("error=%v exit=%d reported=%v", err, commandExitCode(err), controlErrorReported(err))
	}
	if !strings.Contains(stdout.String(), `"status":"delivery_unknown"`) {
		t.Fatalf("uncertain insertion result not preserved: %q", stdout.String())
	}
}

func TestApprovalWaitReturnsResolvedRecord(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/runtime/approvals/apr_demo" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"approval":{"id":"apr_demo","status":"pending"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"approval":{"id":"apr_demo","status":"approved"}}`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"approval", "wait", "apr_demo", "--endpoint", server.URL,
		"--wait-timeout", "1s", "--poll-interval", "100ms", "--json",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 2 || !strings.Contains(stdout.String(), `"status":"approved"`) {
		t.Fatalf("approval result=%q requests=%d", stdout.String(), requests.Load())
	}
}

func TestRootHelpAndCompletions(t *testing.T) {
	var help bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, &help, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.String(), "AgentDock Workbench CLI") || !strings.Contains(help.String(), "conversation") {
		t.Fatalf("help output = %q", help.String())
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var completion bytes.Buffer
		if err := run(context.Background(), []string{"completion", shell}, &completion, &bytes.Buffer{}); err != nil {
			t.Fatalf("completion %s: %v", shell, err)
		}
		if !strings.Contains(completion.String(), "agentdock") {
			t.Fatalf("completion %s output = %q", shell, completion.String())
		}
	}
}
