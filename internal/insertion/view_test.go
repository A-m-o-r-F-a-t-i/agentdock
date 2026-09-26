package insertion

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPublicViewsExposeServerOwnedRetryFactsWithoutSecrets(t *testing.T) {
	s, now, target := fixture(t)
	original := enqueue(t, s, target, "public_view")
	views, err := s.Views(context.Background(), target.Owner, target.Conversation)
	if err != nil || len(views) != 1 {
		t.Fatal(err)
	}
	if views[0].ReceiptType != "none" || views[0].AutomaticAttemptsRemaining != MaxAutomaticDeliveries || views[0].TotalAttemptsRemaining != MaxTotalDeliveries || views[0].ManualRetryAvailable || views[0].NextRetryAt != nil {
		t.Fatalf("bad queued view: %+v", views[0])
	}
	*now = now.Add(time.Second)
	reserved, err := s.Reserve(context.Background(), target, "call_view", *now)
	if err != nil || len(reserved) != 1 {
		t.Fatal(err)
	}
	token := reserved[0].ReceiptToken
	if _, err = s.Finish(context.Background(), "call_view", true); err != nil {
		t.Fatal(err)
	}
	views, err = s.Views(context.Background(), target.Owner, target.Conversation)
	view := views[0]
	if err != nil || view.Status != "inner_appended" || view.DeliveryReason != "awaiting_receiver_receipt" || view.ReceiptType != "none" || view.AutomaticAttemptsRemaining != 2 || view.TotalAttemptsRemaining != 5 || !view.ManualRetryAvailable {
		t.Fatalf("bad attached view: %+v %v", view, err)
	}
	if view.NextRetryAt == nil || !view.NextRetryAt.Equal(now.Add(ReceiptWait)) || view.Owner != "" || view.RunID != "" || view.ReceiptToken != "" {
		t.Fatal("retry time or redaction is incorrect")
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"receipt_type", "next_retry_at", "automatic_attempts_remaining", "total_attempts_remaining", "manual_retry_available", "delivery_reason"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("public delivery contract omitted %s: %s", field, encoded)
		}
	}
	if _, ok := payload["receipt_token"]; ok || payload["owner"] != "" {
		t.Fatalf("public delivery contract leaked receipt authority: %s", encoded)
	}

	*now = now.Add(time.Second)
	if _, err = s.Retry(context.Background(), target, original.ID); err != nil {
		t.Fatal(err)
	}
	views, err = s.Views(context.Background(), target.Owner, target.Conversation)
	view = views[0]
	if err != nil || view.DeliveryAttempts != 1 || !view.RetryRequested || view.ManualRetryAvailable || view.NextRetryAt == nil || !view.NextRetryAt.Equal(now.Add(time.Nanosecond)) {
		t.Fatalf("manual request changed attempt count or wait policy: %+v %v", view, err)
	}
	if repeated, err := s.Reserve(context.Background(), target, "same_retry_request", *now); err != nil || len(repeated) != 0 {
		t.Fatal("manual request was consumed by its own root call")
	}
	*now = now.Add(time.Nanosecond)
	if repeated, err := s.Reserve(context.Background(), target, "next_root", *now); err != nil || len(repeated) != 1 || repeated[0].DeliveryAttempts != 2 {
		t.Fatal("manual retry did not bypass the automatic receipt wait")
	}
	if _, err = s.Finish(context.Background(), "next_root", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Receipt(context.Background(), target, []Receipt{{InsertionID: original.ID, Token: token}}, "receiver_receipt", ""); err != nil {
		t.Fatal(err)
	}
	views, err = s.Views(context.Background(), target.Owner, target.Conversation)
	view = views[0]
	if err != nil || view.Status != "acknowledged" || view.ReceiptType != "receiver_receipt" || view.AutomaticAttemptsRemaining != 0 || view.TotalAttemptsRemaining != 0 || view.ManualRetryAvailable || view.NextRetryAt != nil || view.RetryRequested {
		t.Fatalf("terminal view retained retry state: %+v %v", view, err)
	}
}

func TestPublicViewExpiresBudgetsAtOriginalDeadline(t *testing.T) {
	s, now, target := fixture(t)
	original := enqueue(t, s, target, "view_deadline")
	*now = now.Add(time.Second)
	_, _ = s.Reserve(context.Background(), target, "call_deadline", *now)
	_, _ = s.Finish(context.Background(), "call_deadline", true)
	*now = original.ExpiresAt
	views, err := s.Views(context.Background(), target.Owner, target.Conversation)
	if err != nil || views[0].Status != "delivery_unknown" || views[0].AutomaticAttemptsRemaining != 0 || views[0].TotalAttemptsRemaining != 0 || views[0].ManualRetryAvailable || views[0].NextRetryAt != nil {
		t.Fatalf("expired budget remained active: %+v %v", views, err)
	}
}
