# AgentDock Workbench · macOS feature matrix

Comparison source: Windows control panel and execution center at `b367eaab95202873fb213b8713440bf7822878c4`.
Target: native Swift/AppKit, no Electron/WebView shell, no duplicated Core state machine.

Status vocabulary: **Existing** = present before WB06; **WB06** = implemented in this lane; **Dependency** = UI/client included but final semantics come from another lane; **Manual** = requires a signed/notarized or physical-machine check unavailable in branch CI.

## Product shell and service management

| Capability | Windows baseline | macOS before WB06 | WB06 target/status | Evidence |
|---|---:|---:|---:|---|
| Native product window | WPF | AppKit setup window | **WB06** single native Workbench window | AppKit compile + screenshots |
| Menu/status integration | Tray | Menu bar | **Existing**, Workbench entry added | App source test |
| Reopen same window/selection | Yes | Setup window only | **WB06** | controller unit/UI test |
| Closing UI leaves Core running | Yes | Yes | **Existing/WB06** | no stop-on-close path |
| Core status/start/stop/restart | Yes | Yes | **Existing**, surfaced in Workbench | service/controller tests |
| Install/update transaction | Yes | Yes | **Existing**, unchanged | existing macOS app test |
| Logs/configuration folders | Yes | Yes | **Existing**, surfaced in Workbench | menu/action test |
| Public access/Tunnel settings | Yes | Yes | **Existing** setup/advanced UI | existing tests |
| Browser/ACP/Nexus settings | Yes | Yes | **Existing** advanced UI | existing tests |

## Workbench navigation and activity

| Capability | Windows baseline | macOS before WB06 | WB06 target/status | Dependency/evidence |
|---|---:|---:|---:|---|
| Three-pane navigation/timeline/detail | Yes | No | **WB06** | deterministic AppKit screenshots |
| Search placeholder `搜索对话` | Yes | No | **WB06** | UI accessibility assertion |
| Workspace grouping | Yes | No | **WB06**, ordered by Core sidebar projection | model tests |
| Default recent rows per workspace | 5 | No | **WB06**, sends Core default mode/limit | request contract test |
| Expanded history | 20-row pages | No | **WB06** bounded pagination | model/client tests |
| Pinned/selected/running visibility | Yes | No | **WB06**, consumes authoritative sidebar rows | model tests |
| Active indicator | 120 s + in-flight | No | **Dependency WB04**, compatibility parser included | server-time tests |
| Archive/trash/all views | Yes | No | **WB06** | projection/action tests |
| Offline/stale snapshot | Yes | No | **WB06** preserves last valid data with banner | view-model tests |
| Stream reconnect/cursor resume | Yes | No | **WB06**, bounded SSE parser | parser/reconnect tests |
| Unknown/new schema fields | Tolerated | N/A | **WB06** tolerant JSON tree | parser tests |

## Conversations, tasks and calls

| Capability | Windows baseline | macOS before WB06 | WB06 target/status | Dependency/evidence |
|---|---:|---:|---:|---|
| Conversation detail | Yes | No | **WB06** | client/model tests |
| Terminate/resume conversation | Yes | No | **WB06**, explicit confirmation | action tests |
| Rename/pin/tags/archive/trash/restore | Yes | No | **WB06**, Core batch routes | action tests |
| Link/set current task | Yes | No | **WB06** reads task/thread state; mutation shown when capability exists | client tests |
| Task detail/steps/conditions/review | Yes | No | **WB06** | fixture screenshot/model test |
| Call timeline and lazy detail | Yes | No | **WB06** | bounded pagination test |
| Request/response/technical/timing tabs | Yes | No | **WB06** | screenshot/UI tests |
| File edit statistics/diff preview | Yes | No | **WB06** when returned by Core | model test |
| Stop running call | Yes | No | **WB06** | route/action test |
| Approve/reject request | Yes | No | **WB06** | route/action test |
| Export/copy output | Yes | No | **WB06** selected JSON/text export | UI test |
| Completion notifications | Yes | No | **WB06** in-window notice; macOS Notification permission remains OS-owned | manual notification check |

