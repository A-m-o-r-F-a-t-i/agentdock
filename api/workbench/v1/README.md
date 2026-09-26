# Workbench Local Control API v1

This directory is the shared transport contract for local AgentDock Workbench clients. `contract.json` indexes existing Core routes plus two WB01-owned thin transports: task lifecycle writes delegate to `task_manage`, and workspace reads/writes delegate to `workspace_manage`. Domain state machines remain in their existing packages.

## Security invariants

- Reads use the same bearer/OAuth authentication as Core.
- Management writes keep Core's direct-loopback check; proxy headers do not establish locality.
- CLI credentials come from environment variables or files, never a plaintext token argument.
- Credentials are excluded from normal output, machine output, verbose metadata, fixtures, logs, and handoff reports.
- Optimistic revisions and explicit destructive confirmations are part of the contract, not presentation hints.

## Compatibility and uncertainty

Clients ignore unknown response fields and preserve continuation fields. Existing routes and legacy CLI commands are not repurposed. Failed calls, partial batches, approval waits, insertion `delivery_unknown`, timeout, and interruption have distinct non-zero exit codes; clients must not collapse them into success.

## Files

- `contract.json`: machine-readable transport, command, error, pagination, stream, and safety contract.
- `fixtures/`: representative envelopes and stream records used by candidate CI tests.
- `docs/cli/workbench.md`: operator guide.
- `docs/cli/command-matrix.md`: implemented, derived, legacy, partial, and unavailable commands.

## Integrated domain contracts (revision 3)

`contract.json.domain_contracts` binds the permission, insertion and activity fields to their existing Core authorities. Permission schema 3 separates configured settings from effective settings. Receipt type `none` means unconfirmed, not an unknown error; clients render retry availability and budgets from the server. Activity uses the shared `activity/` schema and independent 120/180/300/30-second boundaries. The CLI preserves these additive fields and native clients must not recreate their state machines.
