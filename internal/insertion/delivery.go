package insertion

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"
)

const (
	MaxAutomaticDeliveries = 3
	MaxTotalDeliveries     = 6
	ReceiptWait            = 30 * time.Second
)

var ErrReceipt = errors.New("insertion receipt does not match a delivered message in the current scope")

// Transport is adapter-owned context, never decoded from business tool output.
type Transport struct {
	HostType               string `json:"host_type"`
	OuterCallID            string `json:"outer_call_id"`
	Passthrough            bool   `json:"passthrough"`
	ContextAcknowledgement bool   `json:"context_acknowledgement"`
}

type Receipt struct {
	InsertionID string `json:"insertion_id"`
	Token       string `json:"receipt_token"`
}

func Public(item Item) Item {
	item.Owner, item.RunID, item.ReceiptToken = "", "", ""
	return item
}

func unconfirmed(status string) bool {
	return status == "inner_appended" || status == "outer_forwarded" || status == "delivery_unknown"
}

func sameTarget(item, current Target) bool {
	return item.Owner == current.Owner && item.Conversation == current.Conversation && item.Task == current.Task && item.Thread == current.Thread && (item.Workspace == "" || item.Workspace == current.Workspace)
}

func canRedeliver(item Item, received time.Time) bool {
	return unconfirmed(item.Status) && item.DeliveryAttempts > 0 && item.ReceiptToken != "" && item.DeliveryAttempts < MaxTotalDeliveries &&
		(item.DeliveryAttempts < MaxAutomaticDeliveries || item.RetryRequested) && received.After(item.UpdatedAt) && received.Before(item.ExpiresAt) &&
		(item.RetryAfter == nil || !received.Before(*item.RetryAfter))
}

// Receipt validates the entire batch before changing any record. A token from
// an actually appended supplement is necessary; knowledge of its public ID is
// insufficient. Older successful delivery attempts can confirm a later retry.
func (s *Store) Receipt(ctx context.Context, target Target, receipts []Receipt, by, outerID string) ([]Item, error) {
	if len(receipts) < 1 || len(receipts) > MaxPending || (by != "receiver_receipt" && by != "host_context_committed" && by != "outer_forwarded") || len(outerID) > 160 {
		return nil, ErrReceipt
	}
	result := []Item{}
	err := s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		indices := make([]int, 0, len(receipts))
		seen := map[string]string{}
		for _, receipt := range receipts {
			if previous, ok := seen[receipt.InsertionID]; ok {
				if previous != receipt.Token {
					return false, ErrReceipt
				}
				continue
			}
			seen[receipt.InsertionID] = receipt.Token
			found := -1
			for index, item := range state.Items {
				if item.ID == receipt.InsertionID && sameTarget(item.Target, target) && item.InnerAppendedAt != nil && item.ReceiptToken != "" &&
					subtle.ConstantTimeCompare([]byte(item.ReceiptToken), []byte(receipt.Token)) == 1 && item.Status != "cancelled" && item.Status != "target_changed" {
					found = index
					break
				}
			}
			if found < 0 {
				return false, ErrReceipt
			}
			indices = append(indices, found)
		}
		dirty := false
		for _, index := range indices {
			item := &state.Items[index]
			if item.Status == "acknowledged" {
				result = append(result, Public(*item))
				continue
			}
			if by == "outer_forwarded" {
				if item.ForwardedAt != nil && item.OuterCallID == outerID {
					result = append(result, Public(*item))
					continue
				}
				item.Status, item.ForwardedAt = "outer_forwarded", &now
				item.DeliveryReason = "awaiting_context_commit"
			} else {
				item.Status, item.AcknowledgedAt, item.AcknowledgedBy = "acknowledged", &now, by
				item.DeliveryReason = ""
				item.RetryRequested, item.RetryAfter = false, nil
			}
			if outerID != "" {
				item.OuterCallID = outerID
			}
			item.UpdatedAt = now
			dirty = true
			result = append(result, Public(*item))
		}
		return dirty, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Retry makes only the same supplement eligible for the next NEW root request.
// It neither extends the deadline nor changes scope nor executes any command.
func (s *Store) Retry(ctx context.Context, target Target, id string) (Item, error) {
	var result Item
	err := s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		for index := range state.Items {
			item := &state.Items[index]
			if item.ID != id || !sameTarget(item.Target, target) {
				continue
			}
			if !unconfirmed(item.Status) || !now.Before(item.ExpiresAt) || item.DeliveryAttempts >= MaxTotalDeliveries || item.DeliveryAttempts == 0 || item.ReceiptToken == "" {
				return false, errors.New("supplement cannot be redelivered: expired, unissued, confirmed, or attempt limit reached")
			}
			if item.RetryRequested {
				result = Public(*item)
				return false, nil
			}
			item.RetryRequested = true
			item.RetryAfter = nil
			item.UpdatedAt = now
			result = Public(*item)
			return true, nil
		}
		return false, ErrNotFound
	})
	return result, err
}