## User insertion

| Capability | Windows baseline | macOS before WB06 | WB06 target/status | Dependency/evidence |
|---|---:|---:|---:|---|
| Insert message into active conversation | Yes | No | **WB06** composer and submission route | action test |
| 180-second eligibility | Yes | No | **Dependency WB03/WB04**, never inferred from 120 s | contract/model test |
| 300-second unclaimed expiry | Yes | No | **Dependency WB03**, terminal state rendered | contract/model test |
| 30-second receipt wait | Yes | No | **Dependency WB03**, pending/unknown shown without false success | contract/model test |
| Cancel/retry insertion | Yes | No | **WB06** when route/capability exists | action test |
| Insertion in timeline | Yes | No | **WB06** compatibility parser | model/screenshot test |

## Permissions and security

| Capability | Windows baseline | macOS before WB06 | WB06 target/status | Dependency/evidence |
|---|---:|---:|---:|---|
| Effective permission mode | Yes | Partial | **WB06** reads effective Core policy | client test |
| Full / approval / read-only profiles | Yes | No Workbench page | **WB06** status and revision-aware editor | model/action test |
| Explicit custom-permission toggle | Planned WB02 | No | **Dependency WB02**, rendered only when exposed | compatibility test |
| Fresh install defaults to full | Planned WB02 | Existing installer policy differs | **Dependency WB02**, WB06 does not overwrite Core config | no local write assertion |
| Existing install preserved | Yes | Yes | **Existing/WB06** | no migration/write path |
| Workspace/conversation scope | Yes | No | **WB06** | effective-policy test |
| Approval decisions bound to ID | Yes | No | **WB06** | action contract test |
| macOS Automation permission | N/A | Yes | **Existing**, summarized in Workbench | permission checker test |
| macOS Accessibility permission | N/A | Yes | **Existing**, summarized in Workbench | permission checker test |
| File access permission | N/A | Yes | **Existing**, summarized in Workbench | permission checker test |
| Direct-loopback/no-proxy client | Windows equivalent | No Workbench client | **WB06** | transport unit test |
| Bounded response/SSE buffers | Windows equivalent | No Workbench client | **WB06** | limit tests |

## Appearance and packaging

| Capability | Windows baseline | macOS before WB06 | WB06 target/status | Evidence |
|---|---:|---:|---:|---|
| System/light/dark theme | Yes | System only | **WB06** system/light/dark preference | light/dark screenshots |
| Native typography/spacing | WPF tokens | AppKit | **WB06** dynamic AppKit colors/fonts | screenshots |
| Keyboard navigation/accessibility labels | Yes | Partial | **WB06** | UI assertions/manual VoiceOver pass |
| Intel app slice | Yes | Existing universal builder | **WB06 verified** | `lipo`/`file` report |
| Apple Silicon app slice | Yes | Existing universal builder | **WB06 verified** | `lipo`/`file` report |
| Native Intel test runner | N/A | Release workflow has label | **WB06 candidate workflow** | Actions run architecture report |
| Native Apple Silicon test runner | N/A | Release workflow has label | **WB06 candidate workflow** | Actions run architecture report |
| DMG and update ZIP | Yes | Existing | **WB06 candidate**, lane/SHA/run artifact names | Actions artifact |
| Developer ID signature/notarization | Release concern | Secrets not assumed | **Manual** unless secrets are available; otherwise truthfully ad-hoc/not run | signature report |
| Physical install/upgrade/rollback | Yes | Existing flow | **Manual**; no production installation in WB06 | handoff checklist |

## Deliberate non-goals

WB06 does not edit Windows files, shared Core state machines, the permission policy implementation, insertion retry semantics, activity freshness computation, release tags or production assets. It does not launch/install the candidate on a user's Mac from this branch. Any unavailable shared capability remains visibly unavailable rather than replaced by a macOS-only fork.
