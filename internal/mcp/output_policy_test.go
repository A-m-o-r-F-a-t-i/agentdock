package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/insertion"
)

func TestOutputBudgetBothAdaptersPreserveCopiesAndInsertion(t *testing.T) {
	for _, adapter := range []string{"SDK", "Invoke"} {
		t.Run(adapter, func(t *testing.T) {
			h := newMCPAppTestHarness(t, config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()})
			_, err := h.runtime.RuntimeUpdateDisplaySettings(t.Context(), config.DisplayChange{ExpectedRevision: h.runtime.MCPPresentationSettings().Revision, ToolOutput: &config.ToolOutputSettings{Enabled: true, MaxChars: 1000}})
			if err != nil {
				t.Fatal(err)
			}
			host := "budget-" + adapter
			ctx := activity.WithSource(t.Context(), activity.Source{Principal: "local", Provider: "openai", Namespace: "native", HostConversationID: host})
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				if adapter == "Invoke" {
					result, err := h.server.Invoke(ctx, name, args)
					if err != nil {
						t.Fatal(err)
					}
					return normalizedEnvelope(t, result)
				}
				result, err := h.session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: args, Meta: mcpsdk.Meta{"openai/session": host}})
				if err != nil {
					t.Fatal(err)
				}
				return normalizedEnvelope(t, result)
			}
			root := h.runtime.Config().AgentDockDefaultDir
			body := strings.Repeat("中😀e\u0301\r\n", 500)
			path := filepath.Join(root, "large-output.txt")
			if err = os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			first := call("list_dir", map[string]any{"path": root, "max_entries": 1})
			conversation, _ := asMap(first["structuredContent"])["conversation_id"].(string)
			if conversation == "" {
				t.Fatal("missing conversation")
			}
			local := activity.WithLocalManagement(t.Context())
			message := strings.Repeat("保留用户补充", 240)
			queued, err := h.runtime.RuntimeEnqueueInsertion(local, conversation, app.InsertionRequest{SubmissionID: "budget-user-insertion", Text: message})
			if err != nil {
				t.Fatal(err)
			}
			id := queued["insertion"].(insertion.PublicItem).ID
			envelope := call("read_file", map[string]any{"path": path})
			assertResponseSupplement(t, envelope, id, message)
			structured := asMap(envelope["structuredContent"])
			visible, _ := structured["content"].(string)
			if utf8.RuneCountInString(visible) != 1000 || !strings.HasPrefix(body, visible) || envelope["isError"] == true {
				t.Fatal("business preview budget or status changed")
			}
			blocks := envelope["content"].([]any)
			business, _ := asMap(blocks[0])["text"].(string)
			var copy map[string]any
			if err = json.Unmarshal([]byte(business), &copy); err != nil {
				t.Fatal(err)
			}
			if copy["content"] != visible {
				t.Fatal("content.text bypassed the structured preview budget")
			}
			policy := asMap(structured["output_policy"])
			continuation := asMap(asMap(policy["continue_read"])["arguments"])
			if continuation["path"] == nil {
				t.Fatal("adapter lost retained source continuation")
			}
			ack := call("insertion_ack", supplementReceipts(t, envelope))
			if ack["isError"] == true {
				t.Fatalf("supplement receipt failed: %v", ack)
			}
			page := call("read_file", continuation)
			pageData := asMap(page["structuredContent"])
			pageBody, _ := pageData["content"].(string)
			if page["isError"] == true || utf8.RuneCountInString(pageBody) > 1000 {
				t.Fatal("adapter could not read its source reference")
			}
			if asMap(pageData["agentdock_guidance"])["response_additions"] != nil {
				t.Fatal("source read claimed insertion twice")
			}
			callID, _ := structured["call_id"].(string)
			replayed, err := activity.New(filepath.Join(h.runtime.Config().AgentDockHome, "tasks", "activity"), activity.Options{})
			if err != nil {
				t.Fatal(err)
			}
			detail, err := replayed.Call(local, callID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.OutputSource == nil || detail.OutputSource.Ref == "" {
				t.Fatal("transport journaling lost retained source")
			}
		})
	}
}
