package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/app"
)

func TestCatalogResponseOrderAndBusinessFailure(t *testing.T) {
	catalog := map[string]any{"server": "test", "catalog_revision": "v1", "complete": true, "total": 2, "tools": []map[string]any{{"name": "test:first", "description": "First."}, {"name": "test:second", "description": "Second."}}}
	for _, failure := range []string{"success", "business", "transport"} {
		t.Run(failure, func(t *testing.T) {
			payload := map[string]any{"mcp_catalog": catalog, "result": map[string]any{"isError": failure == "business", "content": []any{map[string]any{"type": "text", "text": "original"}, map[string]any{"type": "image", "data": "aA==", "mimeType": "image/png"}}}}
			var err error
			if failure == "transport" {
				err = errors.New("synthetic connection lost; outcome unknown")
			}
			result := toolEnvelope("mcp_tool_call", payload, err)
			addition := app.ResponseAdditions{UserMessages: []app.UserResponseAddition{{Type: "activity_center_user", Version: 1, InsertionID: "ins_real", Sequence: 1, ConversationID: "conv_real", Text: "user supplement"}}, TextBlocks: []string{"[[AGENTDOCK_USER_INSERT_V1]] ins_real"}}
			result = normalizedEnvelope(t, appendTrustedAdditions(result, addition))
			content := result["content"].([]any)
			if !strings.HasPrefix(asMap(content[len(content)-2])["text"].(string), "[[AGENTDOCK_MCP_CATALOG_V1]]") || !strings.HasPrefix(asMap(content[len(content)-1])["text"].(string), "[[AGENTDOCK_USER_INSERT_V1]]") {
				t.Fatal("business/catalog/insertion order changed")
			}
			if result["isError"] != (failure != "success") {
				t.Fatal("catalog changed the original business error")
			}
			structured := asMap(result["structuredContent"])
			if asMap(structured["mcp_catalog"])["total"] != float64(2) {
				t.Fatal("catalog missing from structured result")
			}
			if failure != "transport" && (asMap(content[0])["text"] != "original" || asMap(content[1])["type"] != "image") {
				t.Fatal("original multi-content payload lost")
			}
		})
	}
}
