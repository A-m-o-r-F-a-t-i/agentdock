# Installer performance, generation retention, and candidate evidence

This document records the WB05 implementation boundaries and the evidence that
must exist before any installer optimization can be considered release-ready.
The lane is intentionally isolated: it does not publish, sign, install, tag, or
merge artifacts, and it does not change the production release workflows.

## 1. What changed

### 1.1 Structured install timing

Installer Engine results and durable transactions can now contain an optional
`timing` summary. The stable stage vocabulary is:

- `download`
- `verify`
- `extract`
- `installer_staging`
- `service_stop`
- `payload_write`
- `config_skill_bootstrap`
- `service_start`
- `readiness_wait`

Each stage records UTC start/completion timestamps and elapsed milliseconds.
The summary also records total wall time and whether a bundled/offline payload
was reused. Missing stages mean that the operation was not part of that request;
callers must not fabricate zero-duration samples.

Windows Setup writes a best-effort redaction-safe summary to:

```text
<runtime-root>/install/setup-timing.json
```

The Setup summary embeds Installer Engine timing when the Engine handshake is
available. A failure to write diagnostics is a warning only and never changes a
committed install into a failed install.

### 1.2 Single-pass and selective archive extraction

The Windows self-update hot path validates the Release ZIP catalogue once and
streams Core, the Core Skill bundle, and desktop/generation files from that
single catalogue. The local archive path uses the same extractor. Independent
desktop archives and non-Windows paths retain their existing behavior.

Windows Setup no longer calls `Expand-Archive` for the Release payload. It now:

1. validates the complete central directory before creating output;
2. rejects traversal, non-canonical Windows paths, case-folded duplicates,
   file/directory conflicts, reparse points, symlinks, and non-regular files;
3. enforces entry-count, per-file, and total expanded-size limits;
4. creates output files exclusively and verifies exact expanded length; and
5. writes only the files used by Core, Tray, Core Skills, and the WSL helper.

Skipping unrelated archive files reduces temporary writes without weakening the
trust boundary. SHA-256 verification still occurs before extraction.

## 2. Generation retention contract

Cleanup runs only after a Windows generation is committed and independently
reported healthy. It is best-effort and cannot invalidate the committed install.

### 2.1 Normal post-success state

- A fresh install keeps the healthy active generation.
- A successful upgrade keeps the healthy active generation plus exactly one
  explicitly recorded, previously verified fallback generation.
- Older, unreferenced immediate-child generation directories may be removed.

### 2.2 References that always block deletion

The retention collector protects all safe generation references found in:

- the authoritative active pointer;
- the active pointer's fallback;
- the current Installer transaction;
- the current self-update transaction;
- unfinished, failed, or health-sensitive transaction source/target references;
- the current Installer rollback journal, including backup, created, and
  restore-entry paths; and
- any generation needed by an in-progress recovery path.

Malformed authoritative state, an unsafe generation name, or a committed
cross-version upgrade that lacks a verified fallback causes cleanup to stop.
Retaining extra bytes is preferred to guessing and deleting rollback data.

### 2.3 Failure and retry behavior

A delete failure is appended as a terminal warning. The transaction remains
committed and healthy. Repeating cleanup is idempotent: protected generations
remain untouched and a previously locked stale directory can be removed on a
later attempt. Cleanup never follows nested or parent paths outside the direct
`versions` directory.

## 3. Runtime-split candidate

The production WPF publish defaults remain self-contained and single-file.
Setting the candidate-only MSBuild property below changes only an explicit
Actions experiment:

```text
AgentDockRuntimeSplit=true
```

The candidate is framework-dependent and multi-file. It is not connected to
production Setup, generation activation, signing, publishing, or release
workflows. The Actions builder verifies that each architecture contains:

- the `agentdock-tray.exe` AppHost;
- the managed application assembly;
- `.deps.json` and `.runtimeconfig.json`;
- `System.Security.Cryptography.ProtectedData.dll`;
- localized satellite resource assemblies; and
- a runtime requirement for `Microsoft.WindowsDesktop.App`.

Every candidate file is listed with its byte size and SHA-256 hash. The report
compares the framework-dependent ZIP with the existing self-contained
single-file WPF ZIP and records whether the measured archive reduction falls in
the 90–160 MiB exploration window. Missing the exploration window is evidence,
not a reason to mislabel the result.

## 4. Actions isolation and fault injection

`.github/workflows/parallel-installer.yml` is the only workflow added by WB05.
It has `contents: read`, checks out the immutable source commit, uses GitHub-
hosted runners, and records `installation_tests=false` in its evidence.

The workflow performs:

- Linux retention, Installer Engine, and self-update package tests;
- Windows x64 native tests for the same packages;
- Windows x64 Setup archive tests for normal extraction, traversal, duplicate
  entries, symlink/non-regular entries, file/directory conflicts, and timing
  serialization;
- Windows ARM64 test-binary cross-compilation without execution; and
- x64/ARM64 Core and WPF candidate builds.

It does not run Setup, modify a runner's installed AgentDock state, invoke a
release workflow, sign binaries, publish artifacts to Releases, or use a
self-hosted runner.

## 5. Evidence status

The following values must be copied from the successful Actions artifacts after
this branch is pushed. They are intentionally not estimated locally.

| Evidence | x64 | ARM64 |
| --- | ---: | ---: |
| Source commit | pending | pending |
| Baseline WPF ZIP bytes | pending | pending |
| Runtime-split ZIP bytes | pending | pending |
| ZIP reduction MiB | pending | pending |
| Required launch dependencies | pending | pending |
| Localized satellite resources | pending | pending |
| Actions job result | pending | pending |

Real installation time and cold/warm first-ready latency are **not** measured by
this CI workflow. Build duration, ZIP compression time, and test duration are
not substitutes for those measurements.

## 6. Required future machine-level measurement

A release decision still requires an isolated Windows test machine or disposable
VM, explicitly approved for installation testing. The test must not use a
production workstation or the OPPO development node.

For each architecture and packaging option:

1. perform at least five clean-install or clean-upgrade runs;
2. preserve each `setup-timing.json`, Installer transaction/result, active
   pointer, and generation inventory;
3. record time to the first independently healthy Core response;
4. perform at least five cold starts after a reboot or equivalent cache-reset
   boundary;
5. perform at least five warm starts without clearing the OS file cache; and
6. report every sample plus median and p95, not only the best run.

A runtime-split candidate must also be tested with the required Windows Desktop
Runtime absent, present, damaged, and repaired. Until those tests exist, the
runtime-split output remains a size/dependency experiment rather than a release
candidate.

## 7. Rollback of this lane

The implementation is split into reviewable commits. Reverting the WPF
candidate property and this lane's workflow/scripts restores the original WPF
publish defaults. Reverting the Setup streaming commit restores the prior Setup
archive path. Reverting the timing/retention commits restores the prior Installer
result schema and generation cleanup behavior. No production version, tag, or
release asset is modified by this branch.
