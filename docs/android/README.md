# AgentDock Workbench for Android

WB07 adds a native **Kotlin + Jetpack Compose** Workbench while keeping the AgentDock Core and Linux environment outside the APK:

```text
Android Workbench APK
  ├─ Compose UI, Android settings and lifecycle controls
  ├─ Core HTTP/SSE client and Keystore credential envelope
  └─ Termux RUN_COMMAND bridge
          ↓ explicit user grant
External Termux → Debian PRoot → AgentDock Linux ARM64 Core
```

The APK does not bundle Termux, a Linux root filesystem, Chromium, ADB, root, Shizuku, accessibility automation, or the Go Core. It does not claim permanent background survival.

## Candidate state

- Native destinations cover overview, workspaces, conversations, the independent task center, activity/calls, insertion/stop, approvals, permissions, Skill, plugins/MCP, Core connections, install/update, projects/files, diagnostics and settings.
- Real data comes from Core HTTP/SSE. Fixture data is opt-in and unavailable in non-debug builds.
- Termux requests use a fixed path, closed operation vocabulary, bounded JSON stdin, operation/request IDs, a random nonce and a one-shot callback PendingIntent.
- The bridge now provides read-only discovery, bounded private requests, credential-free callbacks, guarded archive extraction and process identity checks. Installation journaling, schema-aware rollback and automatic retention remain incomplete; do not use this candidate to take over an existing node.
- Install/update intentionally returns `pending_manifest` until the release lane supplies a trusted Ed25519 key and signed Linux ARM64 manifest. A SHA-256 sidecar alone is not publisher authentication.
- Candidate APKs are test-signed and are never published by the WB07 workflow.

See the adjacent documents for architecture, security, setup, lifecycle, compatibility, feature mapping and validation.
