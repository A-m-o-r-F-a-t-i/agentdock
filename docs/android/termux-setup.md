# External Termux and Debian setup

## Prerequisites

Use a current official Termux build exposing `RunCommandService`. The APK does not install Termux or Debian silently.

## Export and bootstrap

From **安装与更新**, export:

- `agentdock-workbench`
- `agentdock-workbench-bootstrap.sh`

Copy both into a Termux-visible directory and run the bootstrap manually in Termux. It installs `proot-distro`, curl, jq, coreutils, util-linux, procps, OpenSSL, tar and Python; installs the fixed bridge at `~/.termux/tasker/agentdock-workbench` with mode `0700`; enables `allow-external-apps`; installs Debian when absent; and installs Debian-side CA certificates and command dependencies. It does **not** install a Core release.

Return to Android settings and grant `com.termux.permission.RUN_COMMAND`. The Workbench probe reports Termux absence, missing permission, disabled external-app service, missing Debian/dependencies, compatible stopped Core, healthy Core, or signed-manifest dependency as distinct states.

## Initial flow

1. **probe** — read-only capability/node discovery.
2. **bootstrap** — verify Debian dependencies and initialize private directories/token.
3. **install** — enabled only after a trusted release key/signed manifest contract exists.
4. **start** — persist `desired=running`, launch current Core and verify the owned process identity and an authenticated management response.

Callbacks intentionally carry no Bearer. Use the explicit Core connection editor for an authorized credential. Automatic pairing remains pending a dedicated shared Core contract; never paste credentials into RUN_COMMAND arguments or shared files.

## Existing node

Existing nodes are not changed automatically. Adoption requires a compatible managed layout, a path under Termux home and explicit confirmation. Adoption records the selected root without replacing binaries, configuration or versions.

## Update and rollback

The candidate contains signature/digest gates and bounded archive extraction. Existing version directories are not overwritten and fallback cleanup is deferred. Complete transaction resumption, data-schema compatibility and single-fallback retention are not yet accepted. Candidate installation or takeover of the current phone node is outside this development run.

## Diagnostics

`export_diagnostics` writes a tarball in Termux private Workbench state with a bounded status snapshot and the last 1,000 Core log lines after common token redaction. It is not uploaded or shared automatically.
