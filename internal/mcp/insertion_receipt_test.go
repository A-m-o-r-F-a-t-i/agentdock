package mcp

import "testing"

func supplementReceipts(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	value := normalizedEnvelope(t, envelope)
	messages, _ := asMap(asMap(value["structuredContent"])["agentdock_guidance"])["response_additions"].([]any)
	if len(messages) == 0 {
		t.Fatal("no supplement to acknowledge")
	}
	receipts := make([]map[string]any, 0, len(messages))
	for _, raw := range messages {
		item := asMap(raw)
		if token, _ := item["receipt_token"].(string); len(token) != 32 {
			t.Fatal("receipt token missing")
		}
		receipts = append(receipts, map[string]any{"insertion_id": item["insertion_id"], "receipt_token": item["receipt_token"]})
	}
	return map[string]any{"receipts": receipts}
}
