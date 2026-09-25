# AgentDock 1.1.7 implementation

Source: user-approved `AgentDock_1.1.7_详细修改计划.md` (2026-09-24).
Initial commit: `b66e5f94c0feca92e4d15f0776bf922117549a85`.
Working branch: `feat/1.1.6-release`; publication target is only `A-m-o-r-F-a-t-i/agentdock`.

## Scope and acceptance

- [x] A: source, ownership, toolchains and test entry points checked.
- [ ] B: R08 routed input and transactional sidebar pagination.
- [ ] C: F01/F02/F03/F07 durable recovery and backup fidelity.
- [ ] D: R01/R07 Unicode scalar output limits and byte-position descriptions.
- [ ] E: R06/F05/F09 final binding, request snapshots and cache generations.
- [ ] F: R02/R03/R04/R05/R09 sidebar, Chinese actions, details and edit statistics.
- [ ] G: F04/F06/F08 cancellation, pending OAuth capacity and bounded activity writes.
- [ ] H: F10/F11 focused refactoring, immutable build SHA and candidate verification.

## Execution boundary

Do not launch or replace the production Core, desktop, installer or unrelated tasks.
Pure desktop tests and compilation may run locally. Offscreen WPF tests run only
in the isolated GitHub runner; never bypass that test executable's environment guard.
Installer and native privilege fault injection require a separate isolated environment.
No formal release or tag is authorised by this execution request.

## Validation entry points

- Go 1.26.5: `go test -p 2 ./... -count=1 -timeout=8m` and `go vet ./...`.
- Race: the packages listed in the approved plan, on a supported isolated toolchain.
- Desktop policies: `dotnet run --project desktop/windows/control-panel-tests/AgentDock.ControlPanel.Tests.csproj --configuration Release`.
- Desktop WPF: `dotnet build desktop/windows/control-panel/AgentDock.ControlPanel.csproj --configuration Release`.
- Offscreen controls: `desktop/windows/control-panel-layout-tests/AgentDock.ControlPanel.LayoutTests.csproj` in GitHub Actions.
- Candidate: `.github/workflows/windows-package.yml`, `publish=false`.

## Implemented changes and verification scope

The boxes above describe complete acceptance, not merely code availability. The
candidate implements the following changes. Platform evidence remains separate.

| Plan | Code and automated coverage | Remaining acceptance boundary |
| --- | --- | --- |
| R08 | Shared asynchronous paging, real routed events, duplicate suppression, candidate/committed cursors, stale navigation cancellation; isolated WPF transport fixture includes 100 cycles | Execute the WPF fixture on the isolated runner; physical keyboard/DPI testing is separate |
| R01/R07 | 1,000–100,000 Unicode scalar budget, hot entry snapshots, ordinary text shared across fields, retained-source continuation, UTF-8 byte cursors, truthful position strings | Unknown dynamic structured schemas are explicitly exempt until a pageable contract is declared |
| R06 | Target selected before rules, implicit conversation inheritance, same-target convergence, conflicting revision rejection, final complete after coordination, immutable root audit | Real host duplicate-bootstrap correlation is not an automated client guarantee |
| R02/R03/R04/R05/R09 | Removed conversation badge and child-call tab, search hint, Chinese title formatter, edit statistics before time in both modes | Isolated rendering and physical monitor validation are distinct |
| F01/F02/F07 | Atomic journal publication; verified private staging before replacement; retained interrupted transaction; bounded backup copy with mode preservation and explicit link rejection | Platform ACL/owner/extended attributes and all native failure stages require isolated platform evidence |
| F03 | Privilege lifecycle separated into prepare/apply/verify/commit/recover; backup cleanup only after verified terminal state; bounded recovery independent of user cancellation | Fake lifecycle tests do not replace native elevated process fault injection |
| F04 | Cancellable file lock and removed redundant uninterruptible gates; pre-commit cancellation, post-commit truth, no token/owner leakage | Race and full regression must be attached to the exact candidate SHA |
| F05 | Content-verified registry cache and index reuse; external same-size/same-time changes detected; failed writes invalidate; returned records detached | 10,000-record warm median target of 10× is not met by the recorded 9.21× sample |
| F06 | Independent pending OAuth count/byte budget and fixed consent deadline, durable authorization promotion, consistent index cleanup, differentiated transient capacity errors | The one-hour pending window is tested with a simulated 50-minute PKCE flow; real provider timing may require adjustment |
| F08 | Event and byte admission budgets, cancellation release, terminal credits before tool/process/management side effects, queue metrics, oversized producer isolation | Persistent disk failure still returns an audit failure; capacity control is not a durable queue on a failed disk |
| F09 | Generation-separated cache flights and stale completion protection | Existing revision checks remain in place |
| F10 | Execution HTTP routes split by resource with shared admission/error boundary; projection binding/facts/lifecycle separated; context/output/completion and Windows privilege responsibilities isolated | Existing typed plugin/browser contracts are retained; no wholesale runtime rewrite |
| F11 | One resolved commit supplies Linux validation, Windows build, packaged scope and publication guard; remote tag checked again before release | Candidate workflow and publication remain different actions; publish defaults false |

