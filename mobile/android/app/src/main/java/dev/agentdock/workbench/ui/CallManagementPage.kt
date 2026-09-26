package dev.agentdock.workbench.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.agentdock.workbench.data.*
import org.json.JSONArray
import org.json.JSONObject

@Composable
fun CallManagementPage(state: WorkbenchUiState, model: WorkbenchViewModel, modifier: Modifier) {
    val resource by model.resources.state.collectAsStateWithLifecycle()
    val prompt = rememberCommandPrompt(model)
    var view by rememberSaveable { mutableStateOf("active") }
    var search by rememberSaveable { mutableStateOf("") }
    var status by rememberSaveable { mutableStateOf("") }
    var conversation by rememberSaveable { mutableStateOf("") }
    var task by rememberSaveable { mutableStateOf("") }
    var unattributed by rememberSaveable { mutableStateOf(false) }
    var before by rememberSaveable { mutableStateOf(0L) }
    var history by rememberSaveable { mutableStateOf(arrayListOf<Long>()) }
    var selected by rememberSaveable { mutableStateOf(state.selectedCallId) }
    var checked by remember { mutableStateOf(setOf<String>()) }
    var exportFilter by remember { mutableStateOf(CallFilter()) }
    var exportValue by remember { mutableStateOf(JSONObject()) }
    fun filter() = CallFilter(view, search, status, conversation, task, unattributed = unattributed, before = before)
    fun reload() {
        runCatching { model.resources.read("calls", filter().path()) }.onFailure { model.showNotice("调用筛选参数无效。") }
        checked = emptySet()
    }
    fun select(id: String) {
        selected = id
        model.resources.read("call-detail", "/internal/runtime/calls/${ManagementContract.id(id)}")
        model.resources.read("children", CallFilter(view = "all", parent = id).path())
        model.resources.read("call-events", "/internal/runtime/calls/${ManagementContract.id(id)}/events?limit=100&after=0")
    }
    val exportScope = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/json")) { uri -> uri?.let { model.exportCallScope(it, exportFilter) } }
    val exportSelection = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/json")) { uri -> uri?.let { model.exportRecords(it, exportValue) } }
    LaunchedEffect(state.settings.endpoint, state.settings.remoteEndpointEnabled) {
        reload()
        if (selected.isNotBlank()) select(selected)
    }
    val list = resource.views["calls"]
    val rows = remember(list?.data) { runCatching { list?.data?.let { CorePages.rows(it, "calls") }.orEmpty() } }
    val detail = resource.views["call-detail"]?.data?.let { it.optJSONObject("call") ?: it }
    val children = remember(resource.views["children"]?.data) { runCatching { resource.views["children"]?.data?.let { CorePages.rows(it, "calls") }.orEmpty() } }
    val next = remember(list?.data, before) { runCatching { list?.data?.let { CorePages.next(it, before, "next_before", true) } } }
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Text("调用与历史", style = MaterialTheme.typography.headlineSmall)
            Text("每页最多 100 条，详情与输出按需读取。分页变化不会扩大已冻结的批量选择。")
            ResourceField("搜索工具、命令或摘要", search, { search = it.take(512) })
            ResourceActions {
                listOf("active" to "当前", "archived" to "归档", "isolated" to "隔离", "trash" to "回收站", "all" to "全部").forEach { (value, label) ->
                    FilterChip(view == value, { view = value; before = 0; history = arrayListOf(); reload() }, label = { Text(label) })
                }
            }
            ResourceField("调用状态（留空为全部）", status, { status = it.take(80) })
            ResourceField("对话 ID（可选）", conversation, { conversation = it.take(128) })
            ResourceField("任务 ID（可选）", task, { task = it.take(128) })
            Row { Checkbox(unattributed, { unattributed = it }); Text("仅未归属调用", Modifier.padding(top = 12.dp)) }
            ResourceActions {
                Button(onClick = { before = 0; history = arrayListOf(); reload() }, enabled = list?.loading != true) { Text("应用筛选") }
                TextButton(onClick = { checked = rows.getOrDefault(emptyList()).map { it.optString("call_id") }.filter { it.isNotBlank() }.toSet() }) { Text("选择本页") }
                TextButton(onClick = { checked = emptySet() }) { Text("清除选择") }
                TextButton(onClick = { exportFilter = filter().copy(before = 0); exportScope.launch("AgentDock-call-scope.json") }) { Text("导出完整筛选摘要") }
            }
            ResourceFeedback(list, ::reload)
            rows.exceptionOrNull()?.let { Text(it.message.orEmpty()) }
            if (list?.data != null && rows.getOrDefault(emptyList()).isEmpty()) Text("当前筛选没有调用记录。")
        }
        items(rows.getOrDefault(emptyList()), key = { it.optString("call_id") }) { row ->
            val id = row.optString("call_id")
            Card(Modifier.fillMaxWidth()) {
                Row(Modifier.padding(10.dp)) {
                    Checkbox(id in checked, { checked = if (id in checked) checked - id else checked + id })
                    Column(Modifier.weight(1f)) {
                        Text(row.optString("display_title", row.optString("tool_name", id)), style = MaterialTheme.typography.titleMedium)
                        ResourceObject(row, listOf("status", "display_command", "elapsed_ms", "duration_ms", "started_at", "parent_call_id"))
                        TextButton(onClick = { select(id) }) { Text(if (selected == id) "重新读取详情" else "查看详情") }
                    }
                }
            }
        }
        item {
            ResourceActions {
                TextButton(onClick = { before = history.last(); history = ArrayList(history.dropLast(1)); reload() }, enabled = history.isNotEmpty()) { Text("上一页") }
                TextButton(onClick = { next.getOrNull()?.let { history = ArrayList(history + before); before = it; reload() } }, enabled = next.getOrNull() != null) { Text("更早记录") }
            }
            next.exceptionOrNull()?.let { Text(it.message.orEmpty()) }
            if (checked.isNotEmpty()) ResourceSection("已冻结 ${checked.size} 条调用") {
                ResourceActions {
                    listOf("archive" to "归档", "unarchive" to "取消归档", "isolate" to "隔离", "unisolate" to "解除隔离", "trash" to "移入回收站", "restore" to "恢复", "delete" to "永久删除").forEach { (action, label) ->
                        OutlinedButton(onClick = {
                            val ids = checked.toList()
                            prompt.ask({ WorkbenchCommands.callBatch(ids, action, confirmed = action == "delete") }) { reload() }
                        }, enabled = !resource.actionBusy) { Text(label) }
                    }
                }
                TextButton(onClick = {
                    exportValue = JSONObject().put("calls", JSONArray(rows.getOrDefault(emptyList()).filter { it.optString("call_id") in checked })).put("includes_output", false)
                    exportSelection.launch("AgentDock-selected-calls.json")
                }) { Text("导出所选摘要") }
            }
        }
        if (selected.isNotBlank()) item {
            ResourceSection("详情 $selected") {
                ResourceFeedback(resource.views["call-detail"]) { select(selected) }
                if (detail != null) {
                    ResourceObject(detail)
                    ResourceObject(detail.optJSONObject("timing") ?: JSONObject(), listOf("rpc_ms", "queue_ms", "execution_ms", "total_ms"))
                    ResourceObject(detail, listOf("duration_ms", "rpc_elapsed_ms", "elapsed_ms", "exit_code", "error", "source", "truncated"))
                    val parent = detail.optString("parent_call_id")
                    ResourceActions {
                        if (parent.isNotBlank()) TextButton(onClick = { select(parent) }) { Text("打开父调用") }
                        TextButton(onClick = { prompt.ask({ WorkbenchCommands.stopCall(selected) }) { select(selected); reload() } }, enabled = !resource.actionBusy) { Text("停止所选调用") }
                        TextButton(onClick = { exportValue = JSONObject().put("call", JSONObject(detail.toString())); exportSelection.launch("AgentDock-call-detail.json") }) { Text("导出当前详情") }
                    }
                    Text("失败调用不提供原命令重放按钮。重试须重新核对原请求、部分效果与执行权限。")
                }
                ResourceFeedback(resource.views["children"]) { model.resources.read("children", CallFilter(view = "all", parent = selected).path()) }
                children.exceptionOrNull()?.let { Text(it.message.orEmpty()) }
                children.getOrDefault(emptyList()).forEach { child ->
                    val id = child.optString("call_id")
                    TextButton(onClick = { select(id) }) { Text("子调用 · ${child.optString("tool_name", id)} · ${child.optString("status")}") }
                }
                resource.views["children"]?.data?.let { page ->
                    if (page.optBoolean("has_more")) TextButton(onClick = {
                        runCatching { CorePages.next(page, 0, "next_before", true) }.onSuccess { cursor ->
                            cursor?.let { model.resources.read("children", CallFilter(view = "all", parent = selected, before = it).path()) }
                        }.onFailure { model.showNotice(it.message.orEmpty()) }
                    }) { Text("更早子调用") }
                }
                val events = resource.views["call-events"]?.data
                if (events != null) {
                    val eventRows = runCatching { CorePages.rows(events, "events", 500) }
                    eventRows.getOrDefault(emptyList()).forEach { event -> ResourceObject(event, listOf("event_id", "type", "status", "title", "summary", "timestamp")) }
                    eventRows.exceptionOrNull()?.let { Text(it.message.orEmpty()) }
                    if (events.optBoolean("has_more")) TextButton(onClick = {
                        val cursor = events.optLong("next_seq", -1)
                        if (cursor >= 0) model.resources.read("call-events", "/internal/runtime/calls/${ManagementContract.id(selected)}/events?limit=100&after=$cursor")
                        else model.showNotice("事件游标缺失，停止翻页。")
                    }) { Text("下一段调用事件") }
                }
                CallPayloadPanel(selected, state, model)
            }
        }
        item { ResourceReceipt(resource.actionResult) }
    }
}

@Composable
private fun CallPayloadPanel(id: String, state: WorkbenchUiState, model: WorkbenchViewModel) {
    if (!state.settings.toolOutputEnabled) { Text("工具输出显示已关闭。"); return }
    val payload = state.payload
    ResourceActions {
        listOf("request" to "请求", "response" to "响应", "source" to "源输出").forEach { (kind, label) ->
            TextButton(onClick = { model.loadCallPayload(id, kind) }) { Text("读取$label") }
        }
    }
    if (state.detailError.isNotBlank()) Text(state.detailError)
    if (payload != null && state.payloadCallId == id) {
        Text(payload.optString("text"))
        Text("UTF-8 字节游标 ${payload.optLong("offset")} · 截断 ${payload.optBoolean("truncated")}")
        if (payload.optBoolean("has_more")) TextButton(onClick = {
            val next = payload.optLong("next_offset", -1)
            if (next > payload.optLong("offset", -1)) model.loadCallPayload(id, state.payloadKind, next)
            else model.showNotice("输出游标未前进，未重复读取。")
        }) { Text("读取下一段") }
    }
}
