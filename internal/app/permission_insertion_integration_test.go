package app

import (
	"context"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/insertion"
	"github.com/uvwt/agentdock/internal/permission"
)

func TestRestrictedProfileAcknowledgesOnlyReceivedControlSupplement(t *testing.T) {
	r := executionTestRuntime(t)
	host := scopeHost("restricted-insertion")
	boot, err := r.Call(host, "agentdock_context", nil)
	if err != nil {
		t.Fatal(err)
	}
	conversation := stringArg(boot, "conversation_id")
	local := activity.WithLocalManagement(context.Background())
	queued, err := r.RuntimeEnqueueInsertion(local, conversation, InsertionRequest{SubmissionID: "restricted-receipt", Text: "keep new requirements"})
	if err != nil {
		t.Fatal(err)
	}
	settings := permission.DefaultSettings()
	settings.Profile = permission.Profile{Filesystem: permission.Deny, Network: permission.Deny, SandboxBoundary: permission.BoundaryWorkspace}
	settings.Approval.Mode = permission.Never
	if _, err = r.permissions.Update(host, permission.Change{Scope: "global", Mode: permission.Full, ConfirmFull: true, ExpectedRevision: 1, Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	ctx, response := BeginToolResponse(host)
	if _, err = r.Call(ctx, "session_observe", map[string]any{"action": "list"}); err != nil {
		t.Fatal(err)
	}
	r.FinishToolResponse(ctx, response, true)
	messages := response.CompletedAdditions().UserMessages
	if len(messages) != 1 || messages[0].InsertionID != queued["insertion"].(insertion.PublicItem).ID {
		t.Fatal("scoped supplement not delivered")
	}
	args := map[string]any{"receipts": []map[string]any{{"insertion_id": messages[0].InsertionID, "receipt_token": messages[0].ReceiptToken}}}
	if result, err := r.Call(host, "insertion_ack", args); err != nil || result["evidence"] != "receiver_receipt" {
		t.Fatalf("control-plane acknowledgement denied: %v %v", result, err)
	}
	view, err := r.RuntimeInsertions(local, conversation)
	if err != nil || view["insertions"].([]insertion.PublicItem)[0].Status != "acknowledged" {
		t.Fatal("receipt not durable")
	}
	if _, err = r.Call(host, "exec_command", map[string]any{"cmd": "echo should-not-run"}); err == nil {
		t.Fatal("acknowledgement expanded permission profile")
	}
	if _, err = r.Call(host, "file_edit", map[string]any{"action": "add", "path": "must-not-exist.txt", "content": "x"}); err == nil {
		t.Fatal("receipt admitted unrelated filesystem writes")
	}
}
