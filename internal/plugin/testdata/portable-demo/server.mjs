import readline from "node:readline";

// A deterministic stdio MCP fixture; it writes JSON-RPC only to stdout.
const input = readline.createInterface({ input: process.stdin });
input.on("line", (line) => {
  let request;
  try { request = JSON.parse(line); }
  catch { process.stdout.write(JSON.stringify({jsonrpc: "2.0", id: null, error: {code: -32700, message: "Parse error"}}) + "\n"); return; }
  if (request.id === undefined) return;
  let result;
  switch (request.method) {
    case "initialize":
      result = { protocolVersion: request.params?.protocolVersion ?? "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "portable-echo", version: "1.0.0" } };
      break;
    case "ping": result = {}; break;
    case "tools/list":
      result = { tools: [{ name: "echo", description: "Return a supplied string.", inputSchema: { type: "object", properties: { text: { type: "string" } }, required: ["text"], additionalProperties: false } }] };
      break;
    case "tools/call":
      if (request.params?.name === "echo" && typeof request.params?.arguments?.text === "string") {
        result = { content: [{ type: "text", text: request.params.arguments.text }] };
      }
      break;
  }
  const response = result === undefined
    ? { jsonrpc: "2.0", id: request.id, error: { code: -32601, message: "Unknown method or tool" } }
    : { jsonrpc: "2.0", id: request.id, result };
  process.stdout.write(JSON.stringify(response) + "\n");
});