## Reproduced validation before candidate build

Validation directory on the implementation machine:
`E:\PLAYGROUND\AGENTDOCK\artifacts\wsp_6ef29909aea578e7\tsk_4ffe2f805b5dfe27\validation`.

- Go toolchain: `go1.26.5 windows/amd64`. The initial inherited baseline full
  suite passed. The first integrated suite identified three command-environment
  response regressions; these were corrected in the formatter, and the original
  tests were retained and passed on targeted rerun. The final full Go suite also
  passed; `go-final-all.log` records the result. The final test/vet command took
  234.981 seconds on the implementation machine.
- `go vet ./...` passed again on the final integrated code (`go-final-vet.log`).
- Critical-package Windows race log `go-integrated-race.log` contains passing
  activity, permission, insertion, fs, snapshot, acp and selfupdate results.
- Output/adapter/normal-environment integration rerun: app 14.082 s, MCP 6.071 s,
  passed. Both adapters retain trusted insertions and the same text preview.
- Desktop pure-policy regression: 854 assertions passed. WPF application and
  isolated interaction-test project compiled with zero warnings/errors. The final
  version-1.1.7 desktop compile also passed after aligning fixture fields and JSON
  names with the real sidebar contract (`desktop-final-build.log`).
- Focused cancellation, pending authorization, append capacity, source-reference,
  context binding, generation and workflow identity regressions passed. The
  workflow tests parse YAML and move real Git branch/tag refs in a temporary repo.
- Local compilation is not a claim that the WPF interaction executable, installer,
  or native privilege transition was run. Those results are recorded separately.

## Same-machine registry measurement

Baseline: `b66e5f94c0feca92e4d15f0776bf922117549a85`, detached temporary worktree,
with the same opt-in performance fixture as the candidate. Windows amd64,
Go 1.26.5, 20 logical CPUs. Each size uses 11 cold and 51 warm samples.

| Records | Baseline warm median (ms) | Candidate warm median (ms) |
| --- | ---: | ---: |
| 100 | 2.3360 | 1.6762 |
| 1,000 | 5.5615 | 2.1473 |
| 10,000 | 40.0199 | 4.3446 |
| 20,000 | 77.2965 | 7.8435 |

For 10,000 warm records, P95 changed from 46.8224 to 5.5284 ms and allocated
bytes/read from 15,635,155 to 6,778. The median improvement is about 9.21×,
below the planned 10× target. Cold median changed from 41.411 to 42.461 ms.
Content hashing on every access intentionally retains cross-process authority
validation. These are registry reads, not measurements of end-to-end bootstrap.

## Candidate identity and remaining evidence

`windows-package.yml` records the immutable source SHA in Linux evidence and
`verification-scope.json`. Build with `publish=false`. Do not turn package checksum
success into installation success, or offscreen rendering into physical keyboard
and monitor validation. Do not mark this plan complete until the remaining gates
have real results. The workflow's installation option is only for its isolated runner.

Source changes and regression execution are in progress. Candidate build, actual
installation, native privilege recovery and manual desktop verification are `not_run`.
Do not interpret this implementation checklist as completed release acceptance.
