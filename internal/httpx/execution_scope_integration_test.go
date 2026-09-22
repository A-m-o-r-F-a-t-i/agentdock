package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/activity"
)

func TestExecutionSDKMetadataTwentyCallsAndReconnect(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "scope.txt"), []byte("scope fixture content"), 0600); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, host := range []string{"metadata-a", "metadata-b"} {
		ids[host] = f.call(t, host, "agentdock_context", nil)["conversation_id"].(string)
	}
	// Keep the test concurrent enough to expose crossed request metadata without
	// turning a correctness check into a 40-request cold-filesystem load test.
	// Windows GitHub runners can be heavily contended while the full repository
	// suite is running, so each request also gets an explicit bounded deadline.
	f.client.Timeout = 45 * time.Second
	type callJob struct {
		host  string
		index int
	}
	jobs := make(chan callJob)
	failures := make(chan error, 40)
	var wg sync.WaitGroup
	const workers = 8
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for job := range jobs {
				name, args := "list_dir", map[string]any{"path": ".", "max_entries": 2}
				if job.index%3 == 1 {
					name, args = "read_file", map[string]any{"path": "scope.txt"}
				}
				if job.index%3 == 2 {
					name, args = "search_text", map[string]any{"path": "scope.txt", "query": "scope"}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				result, err := f.callContext(ctx, job.host, name, args)
				cancel()
				if err != nil {
					failures <- fmt.Errorf("%s call %d (%s): %w", job.host, job.index, name, err)
					continue
				}
				if result["conversation_id"] != ids[job.host] {
					failures <- fmt.Errorf("%s call %d crossed request metadata: %+v", job.host, job.index, result)
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		for _, host := range []string{"metadata-a", "metadata-b"} {
			jobs <- callJob{host: host, index: i}
		}
	}
	close(jobs)
	wg.Wait()
	close(failures)
	var firstFailure error
	failureCount := 0
	for err := range failures {
		failureCount++
		if firstFailure == nil {
			firstFailure = err
		}
	}
	if failureCount > 0 {
		t.Fatalf("%d bounded concurrent SDK calls failed; first failure: %v", failureCount, firstFailure)
	}
	for _, host := range []string{"metadata-a", "metadata-b"} {
		page, err := f.runtime.ActivityJournal().Calls(context.Background(), activity.CallQuery{ConversationID: ids[host], Limit: 100})
		if err != nil || len(page.Calls) != 21 {
			t.Fatalf("wrong call inventory: %d %v", len(page.Calls), err)
		}
		for _, call := range page.Calls {
			if call.TaskID != "" || call.BindingQuality != "host_metadata" {
				t.Fatalf("fabricated task or source: %+v", call)
			}
		}
	}
	// A new SDK connection generates a new transport session. Only the trusted
	// host metadata determines whether the local Conversation continues.
	client := sdk.NewClient(&sdk.Implementation{Name: "reconnected-fixture", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: f.server.URL + "/mcp", HTTPClient: f.client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Meta: sdk.Meta{"openai/session": "metadata-a"}, Name: "list_dir", Arguments: map[string]any{"path": "."}})
	if err != nil || result.IsError {
		t.Fatalf("reconnection failed: %+v %v", result, err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["conversation_id"] != ids["metadata-a"] {
		t.Fatal("transport reconnect split host conversation")
	}
	t.Log("Real SDK/HTTP serialization verified: 20 business calls per simulated host conversation; no conversation_id in arguments; same host metadata survives a new MCP transport session. This does not validate the ChatGPT webpage connector.")
}

func TestExecutionSDKMissingMetadataRemainsUnattributed(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	for i := 0; i < 3; i++ {
		result, err := f.session.CallTool(context.Background(), &sdk.CallToolParams{Name: "agentdock_context", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatalf("missing metadata request failed: %+v %v", result, err)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var body map[string]any
		json.Unmarshal(raw, &body)
		if id, _ := body["conversation_id"].(string); id != "" {
			t.Fatal("MCP transport session was guessed to be a conversation")
		}
		if body["binding_quality"] != "unattributed" {
			t.Fatalf("missing fallback quality: %+v", body)
		}
	}
	page, status := f.request(t, "GET", "/internal/runtime/conversations", nil)
	if status != 200 {
		t.Fatal(page)
	}
	for _, raw := range page["conversations"].([]any) {
		item := raw.(map[string]any)
		if item["is_unattributed"] != true {
			t.Fatalf("fake Conversation created: %+v", item)
		}
	}
	calls, err := f.runtime.ActivityJournal().Calls(context.Background(), activity.CallQuery{Unattributed: true, Limit: 100})
	if err != nil || len(calls.Calls) != 3 {
		t.Fatalf("unknown-source calls vanished: %d %v", len(calls.Calls), err)
	}
}

func TestExecutionHTTPViewDoesNotBindAndExplicitCurrentTaskDoes(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	create := func(host, title string) map[string]any {
		return f.call(t, host, "task_manage", map[string]any{"action": "create", "title": title, "goal": "Verify current task", "completion_conditions": []string{"verified"}, "steps": []map[string]any{{"id": "check", "title": "Check"}}})
	}
	a, b := create("binding-a", "A"), create("binding-b", "B")
	conversation, taskA, taskB := a["conversation_id"].(string), a["task_id"].(string), b["task_id"].(string)
	if _, status := f.request(t, "GET", "/internal/runtime/tasks/"+taskB, nil); status != 200 {
		t.Fatal("task view failed")
	}
	read := f.call(t, "binding-a", "list_dir", map[string]any{"path": "."})
	if read["task_id"] != taskA {
		t.Fatal("UI view changed continuation")
	}
	detail, status := f.request(t, "GET", "/internal/runtime/conversations/"+conversation, nil)
	if status != 200 {
		t.Fatal(detail)
	}
	revision := detail["conversation"].(map[string]any)["state"].(map[string]any)["binding_revision"]
	path := "/internal/runtime/conversations/" + conversation + "/current-task"
	changed, status := f.request(t, "POST", path, map[string]any{"task_id": taskB, "binding_revision": revision})
	if status != 200 || changed["binding_updated"] != true {
		t.Fatalf("explicit current task failed: %d %+v", status, changed)
	}
	read = f.call(t, "binding-a", "list_dir", map[string]any{"path": "."})
	if read["task_id"] != taskB {
		t.Fatal("explicit binding was not inherited")
	}
	_, status = f.request(t, "POST", path, map[string]any{"task_id": taskA, "binding_revision": revision})
	if status != 409 {
		t.Fatalf("stale binding update accepted: %d", status)
	}
	page, err := f.runtime.ActivityJournal().Calls(context.Background(), activity.CallQuery{Unattributed: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range page.Calls {
		if call.ToolName == "conversation.set_current" {
			found = true
			if call.ConversationID != "" || call.Source != "http:control" {
				t.Fatalf("management target impersonated origin: %+v", call)
			}
		}
	}
	if !found {
		t.Fatal("explicit local action not audited")
	}
}
