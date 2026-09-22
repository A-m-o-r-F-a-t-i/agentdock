# AgentDock development rules

This repository is the user's extension fork. Keep changes on the current feature
branch. Never publish to the upstream author's repository.

## Before editing

1. Load `agentdock_context` for this working tree. Check Git status, the current
   task, and local rules. Read the complete files being changed.
2. Find the existing responsibility and tests. Make a bounded implementation plan
   before changing code. Do not create parallel runtimes, task systems or themes.
3. Preserve unrelated changes. Do not reset, stash or overwrite another agent's work.

## Implementation boundaries

- `Runtime.Call` / `callObserved` owns external tool admission and root call identity.
  Children inherit an immutable binding. Never infer a conversation from the latest
  task, selected window or account. Unattributed calls stay unattributed.
- Tool RPCs, internal spans and command processes have separate lifetimes. Count
  only roots in totals. Unknown timings and outcomes are nullable, never fake zero
  or success. Use monotonic durations inside a process.
- Persist redacted facts before presenting them. Keep parameters, diff previews,
  output, caches, queues and subscriptions bounded. Never recursively observe
  journal writes, UI queries, heartbeats or background probes as AI requests.
- Views render state; services own I/O and cancellation. Bind selected, checked and
  expanded states explicitly. Hover or keyboard focus is not business state.
- Theme colors, borders and sizing belong in shared resources. Do not set hardcoded
  brushes in event handlers. Keep default and focused control geometry identical.
- All I/O must have cancellation, errors and resource cleanup. Do not synchronously
  block the UI with `.Wait()` / `.Result`; use `async void` only for event handlers.
- Keep one canonical implementation. Name files and types by responsibility, not
  New, Final or V2. Put necessary old-data conversion at a documented boundary.
- Keep authentication, permission checks, installation rollback and configuration
  ownership intact. A display setting must never restart the running core.

## Verification and delivery

Run Go formatting/tests and Windows desktop tests. Add regression tests for each
changed behavior; do not remove tests or weaken assertions to obtain a green build.
Use injected time for activity boundaries. Verify concurrent conversations, calls
without tasks, old records, rejected writes and asynchronous command completion.

Build the final Windows package with `.github/workflows/windows-package.yml` in
`A-m-o-r-F-a-t-i/agentdock`. Source, tests, package construction, installation tests
and publication are separate delivery states. Report unexecuted checks honestly.

Implementation map: `docs/implementation-1.1.5.md`.

For the 1.1.5 delivery, run static and isolated automated regression only. Do not
launch the desktop app or Setup, install/uninstall/upgrade/rollback, or change the
production Core. Dispatch windows-package.yml with installation_tests=false.
Keep the 120s activity, 180s request eligibility and 300s next-call insertion
expiry independent. Manual desktop and installation checks remain not run.
