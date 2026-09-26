# macOS Workbench interface contract

Baseline: `b367eaab95202873fb213b8713440bf7822878c4`
Lane: `parallel/20260925/macos`

## Source of truth

The macOS application treats AgentDock Core's authenticated loopback Runtime API as the only source of truth for conversations, tasks, calls, approvals, permission policy and insertions. It does not open Core databases, task JSON files or the activity journal directly. Local App storage is limited to presentation preferences such as window frame, theme and collapsed workspace groups.

The client resolves the origin from `ServiceConfiguration`, accepts only direct loopback HTTP (`127.0.0.1`, `::1` or `localhost`), disables proxies, and sends `Authorization: Bearer <AGENTDOCK_AUTH_TOKEN>`. A missing token, non-loopback origin or malformed response is a visible unavailable state rather than a successful empty result.

## Consumed resources

| Capability | Runtime resource | Client behavior |
|---|---|---|
| Overview | `GET /internal/runtime/execution` | Reads statistics, permission mode, policy revision, server time and authoritative activity maps. |
| Sidebar | `POST /internal/runtime/execution/sidebar` | Sends view/search/per-workspace modes, limits, history cursors and selected conversation. Preserves Core ordering. |
| Conversation | `GET /internal/runtime/conversations/{id}` | Reads full state and effective permission. |
| Calls | `GET /internal/runtime/calls` and `/calls/{id}` | Uses bounded pages; detail is lazy. |
| Call stream | `GET /internal/runtime/calls/stream` | Resumes with `Last-Event-ID`; line and event buffers are bounded. |
| Tasks | `GET /internal/runtime/execution/tasks`, `/tasks/{id}`, `/tasks/{id}/threads` | Reads task, steps, conditions, review and thread data without reconstructing them locally. |
| Management | Batch, lifecycle, binding, stop and approval routes | Every mutation is explicit and followed by an authoritative refresh. |
| Permission policy | `/internal/runtime/permissions/effective`, `POST /internal/runtime/permissions` | Uses policy revision/CAS. Unknown new settings remain read-only. |
| Insertions | `/internal/runtime/conversations/{id}/insertions` | Displays eligibility, expiry and durable receipt data exactly as returned. |

## Time contracts

These values are deliberately separate:

- **120 seconds**: conversation's recent-activity indicator, based on Core `server_now`, `last_activity_at` and `in_flight`.
- **180 seconds**: user insertion/stop eligibility window when the Core exposes it.
- **300 seconds**: an unclaimed insertion's expiry window.
- **30 seconds**: one receipt wait before the UI reports a pending/unknown receipt state.

The App never extends one window because another is still open.

## Failure and staleness

Requests have a 12-second deadline and bounded response size. A new navigation generation cancels obsolete reads. The most recent successful snapshot remains visible after disconnect, with a stale/offline banner and the exact error. Retryable connection errors reconnect with capped backoff; HTTP policy or validation failures wait for a user action. A missing newer route is shown as **Unavailable on this Core version** and is not simulated.

## Native window rules

The Workbench is one AppKit window per process. Closing it does not stop Core. Reopening the Dock icon, application menu or status item reuses the same controller and current selection. The left pane owns navigation/search, the center pane owns the execution timeline and insertion composer, and the right pane owns detail/actions. Split positions and theme are App preferences only.

## Cross-lane dependencies

WB06 consumes, but does not implement, the shared permission-default change, insertion receipt protocol and activity-list fixes. Compatibility parsing accepts their older and newer field names. The final matrix identifies any row that still depends on WB02, WB03 or WB04.
