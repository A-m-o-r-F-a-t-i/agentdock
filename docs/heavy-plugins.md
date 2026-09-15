# Self-contained heavy plugins

AgentDock heavy plugins are real installation units rather than logical groups layered over separately installed capabilities. Each plugin owns its description, document Skills, MCP server definitions, implementation files, assets, and local enable/disable state in one directory.

## Installed layout

```text
~/.agentdock/plugins/
├── .locks/
├── .tmp/
└── <plugin-name>/
    ├── .agentdock-plugin/
    │   ├── plugin.json
    │   └── state.json
    ├── skills/
    │   └── <skill-name>/
    │       └── SKILL.md
    ├── bin/
    ├── scripts/
    └── assets/
```

`plugins/<plugin-name>` is the installed package and the runtime source of truth. AgentDock does not create `plugins/cache`, does not unpack a second runtime copy, and does not maintain a central `plugins.json` member registry.

The hidden store-level `.tmp` and `.locks` directories are only transaction infrastructure. They are not plugin package layers and must not be referenced by a plugin.

## Portable manifest

Every source directory or ZIP must contain exactly one `.agentdock-plugin/plugin.json`:

```json
{
  "schema_version": 1,
  "name": "easyeda-pcb",
  "description": "EasyEDA PCB layout, routing, verification, and automation.",
  "version": "2.0.0",
  "mcpServers": {
    "easyeda-pcb": {
      "description": "EasyEDA PCB tools.",
      "transport": "stdio",
      "command": "bin/easyeda-pcb.exe",
      "args": [],
      "cwd": ".",
      "enabled": true,
      "timeout_ms": 30000
    }
  }
}
```

The package name and directory name must match after installation. Skill members are discovered from `skills/<name>/SKILL.md`; they are not repeated in the manifest. A plugin must contain at least one Skill or MCP server.

For stdio MCP servers, relative `command` and `cwd` values resolve against the installed plugin root. This lets a package carry its own executables, scripts, and supporting files without host-specific absolute paths. HTTP MCP definitions remain ordinary URLs.

`.agentdock-plugin/state.json` is installation-local and is never trusted from a source package. AgentDock creates it during installation:

```json
{
  "enabled": true,
  "skills": {
    "easyeda-pcb-layout-routing": true
  },
  "mcpServers": {
    "easyeda-pcb": true
  }
}
```

Updating a plugin preserves compatible plugin/member switches and enables newly introduced members by default.

## Installation and safety

`plugin_manage` accepts a local directory or ZIP archive:

- `validate`: validate without installing;
- `install`: install a new package directly under `plugins/<name>`;
- `update`: replace an installed package with a same-name package while preserving compatible state;
- `remove`: remove the complete self-contained plugin directory;
- `enable` / `disable`: change the plugin master switch;
- `member_enable` / `member_disable`: change a contained Skill or MCP switch.

Installation rejects symbolic links, ZIP path traversal, unknown manifest fields, duplicate plugin names, duplicate Skill/MCP ownership, unsafe identifiers, oversized packages, invalid Skill documents, and invalid MCP configurations. Replacement is staged on the same filesystem and restores the previous directory if activation fails.

Removing a plugin removes its contained Skills and implementation files because those files belong to the plugin. Separately stored environment values such as `~/.agentdock/env/mcp/<server>.env` are intentionally preserved and can be removed separately when no longer needed.

## Progressive disclosure

An AgentDock client discovers a heavy plugin in two stages:

1. `agentdock_context` exposes only the enabled plugin's name, domain description, Skill count, and MCP server count. Plugin-owned members are omitted from the top-level `skills` and `dynamic_mcp` indexes.
2. `plugin_load` expands one enabled plugin. It returns the enabled Skill descriptions and `skill://` entry points, then lazily connects to contained MCP servers and returns their tool descriptions.

A generic `mcp_tool_search` without an explicit server does not search plugin-owned MCP servers. After `plugin_load`, use the returned qualified tool names, or search again with the returned server name.

## Availability model

Effective member availability is:

```text
plugin enabled
AND member enabled in .agentdock-plugin/state.json
AND MCP enabled in plugin.json (for MCP members)
```

The plugin master switch masks all members without changing their individual switches. Re-enabling the plugin restores each member's previous state. A contained member is managed through the plugin; standalone Skill/MCP install, remove, update, and switch operations reject plugin-owned names.

## Windows control panel

The **Capabilities** page can:

- install a plugin directory or ZIP;
- update an installed plugin from a same-name replacement package;
- enable or disable the whole plugin;
- enable or disable contained Skills and MCP servers;
- remove the complete plugin package;
- show the installed version and directory;
- keep plugin members out of the standalone Skill and MCP lists.

## Migration boundary

The older experimental format used `~/.agentdock/plugins/plugins.json` to group separately installed members. The direct-plugin runtime does not read that file as an installed plugin.

Migration must be explicit: create one package directory per old definition, copy each owned Skill into the package, move each owned MCP definition into the package manifest, remove duplicate standalone registrations from the migrated copy, and preserve member switches in `state.json`. Never perform this conversion silently against a live AgentDock home.

The repository provides an offline Windows migration entry point. Always stop writers first, choose a different destination, and keep the plan outside the repository because it can contain host-specific implementation paths:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File `
  .\scripts\migrate\migrate-agentdock-home.ps1 `
  -SourceHome "$HOME\.agentdock" `
  -DestinationHome "D:\AgentDock-Delivery\.agentdock" `
  -RepositoryRoot "$PWD" `
  -PluginPlanPath "$env:TEMP\agentdock-plugin-migration-plan.json"
```

The script reads the source, writes only the destination, compares a stable source fingerprint before and after migration, replaces bundled Core Skills with the versions from `RepositoryRoot`, creates `migration-report.json`, and restricts the destination ACL to the current Windows user plus `SYSTEM`. It rejects a destination inside the source and verifies that the result contains neither `plugins/cache` nor a central `plugins.json`.
