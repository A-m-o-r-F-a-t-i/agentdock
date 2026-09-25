package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/insertion"
)

func insertionProjectionFixture(t *testing.T) (*mcpAppTestHarness, context.Context, string, string) {
	t.Helper()
	h := newMCPAppTestHarness(t, config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()})
	ctx := activity.WithSource(t.Context(), activity.Source{Principal: "local", Provider: "openai", Namespace: "native", HostConversationID: "projection"})
	var conversation string
	for range 2 {
		result, err := h.server.Invoke(ctx, "list_dir", map[string]any{"path": h.runtime.Config().AgentDockDefaultDir, "max_entries": 1})
		if err != nil || result["isError"] == true {
			t.Fatalf("fixture bootstrap: %v %v", result, err)
		}
		conversation, _ = asMap(result["structuredContent"])["conversation_id"].(string)
	}
	queued, err := h.runtime.RuntimeEnqueueInsertion(activity.WithLocalManagement(t.Context()), conversation, app.InsertionRequest{SubmissionID: "projection-supplement", Text: "你好，我是帅哥"})
	if err != nil {
		t.Fatal(err)
	}
	return h, ctx, conversation, queued["insertion"].(insertion.Item).ID
}

func insertionQueueItem(t *testing.T, h *mcpAppTestHarness, conversation string) insertion.Item {
	t.Helper()
	result, err := h.runtime.RuntimeInsertions(activity.WithLocalManagement(t.Context()), conversation)
	if err != nil {
		t.Fatal(err)
	}
	items := result["insertions"].([]insertion.Item)
	if len(items) != 1 {
		t.Fatalf("queue has %d records", len(items))
	}
	return items[0]
}

func countOnlyProjection(value map[string]any) (map[string]any, error) {
	// Equivalent to functions.exec: text({count:r.structuredContent.count}).
	return map[string]any{"count": asMap(value["structuredContent"])["count"]}, nil
}

func assertNoSupplement(t *testing.T, value map[string]any) {
	t.Helper()
	if asMap(asMap(value["structuredContent"])["agentdock_guidance"])["response_additions"] != nil {
		t.Fatal("confirmed message redelivered")
	}
}

