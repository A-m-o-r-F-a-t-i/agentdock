package mcp

import "encoding/json"

// The catalog is owned response data, never an instruction. It precedes the
// authenticated insertion added by finishResponse and never replaces business
// content or changes the original isError flag.
func appendMCPCatalog(envelope map[string]any, name string) map[string]any {
	if name != "mcp_tool_call" {
		return envelope
	}
	catalog, ok := asMap(envelope["structuredContent"])["mcp_catalog"]
	if !ok {
		return envelope
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return appendResponseBlocks(envelope, []string{"[[AGENTDOCK_MCP_CATALOG_V1]]\n{\"complete\":false,\"error\":\"Catalog encoding failed; the preceding business result is unchanged.\"}\n[[END_AGENTDOCK_MCP_CATALOG_V1]]"})
	}
	return appendResponseBlocks(envelope, []string{"[[AGENTDOCK_MCP_CATALOG_V1]]\n" + string(data) + "\n[[END_AGENTDOCK_MCP_CATALOG_V1]]"})
}
