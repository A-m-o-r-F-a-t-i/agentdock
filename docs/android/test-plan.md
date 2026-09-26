# WB07 candidate validation plan

Android dependency resolution, compilation, lint, unit tests, instrumentation, screenshots and APK packaging run only in GitHub Actions. The phone worktree performs source editing, shell syntax/contract checks, XML parsing and Git operations.

## Automated gates

1. **Static contract**
   - verify exact checkout SHA and owned-path boundary;
   - parse XML and shell syntax;
   - run isolated Termux bridge contract tests;
   - scan for forbidden ADB/root/accessibility/background tricks;
   - verify version linkage and absence of publish/release steps.
2. **Android build**
   - JDK 17, Gradle 9.4.1 and Android SDK 37;
   - lint and JVM unit tests;
   - debug APK, release-shaped test-signed APK and instrumentation APK;
   - SHA-256 manifest, metadata and reports uploaded even on failure.
3. **Emulator matrix**
   - API 33, 34, 35 and 37 x86_64 Google APIs;
   - navigate every page in explicit debug fixture mode;
   - capture overview, tasks, permissions, install/update and settings;
   - upload per-API logs, reports and screenshots even on failure.
4. **Evidence aggregation**
   - record every job conclusion, SHA, run ID/attempt and artifact name;
   - label signing as test-only;
   - distinguish emulator evidence from physical ARM64/Termux evidence;
   - explicitly record unrun 30-minute, 2-hour and 8-hour cases;
   - state that no release, tag, merge or production mutation occurred.

## Physical-device evidence not proven by emulator

- external Termux permission and callback on Android 14/ARM64;
- Debian PRoot and real Linux ARM64 Core launch;
- Keystore token handoff to a live Core;
- signed release manifest integration after WB05 publishes its contract;
- real start/stop/restart/repair/update/rollback;
- screen-off, Doze, swipe-away and reboot behavior;
- 30-minute, 2-hour and 8-hour duration runs;
- manufacturer-specific battery-management guidance.

## Interpretation rules

Fixture screenshots prove navigation/layout only. Empty inventory plus endpoint failure is not a passing empty state. SHA-256 without publisher signature is not authenticity. Emulator success is not ARM64 PRoot evidence. A queued Termux command is not success; only a bound validated callback is terminal evidence.
