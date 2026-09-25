package insertion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnconfirmedSupplementRetriesSameIDWithinBoundedWindow(t *testing.T) {
	s, now, target := fixture(t)
	original := enqueue(t, s, target, "bounded")
	var token string
	for attempt := 1; attempt <= MaxTotalDeliveries; attempt++ {
		*now = now.Add(time.Second)
		if attempt > MaxAutomaticDeliveries {
			if items, err := s.Reserve(context.Background(), target, "not-automatic", *now); err != nil || len(items) != 0 {
				t.Fatal("automatic retry exceeded its budget")
			}
			if _, err := s.Retry(context.Background(), target, original.ID); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Millisecond)
		}
		call := fmt.Sprintf("call_%d", attempt)
		items, err := s.Reserve(context.Background(), target, call, *now)
		if err != nil || len(items) != 1 {
			t.Fatalf("attempt %d: %v %v", attempt, items, err)
		}
		if attempt == 1 {
			token = items[0].ReceiptToken
		}
		if items[0].ID != original.ID || items[0].ReceiptToken != token || items[0].DeliveryAttempts != attempt || !items[0].ExpiresAt.Equal(original.ExpiresAt) {
			t.Fatal("retry changed identity, receipt or deadline")
		}
		finished, err := s.Finish(context.Background(), call, true)
		if err != nil || len(finished) != 1 || finished[0].Status != "delivery_unknown" || finished[0].AcknowledgedAt != nil {
			t.Fatal("inner response was mistaken for acknowledgement")
		}
	}
	*now = now.Add(time.Second)
	if _, err := s.Retry(context.Background(), target, original.ID); err == nil {
		t.Fatal("manual retry exceeded total cap")
	}
	items, err := s.Reserve(context.Background(), target, "never-replayed", *now)
	if err != nil || len(items) != 0 {
		t.Fatal("total cap ignored")
	}
}

func TestReceiptRequiresActualDeliveryOwnershipAndCurrentScope(t *testing.T) {
	s, now, target := fixture(t)
	target.Workspace = "workspace_a"
	original := enqueue(t, s, target, "receipt")
	*now = now.Add(time.Second)
	items, err := s.Reserve(context.Background(), target, "call_actual", *now)
	if err != nil {
		t.Fatal(err)
	}
	valid := Receipt{InsertionID: original.ID, Token: items[0].ReceiptToken}
	if _, err = s.Receipt(context.Background(), target, []Receipt{valid}, "receiver_receipt", ""); !errors.Is(err, ErrReceipt) {
		t.Fatal("unemitted reservation acknowledged")
	}
	if _, err = s.Finish(context.Background(), "call_actual", true); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []Target{
		{Owner: "other", Conversation: target.Conversation, Task: target.Task, Thread: target.Thread, Workspace: target.Workspace},
		{Owner: target.Owner, Conversation: "other", Task: target.Task, Thread: target.Thread, Workspace: target.Workspace},
		{Owner: target.Owner, Conversation: target.Conversation, Task: "other", Thread: target.Thread, Workspace: target.Workspace},
		{Owner: target.Owner, Conversation: target.Conversation, Task: target.Task, Thread: target.Thread, Workspace: "other"},
	} {
		if _, err = s.Receipt(context.Background(), wrong, []Receipt{valid}, "receiver_receipt", ""); !errors.Is(err, ErrReceipt) {
			t.Fatal("foreign scope receipt accepted")
		}
	}
	if _, err = s.Receipt(context.Background(), target, []Receipt{{InsertionID: valid.InsertionID, Token: strings.Repeat("0", 32)}}, "receiver_receipt", ""); !errors.Is(err, ErrReceipt) {
		t.Fatal("forged receipt accepted")
	}
	if _, err = s.Receipt(context.Background(), target, []Receipt{valid, {InsertionID: "missing", Token: valid.Token}}, "receiver_receipt", ""); !errors.Is(err, ErrReceipt) {
		t.Fatal("mixed invalid batch accepted")
	}
	view, _ := s.List(context.Background(), target.Owner, target.Conversation)
	if view[0].Status != "delivery_unknown" || view[0].ReceiptToken != "" {
		t.Fatal("failed batch committed or token leaked into public view")
	}
	*now = now.Add(time.Second)
	if _, err = s.Reserve(context.Background(), target, "call_new", *now); err != nil {
		t.Fatal(err)
	}
	// A receipt for an earlier actual delivery wins over a concurrent retry.
	confirmed, err := s.Receipt(context.Background(), target, []Receipt{valid}, "receiver_receipt", "")
	if err != nil || confirmed[0].AcknowledgedBy != "receiver_receipt" {
		t.Fatal(err)
	}
	at := *confirmed[0].AcknowledgedAt
	if items, err = s.Finish(context.Background(), "call_new", true); err != nil || len(items) != 0 {
		t.Fatal("late retry resurrected acknowledged supplement")
	}
	*now = now.Add(time.Second)
	confirmed, err = s.Receipt(context.Background(), target, []Receipt{valid}, "receiver_receipt", "")
	if err != nil || !confirmed[0].AcknowledgedAt.Equal(at) {
		t.Fatal("duplicate receipt changed terminal evidence")
	}
	if items, err = s.Reserve(context.Background(), target, "call_after_ack", *now); err != nil || len(items) != 0 {
		t.Fatal("confirmed supplement was repeated")
	}
}

