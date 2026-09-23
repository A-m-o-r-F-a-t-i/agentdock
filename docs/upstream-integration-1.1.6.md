# AgentDock 1.1.6 upstream integration

## Frozen graph

- User fork: `e7ef305d1f8a231dba038af43f7f8b72fff1f33d`.
- Upstream `uvwt/agentdock`: `b20dc723342bd765fa0c32d172eeca44fa02a8ea`.
- Merge base: `ad51001515a2b1b82baa31281970e0b9f67f28e9`.
- A real `--no-ff --no-commit` merge was used. Text and semantic conflicts were resolved by capability, not by the `ours` merge strategy.

## Retained product boundaries

The user fork remains authoritative for the activity center, automatic conversation/task binding, insertion and stop, approval policy, native Setup broker, and authenticated trial-generation startup. The 120-second activity, 180-second insertion eligibility and 300-second queued insertion expiry remain independent.

The existing `skill_package` version/activate/rollback lifecycle and `.system` classification remain. The upstream current-only migration and removal of version history are competing lifecycle designs and are not enabled. Existing `skill` / `skill_env` command selection remains on one resolver alongside exact scoped `skill_ref`; shared/workspace/plugin references cannot borrow managed credentials.

Plugins remain installed in `plugins/<name>` with their existing native MCP names, Heavy and per-member settings, `.state`, `.config` and `.data`. Source/format/catalog adapters from upstream use this same Store. A second Plugin Manager registry, immutable plugin-version layout, pending-finalize journal and purge-on-remove behavior were not introduced. Default removal preserves user data and credentials. New adapter source rebinding requires explicit confirmation.

## Integrated independent improvements

- Portable, OpenAI/Codex and Claude source normalization, local/archive/Git sources, catalog review, source provenance and explicit source-binding confirmation.
- Workspace/shared exact Skill references, issued workspace identity, path and symlink scope checks. The workspace-only entry is optional; the fork's complete `agentdock_context` and configured AGENTS source are preserved.
- Managed Skill read leases coordinated with the existing activation/uninstall write lock. Background command readers release on process completion, not RPC return.
- Reject FIFO/device/special files before hashing or copying Skill packages. Existing package safety checks remain.
- Native Windows host entry points, ABI-safe Task Scheduler VARIANT sizing, structured UTF-8 failure diagnostics, timeout ordering, and asynchronous Quick Tunnel startup without blocking installer commit on public-network readiness.
- Setup keeps the fork's native GUI broker and scoped trial authorization. An unverified caller `MarkHealthy` flag cannot promote stored health.

## Verification boundary

The local test suites use isolated temporary stores and command fixtures. Desktop compilation and pure model assertions do not constitute visual or installed-product acceptance. No production installation, startup replacement, lock fault injection or upgrade was run.

Verified on the isolated Windows development worktree: `go build ./...`; `go test ./... -p 2 -count=1 -timeout=8m` (all packages passed, no unexplained failure); .NET Release control-panel build (0 warnings, 0 errors); 615 desktop pure-model assertions; and syntax parsing of five installation/launcher/test scripts. The final whole-suite command completed successfully after fixing the missing workspace-only registration/read-only classification, exact resource selection, retained environment precedence and updated contract fixtures. These results do not claim the 1.1.6 performance or UI work is complete.

## Upstream commits considered

- 3c95749b4c74572901a1264423bf7b026da56b98 feat(context): 引入独立 workspace_context 工作区上下文 (#116)
- 98085e9cb319564c21bfb0baf07941e580bb7185 fix(context): 阻止工作区 Skill 父路径逃逸
- fe7f79e244ef4e05097590429e931fdaf0ab82e8 fix(installer): 收敛跨平台更新与后台启动
- d59f31a5398bc0736a3c165640c07cbe0b03969d fix(installer): 收敛 Tunnel 异步启动语义
- f520a510c9df9b7facaaadf1ce38f6001dbefd6f fix(windows): 对齐更新下载进度协议
- 58350ba2bd2a083775ae9f639b55e0450983b0bb fix(macos): 防止菜单栏状态持久隐藏
- 2664f2ea6abb23825d73afe136292c4e374f436e fix(windows): 收敛 Setup 激活与错误展示
- 7c4009a0fa420718e0358bf79d47f7e67d5190b7 fix(windows): 兼容旧版在线更新并迁移 generation
- 7c060c07ff09e60e8c4ba3cfe0dead894e30018e fix(windows): 加固旧版在线迁移重入
- eeef02a301c59287acf5e776f8517ff44324bed9 fix(windows): 收敛旧版迁移失败重试
- 53b9a0b1b36288dd4c4671e225ba568e8170cebe docs(windows): 对齐旧版迁移状态注释
- f333e9e0d2c63141ce49a829d84df0dbff598607 test(windows): 补齐旧版迁移凭据状态
- 68782ab646ebf82d0e3b58515ef16eef5a22173d fix(windows): 重试短暂文件替换冲突
- 174237ab29545cfbb091fcaeb28e5d8eb5da3dec feat(skill): 收敛 Skill 当前内容模型 (#127)
- fdb19a94c87315b160a1f4bc89a7d7f1ad9aab49 fix(skill): 让旧布局迁移跟随更新事务 (#128)
- 983c97d705b0e2023675778ef08c0361d0bfc09a fix(skill): 修正 read_file Skill 资源描述 (#129)
- 9071556c43c6181a1583926aa6d014b58c88c25a feat(plugin): 建立 Plugin Core (#130)
- b20dc723342bd765fa0c32d172eeca44fa02a8ea feat(plugin): 接入主流 Plugin Adapter (#131)
