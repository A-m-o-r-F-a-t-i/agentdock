package insertion

import (
	"context"
	"time"
)

// PublicItem is the management/read contract for a supplement. It keeps the
// persisted queue schema private while centralizing retry budgets and receipt
// evidence for every client. Receipt secrets are removed by Public before this
// value leaves the store.
type PublicItem struct {
	Item
	ReceiptType                string     `json:"receipt_type"`
	NextRetryAt                *time.Time `json:"next_retry_at,omitempty"`
	AutomaticAttemptsRemaining int        `json:"automatic_attempts_remaining"`
	TotalAttemptsRemaining     int        `json:"total_attempts_remaining"`
	ManualRetryAvailable       bool       `json:"manual_retry_available"`
}

func attemptsRemaining(limit, attempts int) int {
	if attempts >= limit {
		return 0
	}
	return limit - attempts
}

// Present derives public facts from one record at a caller-supplied server time.
// Pending and reserved records expose budgets, but only an actually issued,
// unconfirmed record can offer manual redelivery.
func Present(item Item, now time.Time) PublicItem {
	raw := item
	view := PublicItem{Item: Public(item), ReceiptType: "none"}
	if raw.Status == "acknowledged" {
		view.ReceiptType = raw.AcknowledgedBy
		return view
	}
	if raw.Status == "outer_forwarded" {
		view.ReceiptType = "outer_forwarded"
	}
	active := raw.Status == "pending" || raw.Status == "reserved" || unconfirmed(raw.Status)
	if !active || !now.Before(raw.ExpiresAt) {
		return view
	}
	// A migrated or malformed unconfirmed record without an issued receipt token
	// cannot be replayed, so it must not advertise arithmetic budget as usable.
	if unconfirmed(raw.Status) && (raw.DeliveryAttempts == 0 || raw.ReceiptToken == "") {
		return view
	}
	view.AutomaticAttemptsRemaining = attemptsRemaining(MaxAutomaticDeliveries, raw.DeliveryAttempts)
	view.TotalAttemptsRemaining = attemptsRemaining(MaxTotalDeliveries, raw.DeliveryAttempts)
	if !unconfirmed(raw.Status) || view.TotalAttemptsRemaining == 0 {
		return view
	}
	view.ManualRetryAvailable = !raw.RetryRequested
	if raw.RetryRequested {
		next := raw.UpdatedAt.Add(time.Nanosecond)
		view.NextRetryAt = &next
	} else if view.AutomaticAttemptsRemaining > 0 {
		if raw.RetryAfter != nil {
			next := *raw.RetryAfter
			view.NextRetryAt = &next
		} else {
			next := raw.UpdatedAt.Add(time.Nanosecond)
			view.NextRetryAt = &next
		}
	}
	return view
}

// Views returns redacted records and derived delivery facts from one atomic
// queue snapshot. The exact same clock sample drives expiry and retry fields.
func (s *Store) Views(ctx context.Context, owner, conversation string) ([]PublicItem, error) {
	result := []PublicItem{}
	err := s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		dirty := expire(state, now)
		for _, item := range state.Items {
			if item.Owner == owner && item.Conversation == conversation {
				result = append(result, Present(item, now))
			}
		}
		return dirty, nil
	})
	return result, err
}
