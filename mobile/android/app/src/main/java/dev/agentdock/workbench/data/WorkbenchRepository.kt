package dev.agentdock.workbench.data

import android.content.Context
import android.content.pm.ApplicationInfo
import dev.agentdock.workbench.model.ActionOutcome
import dev.agentdock.workbench.model.FixtureData
import dev.agentdock.workbench.model.NodeHealth
import dev.agentdock.workbench.model.WorkbenchItem
import dev.agentdock.workbench.model.WorkbenchSettings
import dev.agentdock.workbench.model.WorkbenchSnapshot
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import org.json.JSONArray
import org.json.JSONObject
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.util.UUID

class WorkbenchRepository(
    private val context: Context,
    private val settingsStore: SettingsStore,
    private val credentials: CredentialStore
) {
    private val debugBuild = (context.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0

    suspend fun refresh(fixture: Boolean = false, selectedConversation: String = "", tasksQuery: ListQuery = ListQuery(), conversationsQuery: ListQuery = ListQuery()): WorkbenchSnapshot {
        if (fixture) {
            check(debugBuild) { "Fixture data is disabled in non-debug builds" }
            return FixtureData.snapshot()
        }
        val settings = settingsStore.current()
        val client = client(settings)
        return coroutineScope {
            val health = async { safeResult { client.get("/healthz") } }
            val sidebar = async { safeResult { client.post("/internal/runtime/execution/sidebar", sidebarRequest()) } }
            val conversations = async { safeResult { client.get(ManagementContract.listPath("conversations", conversationsQuery)) } }
            val tasks = async { safeResult { client.get(ManagementContract.listPath("tasks", tasksQuery)) } }
            val calls = async { safeResult { client.get("/internal/runtime/calls?view=active&limit=100&top_level=true&include_output=false") } }
            val activity = async { safeResult { client.get("/internal/runtime/activity?limit=100&after=0") } }
            val approvals = async { safeResult { client.get("/internal/runtime/approvals?limit=100") } }
            val permissions = async { safeResult { client.get("/internal/runtime/permissions/effective") } }
            val skills = async { safeResult { client.get("/internal/runtime/skills?summary=true") } }
            val plugins = async { safeResult { client.get("/internal/runtime/plugins") } }
            val mcp = async { safeResult { client.get("/internal/runtime/mcp") } }
            val insertions = async {
                if (selectedConversation.isBlank()) Result.success(JSONObject())
                else safeResult { client.get("/internal/runtime/conversations/${id(selectedConversation)}/insertions") }
            }

            val healthResult = health.await()
            val sidebarResult = sidebar.await()
            val taskResult = tasks.await()
            val conversationResult = conversations.await()
            val taskPageResult = taskResult.mapCatching { ManagementContract.parsePage(it, "tasks") }
            val conversationPageResult = conversationResult.mapCatching { ManagementContract.parsePage(it, "conversations") }
            val callResult = calls.await()
            val activityResult = activity.await()
            val approvalsResult = approvals.await()
            val permissionsResult = permissions.await()
            val skillsResult = skills.await()
            val pluginsResult = plugins.await()
            val mcpResult = mcp.await()
            val insertionResult = insertions.await()

            val endpointResults = mapOf(
                "home" to healthResult, "workspaces" to sidebarResult, "tasks" to taskPageResult,
                "conversations" to conversationPageResult, "calls" to callResult, "activity" to activityResult,
                "approvals" to approvalsResult, "permissions" to permissionsResult, "skills" to skillsResult,
                "plugins" to pluginsResult, "mcp" to mcpResult, "insert" to insertionResult
            )
            val endpointErrors = endpointResults.mapNotNull { (key, value) ->
                value.exceptionOrNull()?.let { key to ManagementContract.failure(it) }
            }.toMap()
            val failures = endpointErrors.size
            val healthValue = healthResult.getOrNull()
            val coreHealth = when {
                healthValue != null && failures == 0 -> NodeHealth.Healthy
                healthValue != null -> NodeHealth.Degraded
                else -> NodeHealth.Unknown
            }
            val sidebarValue = sidebarResult.getOrNull()
            val groups = sidebarValue?.optJSONArray("groups") ?: JSONArray()
            WorkbenchSnapshot(
                coreHealth = coreHealth,
                coreVersion = healthValue?.optString("version").orEmpty(),
                connectionMessage = when (coreHealth) {
                    NodeHealth.Healthy -> "Core 已连接"
                    NodeHealth.Degraded -> "Core 已连接，但 $failures 个能力端点不可用"
                    NodeHealth.Unknown -> healthResult.exceptionOrNull()?.message ?: "Core 未运行或未授权"
                    else -> "Core 状态未知"
                },
                workspaces = parseWorkspaceGroups(groups),
                conversations = conversationPageResult.getOrNull()?.items.orEmpty(),
                tasks = taskPageResult.getOrNull()?.items.orEmpty(),
                errors = endpointErrors,
                taskPage = taskPageResult.getOrNull() ?: ResourcePage(),
                conversationPage = conversationPageResult.getOrNull() ?: ResourcePage(),
                calls = parseArray(callResult.getOrNull(), "calls", "call_id", "display_title", "status", fallbackTitle = "tool_name"),
                activity = parseArray(activityResult.getOrNull(), "events", "event_id", "title", "status", fallbackTitle = "activity_label"),
                approvals = parseArray(approvalsResult.getOrNull(), "approvals", "approval_id", "operation", "status", fallbackTitle = "tool_name"),
                insertions = parseArray(insertionResult.getOrNull(), "insertions", "insertion_id", "text", "status"),
                skills = parseArray(skillsResult.getOrNull(), "skills", "skill_ref", "name", "status", enabledField = "enabled"),
                plugins = parseArray(pluginsResult.getOrNull(), "plugins", "name", "name", "status", enabledField = "enabled"),
                mcpServers = parseArray(mcpResult.getOrNull(), "servers", "name", "name", "status"),
                effectivePermissionSummary = permissionSummary(permissionsResult.getOrNull()),
                effectivePermission = permissionsResult.getOrNull(),
                updatedAtEpochMs = System.currentTimeMillis()
            )
        }
    }

    suspend fun sendInsertion(conversationId: String, text: String, submissionId: String = UUID.randomUUID().toString().replace("-", "")): ActionOutcome {
        val bytes = text.toByteArray(StandardCharsets.UTF_8)
        if (text.isBlank()) return ActionOutcome(false, "invalid", "补充内容不能为空")
        if (bytes.size > 8192) return ActionOutcome(false, "invalid", "补充内容超过 8192 字节")
        return client(settingsStore.current()).action(
            "/internal/runtime/conversations/${id(conversationId)}/insertions",
            JSONObject().put("submission_id", submissionId).put("text", text)
        )
    }

    suspend fun insertionAction(conversationId: String, insertionId: String, action: String): ActionOutcome {
        require(action == "cancel" || action == "retry")
        return client(settingsStore.current()).action(
            "/internal/runtime/conversations/${id(conversationId)}/insertions/${id(insertionId)}/$action"
        )
    }

    suspend fun terminateConversation(conversationId: String): ActionOutcome =
        client(settingsStore.current()).action(
            "/internal/runtime/conversations/${id(conversationId)}/terminate",
            JSONObject().put("confirm", true)
        )

    suspend fun callAction(callId: String, action: String): ActionOutcome {
        require(action == "stop") { "共享接口不提供调用重试；未执行请求" }
        return client(settingsStore.current()).action("/internal/runtime/calls/${id(callId)}/$action")
    }

    suspend fun approvalAction(approvalId: String, approve: Boolean, allowWorkspace: Boolean = false): ActionOutcome =
        client(settingsStore.current()).action(
            "/internal/runtime/approvals/${id(approvalId)}/${if (approve) "approve" else "reject"}",
            JSONObject().put("allow_workspace", allowWorkspace)
        )

    suspend fun capabilityAction(kind: String, name: String, enabled: Boolean): ActionOutcome {
        require(kind in setOf("skills", "plugins", "mcp"))
        val key = if (kind == "skills") "skill" else "name"
        return client(settingsStore.current()).action(
            "/internal/runtime/$kind",
            JSONObject().put("action", if (enabled) "enable" else "disable").put(key, name)
        )
    }

    suspend fun savePermission(change: JSONObject): ActionOutcome =
        client(settingsStore.current()).action("/internal/runtime/permissions", change)

    suspend fun observeCalls(after: Long, receive: suspend (SseMessage) -> Unit) =
        client(settingsStore.current()).observeSse("/internal/runtime/calls/stream?after=$after", after, receive)

    suspend fun observeActivity(after: Long, receive: suspend (SseMessage) -> Unit) =
        client(settingsStore.current()).observeSse("/internal/runtime/activity/stream?after=$after", after, receive)

    suspend fun batch(kind: String, ids: List<String>, action: String, title: String = "", tags: List<String> = emptyList(), confirmed: Boolean = false): ActionOutcome {
        require(kind in setOf("tasks", "conversations"))
        val body = ManagementContract.batchBody(ids, action, title, tags, confirmed)
        val value = client(settingsStore.current()).post("/internal/runtime/$kind/batch", body)
        return ManagementContract.batchOutcome(value, ids)
    }

    suspend fun detail(kind: String, identity: String): JSONObject {
        require(kind in setOf("tasks", "conversations", "calls", "approvals"))
        return client(settingsStore.current()).get("/internal/runtime/$kind/${id(identity)}")
    }

    suspend fun callChildren(identity: String): JSONObject = client(settingsStore.current()).get(
        "/internal/runtime/calls?parent_call_id=${id(identity)}&limit=100&include_output=false"
    )

    suspend fun callPayload(identity: String, kind: String, offset: Long): JSONObject {
        require(kind in setOf("request", "response", "source") && offset >= 0)
        val settings = settingsStore.current()
        check(settings.toolOutputEnabled) { "工具输出显示已关闭" }
        return client(settings).get("/internal/runtime/calls/${id(identity)}/payload/$kind?offset=$offset&limit_chars=${settings.toolOutputMaxChars}")
    }

    private inline fun <T> safeResult(block: () -> T): Result<T> = try {
        Result.success(block())
    } catch (error: CancellationException) {
        throw error
    } catch (error: Exception) {
        Result.failure(error)
    }

    private suspend fun client(settings: WorkbenchSettings): CoreClient = withContext(Dispatchers.IO) {
        val origin = EndpointPolicy.resolve(settings.endpoint, settings.remoteEndpointEnabled)
        CoreClient(CoreEndpoint(origin, credentials.getCore(origin)))
    }

    private fun sidebarRequest() = JSONObject()
        .put("view", "active")
        .put("search", "")
        .put("limits", JSONObject())
        .put("modes", JSONObject())
        .put("cursors", JSONObject())
        .put("default_mode", "auto")
        .put("selected_id", "")

    private fun permissionSummary(value: JSONObject?): String {
        if (value == null) return "有效权限不可用；未在客户端推断"
        val decision = value.optJSONObject("effective") ?: value
        val mode = decision.optString("mode", "unknown")
        val settings = decision.optJSONObject("settings")
        val profile = settings?.optJSONObject("permission_profile")
        val approval = settings?.optJSONObject("approval_policy")
        return listOf(
            "模式 ${mode.ifBlank { "unknown" }}",
            "文件 ${profile?.optString("filesystem", "unknown") ?: "unknown"}",
            "网络 ${profile?.optString("network", "unknown") ?: "unknown"}",
            "边界 ${profile?.optString("sandbox_boundary", "unknown") ?: "unknown"}",
            "审批 ${approval?.optString("mode", "unknown") ?: "unknown"}"
        ).joinToString(" · ")
    }

    private fun parseWorkspaceGroups(groups: JSONArray): List<WorkbenchItem> = buildList {
        for (index in 0 until groups.length()) {
            val group = groups.optJSONObject(index) ?: continue
            val id = group.optString("workspace_id")
            if (id.isBlank()) continue
            add(WorkbenchItem(id, group.optString("title", id), group.optString("root"), group.optString("mode"), "${group.optInt("total", 0)} 个对话", group))
        }
    }

    private fun parseConversations(groups: JSONArray): List<WorkbenchItem> = buildList {
        for (index in 0 until groups.length()) {
            val group = groups.optJSONObject(index) ?: continue
            val array = group.optJSONArray("conversations") ?: continue
            addAll(parseItems(array, "conversation_id", "title", "source", "", group.optString("title")))
        }
    }

    private fun parseArray(
        value: JSONObject?, arrayName: String, idField: String, titleField: String, statusField: String,
        fallbackTitle: String = "", enabledField: String = ""
    ): List<WorkbenchItem> {
        val array = value?.optJSONArray(arrayName) ?: return emptyList()
        return parseItems(array, idField, titleField, statusField, fallbackTitle, "", enabledField)
    }

    private fun parseItems(
        array: JSONArray, idField: String, titleField: String, statusField: String,
        fallbackTitle: String = "", group: String = "", enabledField: String = ""
    ): List<WorkbenchItem> = buildList {
        for (index in 0 until array.length()) {
            val item = array.optJSONObject(index) ?: continue
            val itemId = ManagementContract.text(item, idField).ifBlank { ManagementContract.text(item, "id").ifBlank { ManagementContract.text(item, "name") } }
            if (itemId.isBlank()) continue
            val title = item.optString(titleField).ifBlank { item.optString(fallbackTitle).ifBlank { itemId } }
            val status = if (enabledField.isNotBlank() && item.has(enabledField)) {
                if (item.optBoolean(enabledField)) "enabled" else "disabled"
            } else item.optString(statusField)
            val subtitle = item.optString("summary").ifBlank {
                item.optString("goal").ifBlank { item.optString("description") }
            }
            add(WorkbenchItem(itemId, title, subtitle, status, group, item))
        }
    }

    private fun id(value: String): String {
        require(Regex("^[A-Za-z0-9_-]{1,128}$").matches(value)) { "标识符无效" }
        return URLEncoder.encode(value, StandardCharsets.UTF_8.name())
    }
}
