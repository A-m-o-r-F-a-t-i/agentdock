# AgentDock 1.1.4 implementation map

Date: 2026-09-21. Starting commit:
`e6380d4779a8bfe6093205a8b442d95246141a68`.
Working tree: `E:\PROJECT\AgentDock-1.1.3`, branch `feat/1.1.4-update`.
Task: `tsk_38885d08f6b6889c`.

The user-facing detailed implementation specification is
`AgentDock_1.1.4_详细落实计划.md`. This checked-in map records concrete implementation
locations and gates. It is not a declaration that the release has passed.

## Implementation sequence

| Work package | Existing entry points | Changes and exit gate |
| --- | --- | --- |
| UI state | `Themes/Tokens.xaml`, `ExecutionWindow.Actions.cs` | Default borders, independent focus, checked menus, remove only the recycle-bin separator; test mouse-out and reopened menus |
| Shared theme | `App.xaml`, `Themes/Light.xaml`, `Themes/Dark.xaml` | Application-level dynamic resources, persisted system/light/dark before first window; test main/execution/dialog parity |
| Execution presentation | `ExecutionWindow.xaml`, `Models/ExecutionModels.cs` | Compact/detailed toggle, actual tool names, nullable timings, conversation/task labels, shared activity expiration |
| Observation | `internal/app/execution_dispatch.go`, `execution_observation.go`, `internal/activity/{model,calls}.go` | One root per request, separate RPC/process measurements, file action result and affected paths, replay-safe request activity |
| UI policy | `internal/mcp/server.go`, local runtime HTTP API | Persisted `chatgpt_mcp_ui_enabled`, live directory/result policy without restart, retain non-UI metadata and tool capabilities |
| Funnel | `internal/desktopruntime/tailscale_*.go`, `RuntimeService.Tailscale.cs` | Reuse owned mappings, separate local readiness from public verification, bounded backoff, cancellation and detailed phase results |
| Startup | `Services/TaskAdminService.cs`, existing `--run-core-task` GUI entry | Avoid directly launching the console core as the scheduled task; preserve elevated execution and installer recovery |
| Engineering | activity projection, desktop data services, documentation | Incremental/bounded updates, regression tests, architecture/development guidance, fixed upstream comparison and migration paths |
| Delivery | `.github/workflows/windows-package.yml` | Go and desktop tests, Windows package/manifest validation, install/upgrade/rollback acceptance, release only in the user's fork |

## Source findings

`file_edit` already passes through `Runtime.Call` and `callObserved`. Therefore the
missing EDIT_FILE report cannot be repaired by adding a second root event. Existing
file-change events intentionally omit dry runs and unchanged content; request
records must still contain the operation and its real outcome. Tests must identify
whether any remaining gap is admission, attribution, projection or presentation.

The current Button template uses a transparent border. The custom MenuItem
template has no checked-state presenter, and menu construction does not mark the
selected filter or theme. These are verified source defects.

Execution rows currently convert a missing `elapsed_ms` into `0.000 s`. Replace
that rendering with "未记录" and expose measured RPC and execution durations
separately. Legacy history must not be rewritten with invented measurements.

The current public Funnel verifier retries every second for up to 60 seconds and
its timeout path says configuration will roll back. DNS propagation must not
block local configuration completion or trigger destructive whole-config resets.
Keep verification strict: an applied route is not proof of public connectivity.

The starting commit has a successful Windows package workflow run (`35596323656`).
This is starting-point evidence only, not the result for the new changes.

## Data rules

- Existing event identity, sequence and binding remain canonical. Add optional
  measurements and structured file details; preserve old records as unknown.
- Capture real incoming root requests separately from `UpdatedAt`. Replaying or
  reading an event cannot start a new 30-second activity interval.
- Activity is `0 <= now - last_tool_call_at < 30s`, and false for terminated
  conversations. The long-running process state is independent of this indicator.
- Redact file paths, preview text and sensitive fields before persistence. A
  multi-file patch has one root and bounded affected-file details.
- Only valid finished root RPCs contribute to latency percentiles. Label summed
  parallel durations separately from elapsed observation span.
- Persist the display preference before changing active policy. Missing new UI
  preferences inherit the existing `MCPAppsEnabled` value, including explicit false.
- Disable only UI mounting metadata, not file-parameter metadata, authentication,
  visibility restrictions, content or structured tool results.

## Original acceptance gates

Preserve UI-01..06, OBS-01..07, ACT-01..04, MCP-01..03, SEM-01..02, FUN-01..04,
DOC-01..02, ENG-01, PERF-01 and REL-01. Add startup and new edge-case regressions
without treating automated tests as a substitute for real installation testing.

Performance fixtures remain 1,000 conversations and 100,000 root calls. Measure on
the same Windows machine/configuration before and after, retaining raw samples.
Targets: cold interaction P95 <= 2s; conversation switch <= 200ms; event display
<= 250ms; history filtering <= 500ms; observation overhead <= 5ms; idle CPU <= 1%;
stable post-detail-cycle memory growth <= 10%. Do not lower unmet thresholds.

Funnel targets exclude human approval and external DNS propagation: cached restore
<= 1.5s, local preflight <= 1s, local ready <= 3s, interactive after handoff <= 100ms,
first probe <= 5s. Count CLI invocations to verify duplicate-click single flight.

## Commit and rollback discipline

Keep feature commits testable. Save the starting point and the existing installation
without changing production configuration during development. Do not replace the
running AgentDock instance merely to test a theme or a display preference. Verify
installer recovery in isolation before any live upgrade. A failed acceptance gate
keeps release status incomplete even when compilation succeeds.
