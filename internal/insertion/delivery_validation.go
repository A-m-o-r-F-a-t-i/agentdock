package insertion

import "encoding/hex"

func validDeliveryRecord(item Item) bool {
	switch item.Status {
	case "pending", "target_changed", "expired", "cancelled", "reserved", "attached", "inner_appended", "outer_forwarded", "delivery_unknown", "acknowledged":
	default:
		return false
	}
	if item.DeliveryAttempts < 0 || item.DeliveryAttempts > MaxTotalDeliveries || len(item.HostType) > 80 || len(item.OuterCallID) > 160 || len(item.DeliveryReason) > 160 {
		return false
	}
	if item.ReceiptToken != "" {
		raw, err := hex.DecodeString(item.ReceiptToken)
		if err != nil || len(raw) != 16 {
			return false
		}
	}
	if item.Status == "acknowledged" {
		return item.InnerAppendedAt != nil && item.AcknowledgedAt != nil && item.DeliveryAttempts > 0 && item.ReceiptToken != "" && (item.AcknowledgedBy == "receiver_receipt" || item.AcknowledgedBy == "host_context_committed")
	}
	return item.AcknowledgedAt == nil && item.AcknowledgedBy == ""
}
