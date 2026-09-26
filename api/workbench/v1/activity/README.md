# Workbench activity contract v1

This directory is the additive, cross-platform contract for conversation activity. Windows, macOS, Android and the shared CLI may render it differently, but must preserve the same state meanings and half-open time boundaries.

## Separate clocks and states

- `last_interaction_at` is the latest genuine external root request/RPC progression timestamp. The first RPC return is a valid interaction boundary. Polling, rendering, reconnect, recovery, metadata binding and asynchronous process output after that RPC return do not renew it.
- `interaction_expires_at` is exactly `last_interaction_at + 120000 ms`. `recently_active` is true only while `server_now` is in `[last_interaction_at, interaction_expires_at)`.
- `last_activity_at` is execution history. It may advance when a detached command emits output or completes after the RPC has returned.
- `in_flight` is execution liveness: a confirmed root call, pending approval or background command remains visible and stoppable even after `recently_active` expires. It never extends the interaction window.
- `statistics.last_tool_call_at` remains the basis for the independent 180000 ms insertion composer window.
- Unclaimed insertions expire at 300000 ms; receipt waiting reaches its deadline at 30000 ms.

The server-provided `server_now` is authoritative. Clients may advance that anchor with a monotonic clock, but must not derive deadlines from wall-clock guesses.

## Navigation identity

Ordinary rows use a nonempty, globally unique `conversation_id` that is neither `unattributed` nor prefixed by `footer:`. The sole legal special row has all of the following properties:

- its group has `workspace_id = "unattributed"`;
- `is_unattributed = true`;
- `conversation_id = ""`;
- at most one such row exists in a response.

Clients must reject ordinary empty IDs, duplicate real IDs, repeated or malformed special rows and reserved-key collisions. They must not generate replacement IDs or silently deduplicate a malformed response.

## Fixtures

- `conversation-status.fixture.json` covers recent interaction, a 20-minute-style background process, pending approval, a long synchronous RPC and the typed unattributed row.
- `activity-boundaries.fixture.json` is the shared edge vector for 120 s, 180 s, 300 s and 30 s rules.
- `conversation-status.schema.json` describes the additive response surface.

Run `scripts/ci/parallel-activity/verify_contract.py` in GitHub Actions to validate the fixtures and their derived invariants.
