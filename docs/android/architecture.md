# Android architecture

## Product boundary

| Component | Responsibility | Trust boundary |
|---|---|---|
| Android APK | UI, settings, lifecycle, Core client and Termux dispatch | Android app sandbox |
| AgentDock Core | Authoritative conversations, tasks, calls, approvals, permissions, capabilities and insertions | Linux process/data root |
| Termux bridge | Fixed operations, PRoot lifecycle, candidate update/rollback and diagnostics | Termux private home |
| Debian PRoot | Linux runtime for the ARM64 Core | PRoot filesystem |
| GitHub Actions | Compilation, emulator tests, screenshots, hashes and evidence | Ephemeral CI runners |

A UI click is not success. Core writes succeed only on a successful Core response. Termux operations succeed only after a callback validates the original operation ID, request ID, nonce and structured result.

## Android source areas

- `data/`: bounded Core HTTP/SSE client, repository, DataStore settings and Keystore credentials.
- `model/`: routes, snapshots, exact timing constants and explicit debug fixtures.
- `termux/`: official RUN_COMMAND contract, dispatcher, persistent operation store and callback validator.
- `lifecycle/`: WorkManager, optional visible foreground guardian, notification actions, boot check and Quick Settings tile.
- `ui/`: responsive Compose shell, all pages and ViewModel.
- `termux/` assets: scripts the user exports into the external Termux app.

## Core transport

Default origin is `http://127.0.0.1:8765`. Cleartext is accepted only for literal loopback hosts. Remote origins require an explicit switch and HTTPS. The client rejects origin paths, queries, fragments, user-info and redirects; bypasses proxies; bounds requests/responses; reconnects SSE with `Last-Event-ID`; and preserves partial endpoint failures instead of showing healthy empty lists.

Android consumes existing Core routes for health, execution/sidebar, conversations, tasks, calls, activity, approvals, effective permissions, Skill, plugins, MCP and insertions. IDs, activity, termination, receipt and success states remain server-authoritative. Permission writes carry the Core revision. Unsupported shared mutation routes report an error rather than fabricated success.

## Termux transport

The APK uses an explicit package/component, fixed executable `~/.termux/tasker/agentdock-workbench`, five validated arguments, bounded JSON stdin and a one-shot mutable PendingIntent to a non-exported callback service. Tokens never enter command arguments. App-private atomic operation summaries survive Android process death.

The Termux script verifies the same IDs and nonce before doing work and echoes them in its result. Callbacks are bounded to 64 KiB UTF-8, expire after two hours and cannot overwrite a terminal operation. Secret-bearing responses and unstructured error output are not stored. Outstanding operations are not evicted to admit new requests.

## Managed PRoot layout

```text
~/.agentdock-workbench/node/
  versions/<version>/
  current -> versions/<version>
  previous -> versions/<version>
  runtime/ data/ workspace/ logs/
  auth-token desired-state core.pid core.identity
```

Probe/status do not initialize the node or generate credentials. Mutations require the bridge lock. Process records include boot identity, start time, group and session; unknown live identities require user review and are not signaled. Archive extraction rejects traversal, links, duplicate members, special files, file/directory conflicts and unbounded expansion. Version directories are not overwritten. Recovery-reference cleanup is deferred until the installation journal and schema-aware rollback are implemented. Their absence still blocks complete deployment acceptance.
