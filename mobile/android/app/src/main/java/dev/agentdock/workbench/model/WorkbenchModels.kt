package dev.agentdock.workbench.model

import org.json.JSONObject

/** Every Windows Workbench capability has a mobile destination; compact layout never deletes a page. */
enum class WorkbenchScreen(val route: String, val title: String, val section: String) {
    Home("home", "首页", "总览"),
    Workspaces("workspaces", "工作区", "总览"),
    Conversations("conversations", "对话", "执行"),
    Tasks("tasks", "任务中心", "执行"),
    Activity("activity", "活动流", "执行"),
    CallDetail("calls", "调用详情", "执行"),
    InsertAndStop("insert", "插入与停止", "执行"),
    Approvals("approvals", "审批", "安全"),
    Permissions("permissions", "权限", "安全"),
    Skills("skills", "Skill", "能力"),
    Plugins("plugins", "插件与 MCP", "能力"),
    CoreConnections("connections", "Core 与连接", "节点"),
    InstallUpdate("install", "安装与更新", "节点"),
    ProjectsFiles("projects", "项目与文件", "节点"),
    LogsDiagnostics("diagnostics", "日志与诊断", "系统"),
    Settings("settings", "设置", "系统");

    companion object {
        fun fromRoute(value: String?): WorkbenchScreen = entries.firstOrNull { it.route == value } ?: Home
    }
}

data class WorkbenchSettings(
    val schemaVersion: Int = 1,
    val theme: String = "system",
    val language: String = "system",
    val density: String = "comfortable",
    val endpoint: String = "http://127.0.0.1:8765",
    val remoteEndpointEnabled: Boolean = false,
    val notificationsEnabled: Boolean = true,
    val guardianEnabled: Boolean = false,
    val guardianPaused: Boolean = false,
    val autoRepairEnabled: Boolean = false,
    val bootHealthCheckEnabled: Boolean = false,
    val guardianIntervalMinutes: Int = 15,
    val onlyOnWifi: Boolean = false,
    val onlyWhileCharging: Boolean = false,
    val allowMobileData: Boolean = false,
    val publicAccessEnabled: Boolean = false,
    val detailedCalls: Boolean = false,
    val toolOutputEnabled: Boolean = true,
    val toolOutputMaxChars: Int = 20_000,
    val customPermissionEnabled: Boolean = false,
    val permissionFilesystem: String = "write",
    val permissionNetwork: String = "allow",
    val permissionBoundary: String = "none",
    val approvalPolicy: String = "on-request",
    val approvalReviewer: String = "user",
    val granularFileWrites: Boolean = true,
    val granularCommands: Boolean = true,
    val granularNetwork: Boolean = true,
    val granularMcp: Boolean = true,
    val granularManagement: Boolean = true,
    val granularOther: Boolean = false,
    val desiredNodeState: String = "stopped",
    val projectTreeUri: String = "",
    val artifactTreeUri: String = "",
    val onboardingComplete: Boolean = false
) {
    init {
        require(guardianIntervalMinutes in 15..1440)
        require(toolOutputMaxChars in 1_000..100_000)
    }
}

object TimeSemantics {
    const val RECENT_ACTIVITY_SECONDS = 120L
    const val INSERT_AND_STOP_ELIGIBILITY_SECONDS = 180L
    const val INSERTION_VALIDITY_SECONDS = 300L
    const val RECEIPT_WAIT_SECONDS = 30L

    fun isRecent(ageSeconds: Long) = ageSeconds in 0..RECENT_ACTIVITY_SECONDS
    fun canInsertOrStop(ageSeconds: Long) = ageSeconds in 0..INSERT_AND_STOP_ELIGIBILITY_SECONDS
    fun insertionStillValid(ageSeconds: Long) = ageSeconds in 0..INSERTION_VALIDITY_SECONDS
}

enum class CapabilityAvailability { Available, Unavailable, PendingIntegration, RequiresUserAction }
enum class NodeHealth { Healthy, Starting, Stopped, Degraded, Unknown }
enum class OperationPhase { Idle, Queued, Running, Succeeded, Failed, PendingManifest, RequiresUserAction }

