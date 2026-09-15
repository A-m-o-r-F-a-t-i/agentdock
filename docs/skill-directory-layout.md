# Codex-style Skill directory layout

AgentDock stores document Skills under a Codex-compatible visible tree while retaining AgentDock's version selection, rollback, switch, and transaction semantics.

## Layout

```text
~/.agentdock/
├── AGENTS.md
├── skills/
│   ├── <skill-name>/
│   │   └── SKILL.md
│   ├── .system/
│   │   ├── .agentdock-system-skills.marker
│   │   └── <bundled-skill>/
│   │       └── SKILL.md
│   ├── .versions/
│   │   └── <skill-name>/<version>/
│   ├── .state/
│   │   └── <skill-name>.json
│   ├── .cache/
│   ├── .locks/
│   └── .tmp/
├── env/
│   ├── skill/<skill-name>.env
│   └── mcp/<server-name>.env
├── skill-data/
│   └── <skill-name>/
├── plugins/
└── mcp/
```

The visible Skill locations mirror Codex:

- `skills/<skill-name>` is the active user-installed Skill;
- `skills/.system/<skill-name>` is the active AgentDock-bundled Skill.

AgentDock-specific version management stays hidden:

- `.versions` contains installed versions that are not active;
- `.state` records the active semantic version, rollback history, base enabled state, and whether the active package is a system Skill;
- `.locks` serializes install, activation, rollback, bundle bootstrap, and uninstall operations;
- `.tmp` keeps transaction staging on the same filesystem so directory swaps can use atomic rename;
- `.cache` is reserved for validated downloads and package cache entries.

Skill environment files and private runtime data remain outside the package directory. Updating, rolling back, or uninstalling package files therefore does not silently overwrite credentials or delete user state.

## Installation and activation

A newly installed but inactive version is copied to:

```text
~/.agentdock/skills/.versions/<skill-name>/<version>/
```

Activation moves the target version to the visible Codex-style path. If another version is active, AgentDock first moves that directory back into `.versions`, then moves the target directory into place and atomically updates `.state`. If the state update fails, the directory moves are reversed.

Bundled release Skills use the same version archive but activate into `.system`. The marker file identifies an AgentDock-managed system tree without duplicating the bundled member list; system membership is represented by the active directory class and the corresponding state entry.

## Compatibility boundary

This source change intentionally does **not** read, rename, merge, or remove the previous layout:

```text
~/.agentdock/skill-store/
```

There is no automatic migration during startup, installation, bundle bootstrap, or update. A future migration must be an explicit operation with preflight checks, conflict handling, rollback, and user-visible results. Until such a migration is implemented, a binary using the new layout starts with the `skills` tree independently of any existing `skill-store` tree.

Public Skill operations remain stable:

- `skill_package install`, `activate`, `rollback`, `enable`, `disable`, and `uninstall` keep their existing meanings;
- `skill://<name>/...` always resolves through the current active visible directory;
- `exec_command` with `skill: "<name>"` binds the active visible directory and the separately managed Skill environment;
- standalone Skills use this tree; Skills carried by a self-contained heavy plugin remain inside `~/.agentdock/plugins/<plugin>/skills/<name>` and are managed through `plugin_manage`.

Callers must not construct `.versions`, `.state`, `.locks`, or `.tmp` paths. Use the Skill tools and resource APIs instead.
