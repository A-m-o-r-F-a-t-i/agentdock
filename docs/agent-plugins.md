# Agent Plugins 1.0.0 与 Windows 运行配置

AgentDock 将插件包、宿主状态、凭据分别管理。插件目录就是分发单元，不嵌套缓存目录。普通插件直接暴露成员，Heavy 插件按需展开。

## 标准目录与清单

```text
~/.agentdock/plugins/
├── <name>/
│   ├── plugin.json
│   ├── mcp.json
│   ├── skills/<skill-name>/SKILL.md
│   └── scripts、bin、其他随包资源
├── .state/<name>.json
├── .config/<name>.json
├── .data/<name>/
├── .tmp/
└── .locks/
```

根清单最低要求为 `$schema` 和 `name`。版本、描述和成员均可省略。包名使用 1—64 位小写字母、数字、点或连字符，首尾为字母或数字，无连续点或连字符。MCP 配置只从根 `mcp.json` 加载，Skill 只从 `skills/` 的直接子目录发现。原 `.agentdock-plugin/plugin.json` 不再参与运行时解析。

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "portable-demo",
  "version": "1.0.0",
  "description": "Portable Skills and MCP tools",
  "extensions": {
    "io.github.uvwt.agentdock": { "heavy": true }
  }
}
```

扩展键必须采用标准规定的反向域名命名空间。因此 `heavy` 放在 `extensions.io.github.uvwt.agentdock` 内，该命名空间只定义此一个参数。其他客户端忽略不认识的命名空间，仍能加载标准成员。

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "demo": {
      "type": "stdio",
      "command": "node",
      "args": ["${PLUGIN_ROOT}/server.mjs"],
      "cwd": "${PLUGIN_ROOT}"
    }
  }
}
```

支持 `stdio` 与 `streamable-http`，通信继续使用 MCP Go SDK。旧 `sse` 传输不实现，遇到时跳过该服务器并给出诊断。可移植配置不包含 AgentDock 的 `enabled`、`timeout_ms`、`header_env` 等字段。标准 `env` 和 `headers` 是公开的固定值，不能用于嵌入秘密。

包内 stdio 命令必须为 `./` 路径或单个可执行程序名；工作目录省略时为插件根。`${PLUGIN_ROOT}`、`${PLUGIN_DATA}` 只在 args、env 值和 cwd 中进行一次展开。未知占位符保持原文，HTTP URL/headers 不做环境变量替换。文件系统解析后的包路径和工作目录必须留在各自允许的根目录，配置的 HTTP 头不会经重定向发送给其他源。

## 宿主状态与加载规则

`.state/<name>.json` 保存总开关、成员开关及可选 Heavy 覆盖值。`.config/<name>.json` 保存本机 MCP 说明、超时、可执行程序覆盖及变量引用，实际秘密继续保存在已有 `env/mcp` 存储。`.data/<name>/` 作为 PLUGIN_DATA 跨版本保留。插件更新不覆盖这些本机数据，删除插件默认保留数据和凭据。

普通插件的已启用成员直接进入 `agentdock_context` 的 Skill/MCP 列表，普通工具搜索可命中其 MCP。Heavy 插件只进入 plugins 摘要列表，调用 `plugin_load` 后返回成员及服务器限定工具名。Heavy 是可见性策略，不构成安全沙箱；认证、路径约束和执行权限由宿主提供。

“功能与插件”页只管理已安装的能力，提供刷新、收起/展开、Heavy、启停、成员开关和删除。插件包安装/更新按钮、选择对话框与对应 GUI 调用均已移除；Core 自身的更新入口保留。

GUI 的 Heavy 开关覆盖包内默认值，不修改分发清单。`plugin_manage` 新增 `heavy_enable`、`heavy_disable`，其余安装、更新、启停和成员管理接口保留。卡片默认收起，展开状态在当前控制面板刷新间保留，不在独立 Skill/MCP 列表重复显示插件成员。

未知清单顶层字段报告并忽略；错误的必填元数据拒绝该插件。非法单个 Skill/MCP 只跳过相应成员，其他有效成员继续可用。诊断显示在插件卡片内。`plugin validate` 命令在出现诊断时返回非零，适合发布前严格检查。

## 安装和迁移

Windows 安装程序和控制面板统一使用 AgentDock 名称，安装文件为 `AgentDockSetup-amd64.exe` 或 `AgentDockSetup-arm64.exe`。安装向导显示“选择安装位置”，支持手动输入和浏览目录；升级时预填原安装位置。安装与卸载均使用所选目录，不改变独立的 `.agentdock` 用户数据目录。

