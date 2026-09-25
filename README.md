<div align="center">

English | [简体中文](./README.zh-CN.md)

<img src="./docs/assets/agentdock-logo.png" alt="AgentDock Workbench logo" width="128" />

# AgentDock Workbench

**AgentDock Workbench 1.1.7** · [Release notes and platform packages](./docs/releases/v1.1.7.md) · [Acceptance matrix](./docs/acceptance-1.1.7.md) · [Layered permissions](./docs/permission-profiles.md) · [Insertion delivery](./docs/insertion-delivery-1.1.7.md). This fork extends [upstream AgentDock](https://github.com/uvwt/agentdock) with its task and execution workbench. Native Windows, Linux and macOS x64/ARM64 packages are built in this repository; the Windows activity-center UI remains platform-specific.

**Give AI agents secure, controlled access to every machine you operate.**

Open ChatGPT in your browser and manage multiple computers and servers from one conversation. Write code, change configuration, run commands, and deploy in the real environment where the work belongs—without consuming a dedicated Codex coding quota.


[Documentation](https://uvwt.github.io/agentdock-docs/) · [Download](https://github.com/A-m-o-r-F-a-t-i/agentdock/releases) · [Community](https://qun.qq.com/universal-share/share?ac=1&authKey=Rp86bSzI7vqm87KoYlKawgsPZ440Ubhyezw6Qkgcn3JISwX3zXxsXkbS5598RrY5&busi_data=eyJncm91cENvZGUiOiIxMDgxMzM3MDE5IiwidG9rZW4iOiJ0Mlg1bUU1ZWtuZzF3SHJDT3pSaGsrOURIMlNYaXBlYllOUjNLZ1BUb1hzM2lJSTZjeVNldzU0ajl0SjRVZkx2IiwidWluIjoiMzIwMjA4ODAzMiJ9&data=W28mWvuqaLf_Fwnf0CgAJXuDs6l3A78V7AoWZnizPboCpKoQMzHzZ-UlluYo47U3tmIBHK2xIgWEVEJbTiGsPQ&svctype=4&tempid=h5_group_info)

[![CI](https://github.com/A-m-o-r-F-a-t-i/agentdock/actions/workflows/ci.yml/badge.svg)](https://github.com/A-m-o-r-F-a-t-i/agentdock/actions/workflows/ci.yml)
[![GitHub Release](https://img.shields.io/github/v/release/A-m-o-r-F-a-t-i/agentdock?display_name=tag&logo=github)](https://github.com/A-m-o-r-F-a-t-i/agentdock/releases)
[![License](https://img.shields.io/github/license/uvwt/agentdock)](./LICENSE)

</div>

<p align="center">
  <img
    src="./docs/assets/agentdock-multi-device.png"
    alt="AgentDock: operate multiple devices from one AI conversation"
    width="100%"
  />
</p>

## What is AgentDock?

AgentDock is an independent tool runtime for AI agents.

It provides unified, secure, and controlled file, command, Git, Skill, MCP, browser automation, and task execution across local computers, remote servers, and containers. Connect multiple AgentDock instances to coordinate work across devices and finish multi-machine workflows in a single conversation.

AgentDock does not provide a chat interface or perform model inference. It focuses on one responsibility:

> Let AI agents operate real environments within explicit permission boundaries and return structured, traceable, and verifiable results.

```text
              ChatGPT / Claude / Codex
                        │
                        │ MCP (multiple instances supported)
          ┌─────────────┼─────────────┐
          ▼             ▼             ▼
   ┌───────────┐ ┌───────────┐ ┌───────────┐
   │ AgentDock │ │ AgentDock │ │ AgentDock │
   │ Local Mac │ │ LAN Host  │ │ Cloud VPS │
   └─────┬─────┘ └─────┬─────┘ └─────┬─────┘
         │             │             │
         ▼             ▼             ▼
   Files · Shell · Git  Tunnels       Proxy · Deploy
```

## What can AgentDock do?

- Manage multiple computers and servers directly from ChatGPT without repeatedly switching SSH sessions
- Write code, modify projects, run tests, and operate Git in the real local or remote environment without depending on a dedicated coding-agent quota
- Manage VPS hosts, Docker services, reverse proxies, and deployment configuration
- Inspect logs, processes, ports, and actual runtime state
- Operate authenticated web pages and macOS desktop applications
- Connect multiple AgentDock instances and coordinate cross-device work in one conversation
- Extend capabilities through Skills and dynamic MCP servers
- Persist long-running task state and continue after an interruption
- Use the same tool model across macOS, Linux, Windows, and containers
- And more

## This fork and upstream AgentDock

This README keeps the comparison concise. See [the detailed feature differences and migration guide](./docs/official-version-differences-and-migration.md) for the fork's plugin model, `AGENTS.md` injection, execution center, Windows desktop extensions, and state boundaries. Switching between the upstream release and this fork requires uninstalling the previous distribution and starting with clean state, or converting only reviewed text configuration in a backup copy; credentials and runtime journals must not be copied directly.

## Quick start

Regular users can install AgentDock from the official package for their operating system. You do not need the source code or Go.

See [Install AgentDock](https://uvwt.github.io/agentdock-docs/docs/getting-started/install) for the complete instructions.

| Platform | Documentation |
| --- | --- |
| Docker | [Docker installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/docker) |
| Linux | [Automated Linux installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/linux) |
| Linux / VPS | [Manual systemd deployment](https://uvwt.github.io/agentdock-docs/docs/getting-started/vps) |
| macOS | [macOS installation](https://uvwt.github.io/agentdock-docs/docs/getting-started/macos) |
| Windows | [Graphical Windows installer](https://uvwt.github.io/agentdock-docs/docs/getting-started/windows) |

### Choose a connection option

- **Local only:** the client and AgentDock run on the same computer.
- **Temporary public address:** ChatGPT, a phone, or another remote device needs access and no domain is ready. The address may change after the Tunnel restarts.
- **Fixed domain:** a stable address for long-term use. Requires a Cloudflare-managed domain and Tunnel Token.
- **Tailscale Funnel (Windows Desktop):** use the signed-in device's `.ts.net` HTTPS origin without a Tunnel Token. AgentDock forwards its complete origin, retains Bearer/OAuth authentication, and only removes mappings it owns. See [configuration, conflicts and recovery](./docs/tailscale-funnel.md).

After installation, get the MCP URL and Bearer Token or OAuth sign-in details from the control panel or terminal, then add them to the MCP, Tools, or Connectors settings in your client. Public access must keep authentication enabled. Do not include credentials in screenshots, issues, or public conversations.

## Connect an AI client

AgentDock exposes tools over MCP Streamable HTTP. The exact client syntax varies, but a typical configuration looks like this:

```json
{
  "mcpServers": {
    "agentdock": {
      "url": "http://127.0.0.1:8765/mcp",
      "headers": {
        "Authorization": "Bearer <AGENTDOCK_AUTH_TOKEN>"
      }
    }
  }
}
```

## Core capabilities

### Files and commands

- Read and search UTF-8 text, traverse directories, and apply structured edits
- Atomic file writes, path boundaries, and private-directory protection
- Command execution with timeout and output limits
- Separate stdout, stderr, and exit status
- Long-running command sessions, PTY, observation, input, and termination
- Output truncation and sensitive-value redaction
- macOS, Linux, Windows, and WSL support

### Skills and dynamic MCP

Official and community Skill sources live in [uvwt/agentdock-skills](https://github.com/uvwt/agentdock-skills). This repository only keeps core Skills that must ship with the AgentDock runtime, including bootstrap/security Skills and the built-in `agentdock-user-guide` official user guide.

- Validate, install, uninstall, activate, enable, disable, and roll back Skill packages
- Store active Skills in a [Codex-style visible directory tree](docs/skill-directory-layout.md) while keeping inactive versions and transaction state hidden
- Stable, development, canary, and pinned release channels
- Isolated environment variables and runtimes for each Skill
- Register, enable, disable, refresh, and remove dynamic MCP servers
- Streamable HTTP and stdio transports
- Search tools, inspect schemas, and perform controlled calls
- Configuration isolation between MCP servers
- Install [self-contained heavy plugins](docs/heavy-plugins.md) that directly carry their Skills, MCP definitions, implementation files, switches, and two-stage progressive disclosure without a plugin cache layer

### Native ACP

AgentDock can optionally act as a native ACP client and host a local coding-agent adapter.

- Desktop control panels provide presets for Codex, Claude, and Grok; host configuration controls whether ACP is enabled and which adapter is selected.
- Use `acp_session` to create and manage sessions, `acp_prompt` to run and observe prompts, and `acp_interaction` to answer agent permission requests.
- Optional ACP operations are available only when the connected adapter advertises the corresponding capability.
- ACP working directories follow the host process or container security boundary rather than an AgentDock filesystem allowlist.

### Browser and desktop automation

- Start, close, and clean up browser sessions
- Navigate, click, type, select, and wait
- Inspect page text, interactive elements, errors, and network responses
- Persist login state, use dedicated browser profiles, and capture screenshots
- Use system Chrome and macOS desktop automation

### Recoverable tasks

- Persist task state
- Define explicit goals, steps, and completion conditions
- Record staged checkpoints
- Track blockers and resume after interruption
- Perform final review and evidence-based completion checks
- Reuse workflow templates

### Recall and NexusDock integration

AgentDock can optionally pair with NexusDock as a multi-device aggregation entrypoint:

- Long-term project memory
- Runbooks and experience records
- Workflow templates
- Private notes
- Multi-device state coordination
- Temporary signed Artifact downloads proxied by NexusDock while the source node is online

## Runtime directories

| Path | Purpose |
| --- | --- |
| `~/AgentDock` | Default working directory for relative file operations |
| `~/.agentdock` | AgentDock state, configuration, sessions, and extension data |

## Ports

Default MCP URL for Docker, native installs, and local development:

`http://127.0.0.1:8765/mcp`

Ports are configurable. Clients must use the address defined by the actual deployment.

For public deployments, enable Bearer Token or OAuth authentication and use HTTPS. Never expose an unauthenticated MCP service to the public internet.

## Development and contribution

Run the full check before submitting code:

```bash
make check
```

GitHub Actions continuously run tests, static checks, builds, and release validation.

User documentation is maintained separately in [`uvwt/agentdock-docs`](https://github.com/uvwt/agentdock-docs). Changes to user-visible behavior, configuration, installation, or tool schemas should update the matching documentation in the same change set.

Changes involving device pairing, cross-node tool routing, Recall, or Workflow integrations may also require coordinated changes in [`uvwt/nexusdock`](https://github.com/uvwt/nexusdock). The shared protocol is maintained in [`uvwt/agentdock-protocol`](https://github.com/uvwt/agentdock-protocol). When changing shared interfaces or data structures, update the protocol definitions first, then align both implementations and their protocol dependency versions, check compatibility, update the corresponding documentation, and link related cross-repository changes in the PR.

Submit bugs and feature requests through [GitHub Issues](https://github.com/A-m-o-r-F-a-t-i/agentdock/issues).

## Support the project

<p>If <b>AgentDock</b> helps you, please consider giving it a <b>Star</b> ⭐. Thank you for your support</p>
<table>
<thead>
<tr>
<th align="center">WeChat</th>
<th align="center">Alipay</th>
</tr>
</thead>
<tbody><tr>
<td align="center"><img src="./docs/assets/donation/wechat-cropped.JPG" alt="WeChat donation QR code" height="200"></td>
<td align="center"><img src="./docs/assets/donation/alipay.JPG" alt="Alipay donation QR code" height="200"></td>
</tr>
</tbody>
</table>

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=uvwt/agentdock&type=Date)](https://star-history.com/#uvwt/agentdock&Date)

## Related links

- [Documentation](https://uvwt.github.io/agentdock-docs/)
- [Documentation source](https://github.com/uvwt/agentdock-docs)
- [GitHub Releases](https://github.com/A-m-o-r-F-a-t-i/agentdock/releases)
- [GitHub Container Registry](https://github.com/uvwt/agentdock/pkgs/container/agentdock)
- [Docker Hub](https://hub.docker.com/r/agentdockio/agentdock)
- [Linux Do](https://linux.do/)

## License

Apache License 2.0. See [LICENSE](./LICENSE).

## Community

[Join the QQ group (1081337019)](https://qun.qq.com/universal-share/share?ac=1&authKey=Rp86bSzI7vqm87KoYlKawgsPZ440Ubhyezw6Qkgcn3JISwX3zXxsXkbS5598RrY5&busi_data=eyJncm91cENvZGUiOiIxMDgxMzM3MDE5IiwidG9rZW4iOiJ0Mlg1bUU1ZWtuZzF3SHJDT3pSaGsrOURIMlNYaXBlYllOUjNLZ1BUb1hzM2lJSTZjeVNldzU0ajl0SjRVZkx2IiwidWluIjoiMzIwMjA4ODAzMiJ9&data=W28mWvuqaLf_Fwnf0CgAJXuDs6l3A78V7AoWZnizPboCpKoQMzHzZ-UlluYo47U3tmIBHK2xIgWEVEJbTiGsPQ&svctype=4&tempid=h5_group_info)
