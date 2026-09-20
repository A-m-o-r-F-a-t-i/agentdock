package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
	coremcp "github.com/uvwt/agentdock/internal/mcp"
)

type executionBearerTransport struct{ base http.RoundTripper }

func (t executionBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer execution-fixture-token")
	return t.base.RoundTrip(copy)
}

type executionHTTPFixture struct {
	runtime *app.Runtime
	server  *httptest.Server
	client  *http.Client
	session *sdk.ClientSession
	root    string
}

func newExecutionHTTPFixture(t *testing.T) *executionHTTPFixture {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{AgentDockDefaultDir: root, AgentDockHome: filepath.Join(root, ".agentdock"), AuthToken: "execution-fixture-token"}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	r, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	serverCore := coremcp.NewServer(r, cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpEndpointHandler(serverCore, cfg, nil))
	registerRuntimeAPI(mux, r, cfg, nil)
	server := httptest.NewServer(mux)
	client := &http.Client{Transport: executionBearerTransport{base: http.DefaultTransport}, Timeout: 12 * time.Second}
	agent := sdk.NewClient(&sdk.Implementation{Name: "execution-contract-fixture", Version: "1"}, nil)
	session, err := agent.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		server.Close()
		_ = r.Close()
		t.Fatal(err)
	}
	fixture := &executionHTTPFixture{runtime: r, server: server, client: client, session: session, root: root}
	t.Cleanup(func() {
		_ = session.Close()
		server.Close()
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return fixture
}
func (f *executionHTTPFixture) call(t *testing.T, host, name string, args map[string]any) app.Result {
	t.Helper()
	result, err := f.session.CallTool(context.Background(), &sdk.CallToolParams{Meta: sdk.Meta{"openai/session": host}, Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var body app.Result
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("MCP %s failed: %+v", name, body)
	}
	return body
}
func (f *executionHTTPFixture) request(t *testing.T, method, path string, input any) (app.Result, int) {
	t.Helper()
	var reader io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result app.Result
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result, response.StatusCode
}
func TestExecutionHTTPRealMCPConversationApprovalAndIsolation(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "sample.txt"), []byte("readable source content"), 0600); err != nil {
		t.Fatal(err)
	}
	first := f.call(t, "chat-A", "agentdock_context", map[string]any{})
	second := f.call(t, "chat-B", "agentdock_context", map[string]any{})
	a, b := first["conversation_id"].(string), second["conversation_id"].(string)
	if a == b || a == "" {
		t.Fatal("parallel host conversations merged")
	}
	for _, host := range []string{"chat-A", "chat-B", "chat-A"} {
		read := f.call(t, host, "read_file", map[string]any{"path": "sample.txt"})
		want := a
		if host == "chat-B" {
			want = b
		}
		if read["conversation_id"] != want {
			t.Fatal("MCP source metadata was not stable")
		}
	}
	rejected, err := f.session.CallTool(context.Background(), &sdk.CallToolParams{Meta: sdk.Meta{"openai/session": "chat-A"}, Name: "read_file", Arguments: map[string]any{"path": "sample.txt", "conversation_id": b}})
	if err != nil || !rejected.IsError {
		t.Fatal("conflicting explicit conversation accepted")
	}
	// The SDK validates the advertised pending-approval output schema here.
	pending := f.call(t, "chat-A", "file_edit", map[string]any{"action": "add", "path": "approved.txt", "content": "fixed request"})
	if pending["status"] != "pending_approval" || pending["executed"] != false {
		t.Fatal("not an explicit pending result")
	}
	if _, err := os.Stat(filepath.Join(f.root, "approved.txt")); !os.IsNotExist(err) {
		t.Fatal("unapproved MCP request modified a file")
	}
	approval := pending["approval_id"].(string)
	callID := pending["call_id"].(string)
	detail, status := f.request(t, "GET", "/internal/runtime/approvals/"+approval, nil)
	if status != 200 || detail["request_available"] != true || !strings.Contains(detail["fixed_request"].(string), "fixed request") {
		t.Fatal("fixed approval parameters not inspectable")
	}
	var dispatched atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, status := f.request(t, "POST", "/internal/runtime/approvals/"+approval+"/approve", map[string]any{})
			if status != 200 {
				t.Errorf("approval status %d: %+v", status, response)
			}
			if response["dispatched"] == true {
				dispatched.Add(1)
			}
		}()
	}
	wg.Wait()
	if dispatched.Load() != 1 {
		t.Fatalf("duplicate approvals dispatched %d times", dispatched.Load())
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		call, err := f.runtime.ActivityJournal().Call(context.Background(), callID)
		if err != nil {
			t.Fatal(err)
		}
		if activity.CallTerminal(call.Status) {
			if call.Status != "succeeded" {
				t.Fatalf("approved call failed: %+v", call)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approved call did not complete")
		}
		time.Sleep(15 * time.Millisecond)
	}
	content, err := os.ReadFile(filepath.Join(f.root, "approved.txt"))
	if err != nil || string(content) != "fixed request" {
		t.Fatal("approved fixed operation was not executed")
	}
	for _, id := range []string{a, b} {
		page, err := f.runtime.ActivityJournal().Calls(context.Background(), activity.CallQuery{ConversationID: id})
		if err != nil || len(page.Calls) == 0 {
			t.Fatal("conversation call projection missing")
		}
		for _, call := range page.Calls {
			if call.ConversationID != id || call.TaskID != "" {
				t.Fatal("call identity leaked or a task was fabricated")
			}
		}
	}
	bad, status := f.request(t, "POST", "/internal/runtime/approvals/"+approval+"/approve", map[string]any{"arguments": map[string]any{"path": "different.txt"}})
	if status != 400 {
		t.Fatalf("approval accepted replacement arguments: %d %+v", status, bad)
	}
}
func TestExecutionHTTPControlBoundariesAndPaging(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	cases := []struct {
		method, path string
		body         any
	}{{"GET", "/internal/runtime/conversations", nil}, {"GET", "/internal/runtime/calls", nil}, {"GET", "/internal/runtime/permissions/effective", nil}, {"GET", "/internal/runtime/approvals", nil}, {"POST", "/internal/runtime/permissions", map[string]any{"scope": "global", "mode": "full", "expected_revision": 1, "confirm_full": true}}, {"POST", "/internal/runtime/conversations/batch", map[string]any{"ids": []string{"conv_11111111111111111111111111111111"}, "action": "trash"}}}
	for _, test := range cases {
		for _, header := range []string{"Forwarded", "X-Forwarded-For", "Origin", "Tailscale-User-Login"} {
			var input io.Reader
			if test.body != nil {
				raw, _ := json.Marshal(test.body)
				input = bytes.NewReader(raw)
			}
			request, _ := http.NewRequest(test.method, f.server.URL+test.path, input)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(header, "https://untrusted.example")
			response, err := f.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 403 {
				t.Fatalf("%s %s accepted %s: %d", test.method, test.path, header, response.StatusCode)
			}
		}
	}
	result, status := f.request(t, "POST", "/internal/runtime/permissions", map[string]any{"scope": "global", "mode": "full", "expected_revision": 1})
	if status != 400 {
		t.Fatalf("full mode without confirmation accepted: %d %+v", status, result)
	}
	first := f.call(t, "paging", "agentdock_context", map[string]any{})
	for i := 0; i < 5; i++ {
		f.call(t, "paging", "list_dir", map[string]any{"path": "."})
	}
	id := first["conversation_id"].(string)
	page, status := f.request(t, "GET", "/internal/runtime/calls?conversation_id="+id+"&limit=2", nil)
	if status != 200 || page["has_more"] != true {
		t.Fatal("calls are not paginated")
	}
	before := uint64(page["next_before"].(float64))
	second, status := f.request(t, "GET", fmt.Sprintf("/internal/runtime/calls?conversation_id=%s&limit=2&before=%d", id, before), nil)
	if status != 200 || len(second["calls"].([]any)) != 2 {
		t.Fatal("next call page missing")
	}
	for _, entry := range second["calls"].([]any) {
		if uint64(entry.(map[string]any)["created_seq"].(float64)) >= before {
			t.Fatal("call pages overlap")
		}
	}
}
