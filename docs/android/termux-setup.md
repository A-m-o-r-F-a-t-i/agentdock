# External Termux and Debian setup

## Prerequisites

Use a current official Termux build exposing `RunCommandService`. The APK does not install Termux or Debian silently.

## Export and bootstrap

From **安装与更新**, export:

- `agentdock-workbench`
- `agentdock-workbench-bootstrap.sh`

Copy both into a Termux-visible directory and run the bootstrap manually in Termux. It installs `proot-distro`, curl, jq, coreutils, util-linux, procps, OpenSSL and tar; installs the fixed bridge at `~/.termux/tasker/agentdock-workbench` with mode `0700`; enables `allow-external-apps`; installs Debian when absent; and installs Debian-side CA certificates and command dependencies. It does **not** install a Core release.

Return to Android settings and grant `com.termux.permission.RUN_COMMAND`. The Workbench probe reports Termux absence, missing permission, disabled external-app service, missing Debian/dependencies, compatible stopped Core, healthy Core, or signed-manifest dependency as distinct states.

## Initial flow

1. **probe** — read-only capability/node discovery.
2. **bootstrap** — verify Debian dependencies and initialize private directories/token.
3. **install** — enabled only after a trusted release key/signed manifest contract exists.
4. **start** — persist `desired=running`, launch current Core and require `/healthz` success.

The bound result can return a local Bearer; Android stores it in the Keystore envelope.

## Existing node

Existing nodes are not changed automatically. Adoption requires a compatible managed layout, a path under Termux home and explicit confirmation. Adoption records the selected root without replacing binaries, configuration or versions.

## Update and rollback

Update uses the same signature, digest, structure, version and health gates as install. The node retains active and one previous generation. Rollback swaps the pointers, starts the selected Core, validates health, and restores the original pointer if rollback health fails.

## Diagnostics

`export_diagnostics` writes a tarball in Termux private Workbench state with a bounded status snapshot and the last 1,000 Core log lines after common token redaction. It is not uploaded or shared automatically.
