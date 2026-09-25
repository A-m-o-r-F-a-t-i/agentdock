package mcp

import (
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/insertion"
	"testing"
	"time"
)

func TestSDKInitializedHostReceiptsAndCapabilityRefusal(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "unnegotiated", true: "initialized"}[supported], func(t *testing.T) {
			h := newMCPAppTestHarness(t, config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()})
			experimental := map[string]any{}
			if supported {
				experimental[insertionCapability] = map[string]any{"passthrough": true, "context_acknowledgement": true}
			}
			serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
			done := make(chan error, 1)
			runCtx := activity.WithSource(t.Context(), activity.Source{Principal: "metadata-client"})
			go func() { done <- h.server.sdk.Run(runCtx, serverTransport) }()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "initialized-host", Version: "1"}, &mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{Experimental: experimental}})
			session, err := client.Connect(t.Context(), clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = session.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("SDK fixture did not close")
				}
			})
			call := func(tool string, args map[string]any, outer string, receipt any) (map[string]any, error) {
				meta := mcpsdk.Meta{"openai/session": "initialized-host-test", insertionCapability: map[string]any{"outer_call_id": outer, "passthrough": true}}
				if receipt != nil {
					meta[insertionHostReceipts] = receipt
				}
				result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: tool, Arguments: args, Meta: meta})
				if err != nil {
					return nil, err
				}
				return normalizedEnvelope(t, result), nil
			}
			first, err := call("list_dir", map[string]any{"path": h.runtime.Config().AgentDockDefaultDir, "max_entries": 1}, "first", nil)
			if err != nil {
				t.Fatal(err)
			}
			conversation, _ := asMap(first["structuredContent"])["conversation_id"].(string)
			queued, err := h.runtime.RuntimeEnqueueInsertion(activity.WithLocalManagement(t.Context()), conversation, app.InsertionRequest{SubmissionID: "session-receipt", Text: "你好，我是帅哥"})
			if err != nil {
				t.Fatal(err)
			}
			id := queued["insertion"].(insertion.Item).ID
			delivered, err := call("session_observe", map[string]any{"action": "list"}, "delivery-outer", nil)
			if err != nil {
				t.Fatal(err)
			}
			assertResponseSupplement(t, delivered, id, "你好，我是帅哥")
			item := insertionQueueItem(t, h, conversation)
			expected := "delivery_unknown"
			if supported {
				expected = "inner_appended"
			}
			if item.Status != expected || item.AcknowledgedAt != nil {
				t.Fatalf("wrong initialized stage: %+v", item)
			}
			receipt := map[string]any{"stage": "outer_forwarded", "outer_call_id": "delivery-outer", "receipts": supplementReceipts(t, delivered)["receipts"]}
			forwarded, err := call("session_observe", map[string]any{"action": "list"}, "forward-receipt", receipt)
			if !supported {
				if err == nil {
					t.Fatalf("uninitialized capability was accepted: %v", forwarded)
				}
				if item = insertionQueueItem(t, h, conversation); item.Status != "delivery_unknown" || item.AcknowledgedAt != nil {
					t.Fatal("rejected metadata changed delivery state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if item = insertionQueueItem(t, h, conversation); item.Status != "outer_forwarded" || item.HostType != "initialized-host" || item.AcknowledgedAt != nil {
				t.Fatalf("forwarding overclaimed context commit: %+v", item)
			}
			receipt["stage"] = "context_committed"
			committed, err := call("session_observe", map[string]any{"action": "list"}, "context-receipt", receipt)
			if err != nil {
				t.Fatal(err)
			}
			assertNoSupplement(t, committed)
			if item = insertionQueueItem(t, h, conversation); item.Status != "acknowledged" || item.AcknowledgedBy != "host_context_committed" || item.OuterCallID != "delivery-outer" {
				t.Fatalf("host receipt lost: %+v", item)
			}
			again, err := call("session_observe", map[string]any{"action": "list"}, "last", receipt)
			if err != nil {
				t.Fatal(err)
			}
			assertNoSupplement(t, again)
		})
	}
}

func TestBusinessArgumentsCannotDeclareReceiptCapability(t *testing.T) {
	h, ctx, conversation, id := insertionProjectionFixture(t)
	result, err := h.server.Invoke(ctx, "session_observe", map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	assertResponseSupplement(t, result, id, "你好，我是帅哥")
	host := app.InsertionTransportFromContext(ctx)
	if host.Passthrough || host.ContextAcknowledgement {
		t.Fatal("ordinary result elevated transport capability")
	}
	receipts := supplementReceipts(t, result)
	receipts["evidence"] = "host_context_committed"
	invalid, err := h.server.Invoke(ctx, "insertion_ack", receipts)
	if err == nil && invalid["isError"] != true {
		t.Fatal("business argument could choose its evidence authority")
	}
	if item := insertionQueueItem(t, h, conversation); item.Status != "delivery_unknown" || item.AcknowledgedAt != nil {
		t.Fatal("invalid receiver request acknowledged message")
	}
}