func TestHostStagesAndUnconfirmedExpiryRemainTruthful(t *testing.T) {
	s, now, target := fixture(t)
	original := enqueue(t, s, target, "host")
	*now = now.Add(time.Second)
	items, _ := s.Reserve(context.Background(), target, "call_host", *now)
	receipts := []Receipt{{InsertionID: original.ID, Token: items[0].ReceiptToken}}
	host := Transport{HostType: "fixture", OuterCallID: "outer_1", Passthrough: true, ContextAcknowledgement: true}
	items, err := s.FinishForHost(context.Background(), "call_host", true, host)
	if err != nil || items[0].Status != "inner_appended" || items[0].ForwardedAt != nil {
		t.Fatal("inner phase overstated delivery")
	}
	*now = now.Add(time.Second)
	if repeated, err := s.Reserve(context.Background(), target, "too_early", *now); err != nil || len(repeated) != 0 {
		t.Fatal("host receipt wait ignored")
	}
	items, err = s.Receipt(context.Background(), target, receipts, "outer_forwarded", host.OuterCallID)
	if err != nil || items[0].AcknowledgedAt != nil || items[0].Status != "outer_forwarded" {
		t.Fatal("outer forwarding is not context commit")
	}
	*now = original.ExpiresAt
	items, err = s.List(context.Background(), target.Owner, target.Conversation)
	if err != nil || items[0].Status != "delivery_unknown" {
		t.Fatal("missing host commit not exposed")
	}
	if _, err = s.Retry(context.Background(), target, original.ID); err == nil {
		t.Fatal("expired instructions retried")
	}
	items, err = s.Receipt(context.Background(), target, receipts, "host_context_committed", host.OuterCallID)
	if err != nil || items[0].AcknowledgedBy != "host_context_committed" {
		t.Fatal("late actual context receipt lost")
	}
}

func TestLegacyAttachedMigrationNeverPretendsAcknowledged(t *testing.T) {
	s, now, target := fixture(t)
	original := enqueue(t, s, target, "legacy")
	path := filepath.Join(s.root, "queue.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state diskState
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state.SchemaVersion = 1
	state.Items[0].Status = "attached"
	state.Items[0].CallID = "historic_call"
	data, _ = json.Marshal(state)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(s.root, "new_run", func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	items, err := reloaded.List(context.Background(), target.Owner, target.Conversation)
	if err != nil || items[0].ID != original.ID || items[0].Status != "delivery_unknown" || items[0].DeliveryReason != "legacy_inner_response_without_receipt" || items[0].AcknowledgedAt != nil {
		t.Fatal("legacy attachment fabricated receipt")
	}
	*now = now.Add(time.Second)
	if items, err = reloaded.Reserve(context.Background(), target, "new_call", *now); err != nil || len(items) != 0 {
		t.Fatal("old arbitrary instructions were redelivered")
	}
	data, _ = os.ReadFile(path)
	if err = json.Unmarshal(data, &state); err != nil || state.SchemaVersion != 2 {
		t.Fatal("migration not durable")
	}
}
