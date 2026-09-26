package dev.agentdock.workbench.ui

import dev.agentdock.workbench.data.CoreClient
import dev.agentdock.workbench.data.CoreCommand
import dev.agentdock.workbench.data.ManagementContract
import dev.agentdock.workbench.model.WorkbenchSnapshot
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import org.json.JSONArray
import org.json.JSONObject

/** Bounded, disposable client views; authoritative mutations exist only on Core. */
data class ResourceView(val data: JSONObject? = null, val error: String = "", val loading: Boolean = false, val sourcePath: String = "")
data class ResourceState(val views: Map<String, ResourceView> = emptyMap(), val actionBusy: Boolean = false, val actionResult: JSONObject? = null)

class CoreResourceController(
    private val scope: CoroutineScope,
    private val client: suspend () -> CoreClient,
    private val fixture: () -> Boolean,
    private val snapshot: () -> WorkbenchSnapshot,
    private val notify: (String) -> Unit,
    private val refresh: () -> Unit
) {
    private val mutable = MutableStateFlow(ResourceState())
    val state = mutable.asStateFlow()
    private val jobs = mutableMapOf<String, Job>()
    private val generations = mutableMapOf<String, Long>()
    private var epoch = 0L

    fun read(key: String, path: String) {
        require(key in KEYS && path.startsWith("/internal/runtime/") && path.length <= 8192)
        jobs.remove(key)?.cancel()
        val generation = (generations[key] ?: 0) + 1
        generations[key] = generation
        val connectionEpoch = epoch
        mutable.update { it.copy(views = it.views + (key to ResourceView(loading = true, sourcePath = path))) }
        jobs[key] = scope.launch {
            try {
                val data = if (fixture()) fixtureData(key) else client().get(path)
                ensureActive()
                if (connectionEpoch == epoch && generation == generations[key]) {
                    mutable.update { it.copy(views = it.views + (key to ResourceView(data, sourcePath = path))) }
                }
            } catch (error: CancellationException) { throw error
            } catch (error: Exception) {
                if (connectionEpoch == epoch && generation == generations[key]) {
                    mutable.update { it.copy(views = it.views + (key to ResourceView(error = ManagementContract.failure(error), sourcePath = path))) }
                }
            }
        }
    }

    fun execute(command: CoreCommand, onAccepted: (JSONObject) -> Unit = {}) {
        if (fixture()) { notify("Fixture 模式禁止实际写入与设备操作"); return }
        if (mutable.value.actionBusy) return
        val frozenBody = command.body()
        val connectionEpoch = epoch
        mutable.update { it.copy(actionBusy = true, actionResult = null) }
        jobs["mutation"] = scope.launch {
            try {
                val result = client().post(command.path, frozenBody)
                ensureActive()
                if (connectionEpoch != epoch) return@launch
                mutable.update { it.copy(actionResult = result) }
                val ids = frozenBody.optJSONArray("ids")
                val outcome = if (ids == null) ManagementContract.outcome(result)
                    else ManagementContract.batchOutcome(result, (0 until ids.length()).map { ids.getString(it) })
                notify(outcome.message)
                if (outcome.accepted) onAccepted(result)
                // Refresh facts after both complete and partial writes. Failed objects
                // are not replayed; the complete per-item receipt remains visible.
                refresh()
            } catch (error: CancellationException) {
                if (connectionEpoch == epoch) notify("请求已取消；已发送写入可能已经提交，请先回读，不自动重发。")
                throw error
            } catch (error: Exception) { if (connectionEpoch == epoch) notify(ManagementContract.failure(error))
            } finally { if (connectionEpoch == epoch) mutable.update { it.copy(actionBusy = false) } }
        }
    }

    fun invalidate() {
        epoch++
        jobs.values.forEach { it.cancel() }; jobs.clear(); generations.clear()
        mutable.value = ResourceState()
    }

    private fun fixtureData(key: String): JSONObject {
        val source = snapshot()
        fun records(items: List<dev.agentdock.workbench.model.WorkbenchItem>, id: String): JSONArray = JSONArray().apply {
            items.forEach { item -> put(item.raw ?: JSONObject().put(id, item.id).put("title", item.title).put("name", item.title).put("status", item.status).put("summary", item.subtitle)) }
        }
        return when (key) {
            "workspaces" -> JSONObject().put("workspaces", records(source.workspaces, "workspace_id"))
            "calls", "children" -> JSONObject().put("calls", records(source.calls, "call_id")).put("has_more", false)
            "approvals" -> JSONObject().put("approvals", records(source.approvals, "approval_id")).put("total", source.approvals.size).put("has_more", false)
            "skills" -> JSONObject().put("skills", records(source.skills, "skill_ref"))
            "plugins" -> JSONObject().put("plugins", records(source.plugins, "name"))
            "mcp" -> JSONObject().put("servers", records(source.mcpServers, "name"))
            "activity", "call-events" -> JSONObject().put("events", records(source.activity, "event_id")).put("has_more", false)
            "permission" -> source.effectivePermission ?: JSONObject().put("revision", 1).put("effective", JSONObject().put("revision", 1).put("mode", "full").put("custom_permissions_enabled", false)
                .put("configured_settings", JSONObject().put("permission_profile", JSONObject().put("filesystem", "write").put("network", "allow").put("sandbox_boundary", "none"))
                .put("approval_policy", JSONObject().put("mode", "on-request")).put("approval_reviewer", "user")))
            "display" -> JSONObject().put("revision", 1).put("chatgpt_mcp_ui_enabled", false).put("tool_output", JSONObject().put("enabled", true).put("max_chars", 20000))
            "connection" -> JSONObject().put("public_reachability", "not_checked").put("source", "explicit CI fixture")
            else -> JSONObject().put("fixture", true).put("status", "available").put("summary", "固定测试数据，不执行真实服务动作")
        }
    }
    companion object {
        private val KEYS = setOf("workspaces", "workspace-detail", "calls", "call-detail", "call-events", "children", "activity",
            "approvals", "approval-detail", "permission", "skills", "skill-detail", "skill-files", "skill-file", "plugins", "plugin-detail", "mcp", "mcp-detail", "display", "connection", "task-detail", "conversations")
    }
}