data class BridgeOperation(
    val schemaVersion: Int = 1,
    val operationId: String,
    val requestId: String,
    val nonce: String,
    val operation: String,
    val phase: String = "queued",
    val message: String = "",
    val createdAtEpochMs: Long,
    val updatedAtEpochMs: Long = createdAtEpochMs,
    val exitCode: Int? = null,
    val stdoutTruncated: Boolean = false,
    val stderrTruncated: Boolean = false
)

data class WorkbenchItem(
    val id: String,
    val title: String,
    val subtitle: String = "",
    val status: String = "",
    val metadata: String = "",
    val raw: JSONObject? = null
)

data class WorkbenchSnapshot(
    val coreHealth: NodeHealth = NodeHealth.Unknown,
    val coreVersion: String = "",
    val connectionMessage: String = "尚未连接 Core",
    val workspaces: List<WorkbenchItem> = emptyList(),
    val conversations: List<WorkbenchItem> = emptyList(),
    val tasks: List<WorkbenchItem> = emptyList(),
    val calls: List<WorkbenchItem> = emptyList(),
    val activity: List<WorkbenchItem> = emptyList(),
    val approvals: List<WorkbenchItem> = emptyList(),
    val insertions: List<WorkbenchItem> = emptyList(),
    val skills: List<WorkbenchItem> = emptyList(),
    val plugins: List<WorkbenchItem> = emptyList(),
    val mcpServers: List<WorkbenchItem> = emptyList(),
    val effectivePermissionSummary: String = "由 Core 提供有效权限",
    val effectivePermission: JSONObject? = null,
    val updatedAtEpochMs: Long = 0L,
    val fixture: Boolean = false
)

data class ActionOutcome(
    val accepted: Boolean,
    val status: String,
    val message: String,
    val serverValue: JSONObject? = null
)

object FixtureData {
    fun snapshot(now: Long = System.currentTimeMillis()) = WorkbenchSnapshot(
        coreHealth = NodeHealth.Healthy,
        coreVersion = "1.1.7",
        connectionMessage = "CI fixture · Core authority simulated explicitly",
        workspaces = listOf(WorkbenchItem("wsp_mobile", "Android 产品化", "/workspace/android", "active", "2 个活动对话")),
        conversations = listOf(
            WorkbenchItem("conv_active", "Android Workbench 实现", "最近工具调用 42 秒前", "active", "可插入 / 可停止"),
            WorkbenchItem("conv_history", "Termux 契约审查", "已完成", "completed", "只读历史")
        ),
        tasks = listOf(WorkbenchItem("tsk_android", "WB07 Android 完整 Workbench", "3/5", "active", "当前：Compose 页面")),
        calls = listOf(
            WorkbenchItem("call_running", "exec_command · Actions 构建", "等待运行器", "running", "RPC 1.231 s"),
            WorkbenchItem("call_approval", "plugin_manage · install", "需要用户审批", "pending_approval", "不会因 never 自动批准")
        ),
        activity = listOf(WorkbenchItem("evt_1", "读取 Core 快照", "GET /internal/runtime/execution", "succeeded", "128 ms")),
        approvals = listOf(WorkbenchItem("apr_1", "安装 GitHub 插件", "工作区一次性授权", "pending", "用户审批")),
        insertions = listOf(WorkbenchItem("ins_1", "补充：保持现有手机节点", "等待 Core 回执", "attached", "30 秒回执窗")),
        skills = listOf(WorkbenchItem("skill_1", "easyeda-pcb-layout-routing", "布局与布线", "enabled")),
        plugins = listOf(WorkbenchItem("plugin_1", "github", "Actions 与代码托管", "enabled")),
        mcpServers = listOf(WorkbenchItem("mcp_1", "AgentDock-OPPO", "21 tools", "connected")),
        effectivePermissionSummary = "完全权限 · 自定义权限开关关闭 · 显式 deny 仍生效",
        updatedAtEpochMs = now,
        fixture = true
    )
}
