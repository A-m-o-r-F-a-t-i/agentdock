package app

import (
	"context"
	"encoding/json"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/insertion"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

func TestInsertionRuntimeOnlyNextExternalRootAndOwnConversation(t *testing.T) {
	r := executionTestRuntime(t)
	host := scopeHost("insert-a")
	other := scopeHost("insert-b")
	callCtx, old := BeginToolResponse(host)
	first, err := r.Call(callCtx, "list_dir", map[string]any{"path": ".", "max_entries": 1})
	if err != nil {
		t.Fatal(err)
	}
	id := stringArg(first, "conversation_id")
	local := activity.WithLocalManagement(context.Background())
	if _, err = r.RuntimeEnqueueInsertion(context.Background(), id, InsertionRequest{SubmissionID: "req_bad", Text: "bad"}); err == nil {
		t.Fatal("remote management accepted")
	}
	queued, err := r.RuntimeEnqueueInsertion(local, id, InsertionRequest{SubmissionID: "req_a", Text: "保留同一源码，先修改要求"})
	if err != nil {
		t.Fatal(err)
	}
	item := queued["insertion"].(insertion.Item)
	if blocks := r.FinishToolResponse(callCtx, old, true); len(blocks) != 0 {
		t.Fatal("old call consumed new input")
	}
	otherCtx, otherResponse := BeginToolResponse(other)
	scopeRead(t, r, otherCtx)
	if blocks := r.FinishToolResponse(otherCtx, otherResponse, true); len(blocks) != 0 {
		t.Fatal("another conversation consumed input")
	}
	nextCtx, next := BeginToolResponse(host)
	result, err := r.Call(nextCtx, "list_dir", map[string]any{"path": ".", "max_entries": 1})
	if err != nil {
		t.Fatal(err)
	}
	if stringArg(result, "conversation_id") != id {
		t.Fatal("wrong call binding")
	}
	blocks := r.FinishToolResponse(nextCtx, next, true)
	if len(blocks) != 1 || !strings.Contains(blocks[0], item.ID) || !strings.HasPrefix(blocks[0], "[[AGENTDOCK_USER_INSERT_V1]]") {
		t.Fatalf("missing supplement=%v", blocks)
	}
	if blocks = r.FinishToolResponse(nextCtx, next, true); len(blocks) != 0 {
		t.Fatal("duplicate response supplement")
	}
	view, err := r.RuntimeInsertions(local, id)
	if err != nil {
		t.Fatal(err)
	}
	items := view["insertions"].([]insertion.Item)
	if items[0].Status != "attached" || items[0].Owner != "" {
		t.Fatalf("bad queue projection=%+v", items)
	}
}
func TestInsertionRuntimeTaskSwitchAndTermination(t *testing.T) {
	r := executionTestRuntime(t)
	host := scopeHost("insert-task")
	scopeTask(t, r, host, "Task A")
	first := scopeRead(t, r, host)
	id := stringArg(first, "conversation_id")
	local := activity.WithLocalManagement(context.Background())
	if _, err := r.RuntimeEnqueueInsertion(local, id, InsertionRequest{SubmissionID: "req_a", Text: "task A only"}); err != nil {
		t.Fatal(err)
	}
	scopeTask(t, r, host, "Task B")
	ctx, response := BeginToolResponse(host)
	scopeRead(t, r, ctx)
	if blocks := r.FinishToolResponse(ctx, response, true); len(blocks) != 0 {
		t.Fatal("task switch redirected input")
	}
	queue, err := r.RuntimeInsertions(local, id)
	if err != nil {
		t.Fatal(err)
	}
	if queue["insertions"].([]insertion.Item)[0].Status != "target_changed" {
		t.Fatal("task switch not paused")
	}
	if _, err = r.RuntimeConversationLifecycle(local, id, "terminate", ConversationLifecycleRequest{Confirm: true}); err != nil {
		t.Fatal(err)
	}
	queue, err = r.RuntimeInsertions(local, id)
	if err != nil {
		t.Fatal(err)
	}
	if queue["insertions"].([]insertion.Item)[0].Status != "cancelled" {
		t.Fatal("termination left input queued")
	}
}
func TestInsertionEligibilityExactBoundary(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		age  time.Duration
		want bool
	}{{-time.Second, false}, {179999 * time.Millisecond, true}, {180 * time.Second, false}, {180001 * time.Millisecond, false}} {
		last := now.Add(-test.age)
		if got := insertionEligible(&last, now); got != test.want {
			t.Fatalf("age=%s eligible=%v", test.age, got)
		}
	}
	if insertionEligible(nil, now) {
		t.Fatal("unknown request considered active")
	}
}

func TestLocalContextCatalogRevisionOutputContract(t *testing.T) {
	r := executionTestRuntime(t)
	if _, err := r.capabilityManager.Add(mcpclient.ServerConfig{Name: "contract_mcp", Description: "local output contract fixture", Transport: mcpclient.TransportStreamableHTTP, URL: "http://127.0.0.1:9/mcp", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	result, err := r.Call(scopeHost("catalog-contract"), "agentdock_context", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	// The shared test helper uses Nexus mode. Validate this local-only extension
	// against the exact schema exposed by this Runtime instead.
	definition, ok := r.ToolDefinition("agentdock_context")
	if !ok {
		t.Fatal("local context definition missing")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err = json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	schemaBytes, err := json.Marshal(definition.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err = json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err = compiler.AddResource("urn:agentdock:local-context", schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("urn:agentdock:local-context")
	if err != nil {
		t.Fatal(err)
	}
	if err = compiled.Validate(normalized); err != nil {
		t.Fatalf("local context violates its advertised schema: %v", err)
	}
}