已有有效安装时，升级继续使用原目录，避免两处安装争用同一开机启动项和端口。更换位置需先卸载并选择保留用户数据，再重新安装并配置连接；仅有卸载注册表残项且原目录已失效时不会阻止新位置安装。

Windows Setup 的安装事务会迁移已安装的旧私有插件格式，先记录恢复快照、停止旧实例、写入标准清单和外部状态，验证后再启动新版。失败通过同一安装事务恢复旧包与配置。独立的离线转换命令带持久恢复日志，重复运行可先恢复未结束的转换再重试：

```powershell
agentdock plugin migrate --home <停止写入后的绝对数据目录>
agentdock plugin validate --source <插件目录或ZIP>
agentdock plugin list --home <绝对数据目录>
```

转换保留既有成员开关和环境变量引用，把 Skill 顶层 version 移到 metadata.version，不复制或公开凭据值。私有格式仅出现在一次性迁移边界和回归样本中。源/目标清单同时存在等冲突会明确报错，不猜测哪份配置应覆盖另一份。

整个旧用户目录需要转成副本时，可运行 `scripts/migrate/migrate-agentdock-home.ps1` 并提供当前 `-AgentDockBinary`。旧逻辑分组还需显式插件计划。原目录不变，目标目录受 ACL 保护。不要把用户目录、迁移计划、环境文件和生成的本机配置提交到公共仓库。

从私有格式升级请使用本版 Setup；普通 Core 启动和旧版本在线自更新机制不自动执行本次迁移。使用备份回退到旧 Core 时，应同时恢复同一份旧格式用户数据。

## 安装残余参数修复

安装器的 Skill 自举和 `skill bootstrap` 只解析存储位置，不再调用完整运行环境校验。旧 `AGENTDOCK_INSTRUCTIONS_FILE` 指向已删除文件、错误的 ACP/浏览器/端口变量均不会阻断自举。存储路径与权限仍严格检查。

Windows Desktop 启动由运行清单、控制面板设置和受保护凭据重建受管环境。未显式保存的旧额外指令路径不再自动继承；显式保存的新配置仍需通过校验，避免静默丢失用户规则。默认工作区优先采用显式设置与运行清单，旧环境中现存的有效绝对工作区可作为升级回退值。

## 运行配置页面

“运行配置”页提供默认全局工作区、AGENTS.md 自动加载、额外指令文件、浏览器程序、可信代理 CIDR、命令环境变量引用、ACP 并发提示数和超时。保存前校验，成功后重启；失败恢复原设置，并独立于请求取消尝试恢复旧实例。数据目录及配置文件位置只读展示。

配置保存在 `control-panel-settings.json.runtime_options`，默认工作区同步运行清单。`config runtime-get` 和 `config runtime-update` 为 GUI 提供同一原生 CLI 接口，不依赖正在运行的 Core。已有高级设置页负责端口、日志、MCP Apps、浏览器连接模式、ACP Profile 等参数，公网设置与凭据仍由专门页面管理。

## 验证与依据

- `go test ./...`、`go vet ./...` 和 WPF Windows 构建覆盖运行时与桌面修改。
- `internal/plugin/standard_test.go` 验证最小清单、组件失败隔离、字段和路径约束、单次展开；客户端测试验证头部优先级和跨源阻断。
- `internal/app/plugin_standard_test.go` 验证普通/Heavy 动态切换、无版本 Skill 和包内容不可变；迁移测试验证回滚、重试和凭据不改写。
- `internal/plugin/testdata/portable-demo` 是带标准 Skill 和 stdio MCP 的跨客户端样本。独立官方 JSON Schema 校验不替代客户端实际安装与工具调用测试。
- YAML 使用 `gopkg.in/yaml.v3` 解析，支持 Agent Skills 的引号、多行值和元数据，避免手工拆行误判。

规范原文：[Agent Plugins 1.0.0](https://agent-plugins.org/specification)、[Agent Skills](https://agentskills.io/specification)、[MCP](https://modelcontextprotocol.io/specification)。官方 JSON Schema 保存在 `internal/plugin/schemas/`，加载插件时不联网下载 Schema。客户端兼容声明应标明实际测试版本和范围，不用单次安装结果代替所有 Agent 的完整验证。