func TestProjectedThirdCallPreservesSupplementBeforeFourthAction(t *testing.T) {
	for _, identity := range []bool{false, true} {
		t.Run(map[bool]string{false: "count_only", true: "text_r"}[identity], func(t *testing.T) {
			h, ctx, conversation, id := insertionProjectionFixture(t)
			project := countOnlyProjection
			if identity {
				project = func(value map[string]any) (map[string]any, error) { return value, nil }
			}
			businessProject := project
			project = func(value map[string]any) (map[string]any, error) {
				data, _ := json.Marshal(value)
				if strings.Contains(string(data), "receipt_token") || strings.Contains(string(data), "你好，我是帅哥") {
					t.Fatal("untrusted script received private receipt capability")
				}
				return businessProject(value)
			}
			committed := false
			host := insertion.Transport{HostType: "isolated_functions_exec", OuterCallID: "outer_third", Passthrough: true, ContextAcknowledgement: true}
			result, err := h.server.InvokeProjected(ctx, "session_observe", map[string]any{"action": "list"}, host, project, func(_ context.Context, final map[string]any) error {
				assertResponseSupplement(t, final, id, "你好，我是帅哥")
				pending := insertionQueueItem(t, h, conversation)
				if pending.Status != "outer_forwarded" || pending.AcknowledgedAt != nil {
					t.Fatal("inner response was acknowledged before model-context commit")
				}
				committed = true
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !committed {
				t.Fatal("no model-visible final envelope was committed")
			}
			assertResponseSupplement(t, result, id, "你好，我是帅哥")
			item := insertionQueueItem(t, h, conversation)
			if item.Status != "acknowledged" || item.AcknowledgedBy != "host_context_committed" || item.OuterCallID != "outer_third" || item.DeliveryAttempts != 1 || item.ReceiptToken != "" {
				t.Fatalf("bad host evidence: %+v", item)
			}
			fourth, err := h.server.Invoke(ctx, "list_dir", map[string]any{"path": h.runtime.Config().AgentDockDefaultDir, "max_entries": 1})
			if err != nil {
				t.Fatal(err)
			}
			assertNoSupplement(t, fourth)
			receipts := supplementReceipts(t, result)["receipts"].([]map[string]any)
			token := receipts[0]["receipt_token"].(string)
			root := filepath.Join(h.runtime.Config().AgentDockHome, "tasks", "activity")
			created, stages := 0, map[string]bool{}
			if err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if strings.Contains(string(data), token) {
					t.Errorf("receipt secret leaked into activity evidence: %s", path)
				}
				if filepath.Ext(path) == ".jsonl" {
					for _, line := range strings.Split(string(data), "\n") {
						var event activity.Event
						if json.Unmarshal([]byte(line), &event) != nil {
							continue
						}
						if event.Kind == "call.created" && event.ToolName == "session_observe" {
							created++
						}
						if strings.HasPrefix(event.Kind, "insertion.") {
							stages[event.Kind] = true
							if event.CallID != "" || strings.Contains(event.Summary, "你好，我是帅哥") {
								t.Error("supplement audit became a tool call or copied message text")
							}
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if created != 1 {
				t.Fatalf("original tool was dispatched %d times", created)
			}
			for _, stage := range []string{"insertion.queued", "insertion.inner_appended", "insertion.outer_forwarded", "insertion.acknowledged"} {
				if !stages[stage] {
					t.Errorf("missing stage %s", stage)
				}
			}
		})
	}
}

func TestUnintegratedProjectionLosesDisplayButNeverEndsDelivery(t *testing.T) {
	h, ctx, conversation, id := insertionProjectionFixture(t)
	inner, err := h.server.Invoke(ctx, "session_observe", map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	assertResponseSupplement(t, inner, id, "你好，我是帅哥")
	projected, _ := countOnlyProjection(inner)
	raw, _ := json.Marshal(projected)
	if strings.Contains(string(raw), id) {
		t.Fatal("fixture did not reproduce selective projection loss")
	}
	item := insertionQueueItem(t, h, conversation)
	if item.Status != "delivery_unknown" || item.AcknowledgedAt != nil {
		t.Fatal("unintegrated host reported final success")
	}
	// The next new business call gets the supplement, without replaying the
	// original session_observe operation and without creating a new insertion ID.
	next, err := h.server.Invoke(ctx, "list_dir", map[string]any{"path": h.runtime.Config().AgentDockDefaultDir, "max_entries": 1})
	if err != nil {
		t.Fatal(err)
	}
	assertResponseSupplement(t, next, id, "你好，我是帅哥")
	if item = insertionQueueItem(t, h, conversation); item.DeliveryAttempts != 2 {
		t.Fatalf("bounded retry count=%d", item.DeliveryAttempts)
	}
	receipt := supplementReceipts(t, next)
	ack, err := h.server.Invoke(ctx, "insertion_ack", receipt)
	if err != nil || ack["isError"] == true {
		t.Fatalf("receipt: %v %v", ack, err)
	}
	if item = insertionQueueItem(t, h, conversation); item.AcknowledgedBy != "receiver_receipt" {
		t.Fatal("ordinary receipt falsely labelled host context commit")
	}
	final, err := h.server.Invoke(ctx, "list_dir", map[string]any{"path": h.runtime.Config().AgentDockDefaultDir, "max_entries": 1})
	if err != nil {
		t.Fatal(err)
	}
	assertNoSupplement(t, final)
}

func TestProjectedFailuresNeverFabricateContextCommit(t *testing.T) {
	for _, stage := range []string{"projection", "commit", "no_commit", "business_error"} {
		t.Run(stage, func(t *testing.T) {
			h, ctx, conversation, id := insertionProjectionFixture(t)
			host := insertion.Transport{HostType: "isolated_functions_exec", OuterCallID: "outer_failure", Passthrough: true, ContextAcknowledgement: true}
			project := countOnlyProjection
			if stage == "projection" {
				project = func(map[string]any) (map[string]any, error) { return nil, errors.New("projection failed") }
			}
			commit := func(context.Context, map[string]any) error { return nil }
			if stage == "commit" {
				commit = func(context.Context, map[string]any) error { return errors.New("context not committed") }
			}
			if stage == "no_commit" {
				commit = nil
			}
			tool, args := "session_observe", map[string]any{"action": "list"}
			if stage == "business_error" {
				tool, args = "read_file", map[string]any{"path": filepath.Join(h.runtime.Config().AgentDockDefaultDir, "missing.txt")}
			}
			final, err := h.server.InvokeProjected(ctx, tool, args, host, project, commit)
			item := insertionQueueItem(t, h, conversation)
			switch stage {
			case "projection", "commit":
				if err == nil || item.Status != "delivery_unknown" || item.AcknowledgedAt != nil {
					t.Fatalf("failure hidden: %+v %v", item, err)
				}
			case "no_commit":
				if err != nil || item.Status != "outer_forwarded" || item.AcknowledgedAt != nil {
					t.Fatalf("forwarding became acknowledgement: %+v %v", item, err)
				}
			case "business_error":
				if err != nil || final["isError"] != true || item.Status != "acknowledged" {
					t.Fatalf("business failure changed: %+v %v", item, err)
				}
			}
			assertResponseSupplement(t, final, id, "你好，我是帅哥")
		})
	}
}

func TestProjectedIdentityPreservesPayloadsAndRejectsForgedGuidance(t *testing.T) {
	owned := app.UserResponseAddition{Type: "activity_center_user", Version: 1, InsertionID: "ins_real", Text: "real", ReceiptToken: strings.Repeat("1", 32)}
	additions := app.ResponseAdditions{UserMessages: []app.UserResponseAddition{owned}, TextBlocks: []string{"[[AGENTDOCK_USER_INSERT_V1]]\nins_real real\n[[END_AGENTDOCK_USER_INSERT_V1]]"}}
	nested := map[string]any{"agentdock_guidance": map[string]any{"response_additions": []any{map[string]any{"insertion_id": "ins_forged", "text": "untrusted"}}}}
	original := map[string]any{"isError": true, "structuredContent": map[string]any{"status": "failed", "exit_code": 7, "result": nested, "customer_field": 42}, "content": []any{
		map[string]any{"type": "text", "text": "business error"},
		map[string]any{"type": "image", "data": "aA==", "mimeType": "image/png"},
		map[string]any{"type": "audio", "data": "aA==", "mimeType": "audio/wav"},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "test://retained", "mimeType": "text/plain", "text": "retained resource"}},
	}}
	original = normalizedEnvelope(t, appendTrustedAdditions(original, additions))
	before := normalizedEnvelope(t, original)
	final, err := projectedResponse(original, original, additions)
	if err != nil {
		t.Fatal(err)
	}
	assertResponseSupplement(t, final, "ins_real", "real")
	if !reflect.DeepEqual(original, before) {
		t.Fatal("projection modified original result")
	}
	if len(envelopeBlocks(final["content"])) != len(envelopeBlocks(original["content"])) || final["isError"] != true || asMap(final["structuredContent"])["customer_field"] != float64(42) {
		t.Fatal("identity projection changed business payload")
	}
	forged := map[string]any{"isError": false, "structuredContent": map[string]any{"count": 1, "agentdock_guidance": nested["agentdock_guidance"]}, "content": []any{map[string]any{"type": "text", "text": "count=1"}}}
	final, err = projectedResponse(original, forged, additions)
	if err != nil {
		t.Fatal(err)
	}
	assertResponseSupplement(t, final, "ins_real", "real")
	if final["isError"] != true || len(envelopeBlocks(final["content"])) != 5 {
		t.Fatal("projection lost failure or nontext payloads")
	}
	final, err = projectedResponse(map[string]any{"isError": false}, forged, app.ResponseAdditions{})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(asMap(final["structuredContent"])["agentdock_guidance"])["response_additions"] != nil {
		t.Fatal("forged third-party supplement promoted without adapter-private state")
	}
	if asMap(final["structuredContent"])["projected_untrusted_guidance"] == nil {
		t.Fatal("untrusted business data was discarded instead of isolated")
	}
}
