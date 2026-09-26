package dev.agentdock.workbench.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import dev.agentdock.workbench.data.ManagementContract
import dev.agentdock.workbench.model.WorkbenchScreen
import org.json.JSONObject

@Composable
fun ManagementPage(kind: String, state: WorkbenchUiState, model: WorkbenchViewModel, modifier: Modifier) {
    val tasks = kind == "tasks"
    val data = if (tasks) state.snapshot.tasks else state.snapshot.conversations
    val page = if (tasks) state.snapshot.taskPage else state.snapshot.conversationPage
    val current = if (tasks) state.tasksQuery else state.conversationsQuery
    val selectedId = if (tasks) state.selectedTaskId else state.selectedConversationId
    var query by remember(current) { mutableStateOf(current) }
    var name by rememberSaveable(selectedId) { mutableStateOf("") }
    var tags by rememberSaveable(selectedId) { mutableStateOf("") }
    var confirmation by remember { mutableStateOf<Pair<String, List<String>>?>(null) }
    val selected = data.firstOrNull { it.id == selectedId }
    val frozenIds = state.checkedIds.toList().ifEmpty { listOfNotNull(selected?.id) }
    val labels = listOf("pin" to "置顶", "unpin" to "取消置顶", "archive" to "归档", "unarchive" to "取消归档",
        "trash" to "移入回收站", "restore" to "恢复", "delete" to "永久删除")

    confirmation?.let { (action, identities) ->
        AlertDialog(
            onDismissRequest = { confirmation = null },
            title = { Text("确认${labels.first { it.first == action }.second}") },
            text = { Text("本次仅处理已冻结的 ${identities.size} 个对象：\n${identities.take(8).joinToString("\n")}\n仅修改管理记录，项目文件保留。") },
            confirmButton = { TextButton(onClick = { confirmation = null; model.manage(kind, identities, action, confirmed = true) }, modifier = Modifier.testTag("confirm-management")) { Text("确认") } },
            dismissButton = { TextButton(onClick = { confirmation = null }) { Text("取消") } }
        )
    }

    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Text(if (tasks) "任务中心" else "对话", style = MaterialTheme.typography.headlineSmall)
            Text("每页最多 ${ManagementContract.PAGE_SIZE} 项；分页与筛选由 Core 执行。")
            OutlinedTextField(query.search, { query = query.copy(search = it.take(512)) }, label = { Text("搜索") }, modifier = Modifier.fillMaxWidth().testTag("$kind-search"))
            Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                listOf("active" to "当前", "archived" to "已归档", "trash" to "回收站", "all" to "全部").forEach { (value, label) ->
                    FilterChip(query.view == value, { query = query.copy(view = value, offset = 0); model.updateQuery(kind, query) }, label = { Text(label) })
                }
            }
            if (tasks) Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                listOf("" to "所有状态", "active" to "进行中", "blocked" to "受阻", "completed" to "已完成", "cancelled" to "已取消").forEach { (value, label) ->
                    FilterChip(query.status == value, { query = query.copy(status = value, offset = 0); model.updateQuery(kind, query) }, label = { Text(label) })
                }
            }
            OutlinedTextField(query.tag, { query = query.copy(tag = it.take(128)) }, label = { Text("标签筛选") }, modifier = Modifier.fillMaxWidth())
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = { model.updateQuery(kind, query.copy(offset = 0)) }, enabled = !state.loading) { Text("应用筛选") }
                TextButton(onClick = { query = query.copy(workspaceId = "", offset = 0); model.updateQuery(kind, query) }) { Text("全部工作区") }
            }
            if (current.workspaceId.isNotEmpty()) Text("工作区：${current.workspaceId}")
        }
        if (data.isEmpty()) item { Text(if (state.loading) "正在读取 Core…" else "当前筛选下没有记录", Modifier.testTag("$kind-empty")) }
        item {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = { model.checkPage(kind) }, enabled = data.isNotEmpty()) { Text("选择本页") }
                Text("已选 ${state.checkedIds.size}", Modifier.padding(top = 12.dp))
            }
        }
        items(data, key = { it.id }) { item ->
            Card(Modifier.fillMaxWidth()) {
                Row(Modifier.padding(8.dp)) {
                    Checkbox(item.id in state.checkedIds, { model.toggleChecked(item.id) }, modifier = Modifier.testTag("check-${item.id}"))
                    Column(Modifier.weight(1f).clickable { if (tasks) model.selectTask(item) else model.selectConversation(item) }.padding(8.dp).testTag("item-${item.id}")) {
                        Text(item.title, style = MaterialTheme.typography.titleMedium)
                        Text(item.subtitle)
                        Text(listOf(item.status, item.metadata).filter { it.isNotBlank() }.joinToString(" · "))
                        val row = item.raw
                        if (row?.optBoolean("pinned") == true) Text("已置顶")
                        row?.optJSONArray("tags")?.let { Text("标签：${(0 until it.length()).joinToString("、") { index -> it.optString(index) }}") }
                    }
                }
            }
        }
        item {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = { model.updateQuery(kind, current.copy(offset = (current.offset - ManagementContract.PAGE_SIZE).coerceAtLeast(0))) }, enabled = current.offset > 0 && !state.loading) { Text("上一页") }
                Text("偏移 ${current.offset} · 总计 ${page.total?.toString() ?: "未知"}", Modifier.padding(top = 12.dp))
                TextButton(onClick = { page.nextOffset?.let { model.updateQuery(kind, current.copy(offset = it)) } }, enabled = page.hasMore && !state.loading) { Text("下一页") }
            }
        }
        if (selected != null || frozenIds.isNotEmpty()) {
            item {
                Text("管理 ${frozenIds.size} 个对象", style = MaterialTheme.typography.titleMedium)
                Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    labels.forEach { (action, label) ->
                        OutlinedButton(onClick = {
                            if (action in setOf("trash", "delete")) confirmation = action to frozenIds.toList()
                            else model.manage(kind, frozenIds.toList(), action)
                        }, enabled = frozenIds.isNotEmpty() && !state.actionBusy, modifier = Modifier.testTag("manage-$action")) { Text(label) }
                    }
                }
                OutlinedTextField(name, { name = it.take(512) }, label = { Text("新名称（单选）") }, modifier = Modifier.fillMaxWidth())
                TextButton(onClick = { model.manage(kind, frozenIds.toList(), "rename", title = name) }, enabled = frozenIds.size == 1 && name.isNotBlank() && !state.actionBusy) { Text("保存名称") }
                OutlinedTextField(tags, { tags = it.take(1024) }, label = { Text("标签，以逗号分隔；留空清除") }, modifier = Modifier.fillMaxWidth())
                TextButton(onClick = { model.manage(kind, frozenIds.toList(), "tags", tags = tags.split(',', '，').map { it.trim() }.filter { it.isNotEmpty() }.distinct()) }, enabled = frozenIds.isNotEmpty() && !state.actionBusy) { Text("保存标签") }
                if (!tasks && selected != null) Button(onClick = { model.navigate(WorkbenchScreen.InsertAndStop) }) { Text("插入与停止") }
            }
        }
        if (selected != null && state.detailId == selected.id && state.detailKind == kind) {
            item {
                Text("详情 · ${selected.title}", style = MaterialTheme.typography.titleMedium)
                if (state.detailError.isNotBlank()) Text(state.detailError)
                val detail = state.detail?.optJSONObject("task") ?: state.detail?.optJSONObject("conversation") ?: state.detail
                if (detail != null) DetailFields(detail)
                if (tasks) {
                    val steps = detail?.optJSONArray("steps") ?: detail?.optJSONObject("active_thread")?.optJSONArray("steps")
                    if (steps != null) for (index in 0 until steps.length()) {
                        val step = steps.optJSONObject(index) ?: continue
                        Text("${ManagementContract.text(step, "id")} · ${ManagementContract.text(step, "title")} · ${ManagementContract.text(step, "status")}")
                    }
                }
            }
        }
        state.batchResult?.optJSONArray("items")?.let { results ->
            item {
                Text("最近批量回执", style = MaterialTheme.typography.titleMedium)
                for (index in 0 until results.length()) {
                    val result = results.optJSONObject(index) ?: continue
                    Text("${ManagementContract.text(result, "id")} · ${ManagementContract.text(result, "status")}\n${ManagementContract.text(result, "message")}")
                }
            }
        }
    }
}

@Composable
fun DetailFields(value: JSONObject) {
    for (key in listOf("title", "goal", "summary", "status", "current_step_id", "parent_call_id", "tool_name", "display_command", "error_code", "error_summary", "created_at", "updated_at", "completed_at")) {
        val text = ManagementContract.text(value, key)
        if (text.isNotBlank()) Text("$key：${text.take(4096)}")
    }
}
