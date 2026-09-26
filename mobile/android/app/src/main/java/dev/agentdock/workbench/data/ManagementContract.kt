package dev.agentdock.workbench.data

import dev.agentdock.workbench.model.ActionOutcome
import dev.agentdock.workbench.model.WorkbenchItem
import kotlinx.coroutines.CancellationException
import org.json.JSONArray
import org.json.JSONObject
import java.net.URLEncoder

/** Client transport only. State transitions and batch eligibility remain Core-owned. */
data class ListQuery(
    val view: String = "active",
    val search: String = "",
    val status: String = "",
    val workspaceId: String = "",
    val tag: String = "",
    val offset: Int = 0
) {
    init {
        require(view in setOf("active", "archived", "trash", "all"))
        require(search.length <= 512 && tag.length <= 128)
        require(offset in 0..20000)
    }
}

data class ResourcePage(
    val items: List<WorkbenchItem> = emptyList(),
    val total: Int? = null,
    val nextOffset: Int? = null,
    val hasMore: Boolean = false
)

object ManagementContract {
    const val PAGE_SIZE = 100
    const val MAX_BATCH = 200
    val actions = setOf("pin", "unpin", "tags", "rename", "archive", "unarchive", "trash", "restore", "delete")

    fun id(value: String): String {
        require(Regex("^[A-Za-z0-9_-]{1,128}$").matches(value)) { "无效资源标识" }
        return value
    }

    fun encode(value: String): String = URLEncoder.encode(value, "UTF-8").replace("+", "%20")

    fun listPath(kind: String, query: ListQuery): String {
        require(kind == "tasks" || kind == "conversations")
        val route = if (kind == "tasks") "execution/tasks" else "conversations"
        val values = linkedMapOf(
            "view" to query.view, "search" to query.search,
            "workspace_id" to query.workspaceId, "tag" to query.tag,
            "offset" to query.offset.toString(), "limit" to PAGE_SIZE.toString()
        )
        if (kind == "tasks" && query.status.isNotEmpty()) values["status"] = query.status
        return "/internal/runtime/$route?" + values.entries.joinToString("&") { "${it.key}=${encode(it.value)}" }
    }

    fun batchBody(ids: List<String>, action: String, title: String = "", tags: List<String> = emptyList(), confirmed: Boolean = false): JSONObject {
        require(ids.size in 1..MAX_BATCH && ids.distinct().size == ids.size) { "请选择 1–200 个不同对象" }
        ids.forEach(::id)
        require(action in actions) { "不支持此管理操作" }
        require(action != "rename" || (ids.size == 1 && title.isNotBlank())) { "重命名需一个对象及非空名称" }
        require(action != "delete" || confirmed) { "永久删除必须明确确认" }
        require(title.length <= 512 && tags.size <= 32 && tags.all { it.length in 1..128 })
        return JSONObject().put("ids", JSONArray(ids)).put("action", action).apply {
            if (action == "rename") put("title", title)
            if (action == "tags") put("tags", JSONArray(tags))
            if (action == "delete") put("confirm_permanent", true)
        }
    }

    fun parsePage(value: JSONObject, kind: String): ResourcePage {
        require(kind == "tasks" || kind == "conversations")
        val array = value.optJSONArray(kind) ?: error("Core 响应缺少 $kind 数组")
        require(array.length() <= MAX_BATCH) { "Core 返回超出页上限" }
        val key = if (kind == "tasks") "id" else "conversation_id"
        val items = (0 until array.length()).map { index ->
            val row = array.getJSONObject(index)
            val identity = text(row, key).ifBlank { text(row, "id") }
            id(identity)
            WorkbenchItem(
                identity, text(row, "title").ifBlank { identity },
                text(row, "summary"), text(row, "status"),
                if (kind == "tasks" && row.has("step_count"))
                    "${row.optInt("completed_steps")}/${row.optInt("step_count")} 步 · ${text(row, "current_step_id")}" else "",
                row
            )
        }
        require(items.distinctBy { it.id }.size == items.size) { "Core 返回重复资源标识" }
        val more = value.optBoolean("has_more")
        val next = if (value.has("next_offset")) value.getInt("next_offset") else null
        require(!more || (next != null && next in 1..20000)) { "Core 分页游标缺失或无效" }
        return ResourcePage(items, if (value.has("total")) value.getInt("total") else null, next, more)
    }

    fun batchOutcome(value: JSONObject, expectedIds: List<String>): ActionOutcome {
        val items = value.optJSONArray("items") ?: error("批量回执缺少逐项结果，需刷新后核对")
        val received = (0 until items.length()).map { items.getJSONObject(it) }
        require(received.map { text(it, "id") }.toSet() == expectedIds.toSet() && received.size == expectedIds.size) {
            "批量回执标识不完整，需刷新后核对"
        }
        val counts = received.groupingBy { text(it, "status") }.eachCount()
        val success = counts["succeeded"] ?: 0
        val skipped = counts["skipped"] ?: 0
        val failed = received.size - success - skipped
        return ActionOutcome(success == received.size, if (success == received.size) "succeeded" else "partial",
            "成功 $success，跳过 $skipped，失败或未判定 $failed", value)
    }

    fun outcome(value: JSONObject): ActionOutcome {
        val status = text(value, "status").ifBlank { "accepted" }
        val accepted = (!value.has("ok") || value.optBoolean("ok")) && status !in setOf("failed", "partial", "denied", "rejected", "pending_integration")
        return ActionOutcome(accepted, status, text(value, "message").ifBlank { "Core 返回：$status" }, value)
    }

    fun text(value: JSONObject, key: String): String = if (value.isNull(key)) "" else value.optString(key, "")

    fun failure(error: Throwable): String {
        if (error is CancellationException) throw error
        return if (error is CoreRequestException) "${error.errorCode} (HTTP ${error.statusCode})：${error.message}" else error.message.orEmpty().take(1024)
    }
}
