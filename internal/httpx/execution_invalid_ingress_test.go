package httpx

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/activity"
)

func TestExecutionSDKUnknownToolRetainsFailedCall(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	first := f.call(t, "unknown-tool-host", "agentdock_context", nil)
	conversation := first["conversation_id"].(string)
	_, err := f.session.CallTool(context.Background(), &sdk.CallToolParams{
		Meta: sdk.Meta{"openai/session": "unknown-tool-host"},
		Name: "missing_execution_fixture", Arguments: map[string]any{},
	})
	if err == nil {
		t.Fatal("SDK accepted an unavailable tool")
	}
	page, err := f.runtime.ActivityJournal().Calls(context.Background(), activity.CallQuery{ConversationID: conversation, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, call := range page.Calls {
		if call.ToolName == "missing_execution_fixture" {
			count++
			if call.Status != "failed" || call.BindingQuality != "host_metadata" || call.CallID == "" {
				t.Fatalf("invalid failed ingress call: %+v", call)
			}
		}
	}
	if count != 1 {
		t.Fatalf("SDK rejected tool should retain exactly one failed Call, got %d", count)
	}
}
