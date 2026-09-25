# Workbench Local Control API v1

This directory is the shared transport contract for local AgentDock Workbench clients. It indexes existing Core routes and the thin task/workspace additions owned by WB01; domain state machines remain in their existing packages.

## Security

- Read calls use the same bearer/OAuth authentication as Core.
- Management writes remain direct-loopback only. Proxy headers do not establish locality.
- CLI tokens come from environment variables or files, never a command-line token argument.
- Responses and diagnostics must not print credentials.

## Compatibility

Clients ignore unknown response fields and preserve continuation tokens. Existing routes and old CLI commands are not repurposed. `contract.json` is machine-readable; `fixtures/` freezes representative envelopes and streams.
