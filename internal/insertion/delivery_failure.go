package insertion

import (
	"context"
	"time"
)

// An outer failure only changes unconfirmed messages from this inner call.
// A late failure never revokes a real receipt or overwrites a newer attempt.
func (s *Store) DeliveryFailed(ctx context.Context, callID, reason string) ([]Item, error) {
	if reason != "outer_projection_failed" && reason != "context_commit_failed" {
		return nil, ErrReceipt
	}
	items := []Item{}
	err := s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		for index := range state.Items {
			item := &state.Items[index]
			if item.CallID != callID || !unconfirmed(item.Status) {
				continue
			}
			item.Status, item.DeliveryReason, item.UpdatedAt = "delivery_unknown", reason, now
			item.RetryAfter = nil
			items = append(items, Public(*item))
		}
		return len(items) > 0, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}
