package dev.agentdock.workbench.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.agentdock.workbench.data.CorePages
import dev.agentdock.workbench.data.ManagementContract
import dev.agentdock.workbench.data.WorkbenchCommands
import dev.agentdock.workbench.model.WorkbenchItem
import org.json.JSONObject

@Composable
fun WorkspacePage(state: WorkbenchUiState, model: WorkbenchViewModel, modifier: Modifier) {
    val resources by model.resources.state.collectAsStateWithLifecycle()
    val view = resources.views["workspaces"]
    fun reload() = model.resources.read("workspaces", "/internal/runtime/workspaces")
    LaunchedEffect(state.settings.endpoint, state.settings.remoteEndpointEnabled) { reload() }
    val rows = remember(view?.data) { runCatching { view?.data?.let { CorePages.rows(it, "workspaces") }.orEmpty() } }
    val prompt = rememberCommandPrompt(model)
    var collapsed by rememberSaveable { mutableStateOf(false) }
    var selected by rememberSaveable { mutableStateOf("") }
    var name by rememberSaveable { mutableStateOf("") }
    var root by rememberSaveable { mutableStateOf("") }
    var project by rememberSaveable { mutableStateOf("") }
    var create by rememberSaveable { mutableStateOf(false) }
    var artifacts by rememberSaveable { mutableStateOf("") }
    var scratch by rememberSaveable { mutableStateOf("") }
    var cache by rememberSaveable { mutableStateOf("") }
    var resolve by rememberSaveable { mutableStateOf(".") }
    var revision by rememberSaveable { mutableStateOf(0L) }
    val detail = resources.views["workspace-detail"]?.data?.let { it.optJSONObject("workspace") ?: it }
    LaunchedEffect(detail, selected) {
        if (selected.isNotBlank() && detail != null && detail.optString("workspace_id", detail.optString("id")) == selected) {
            name = detail.optString("name"); root = detail.optString("root"); project = detail.optString("project")
            artifacts = detail.optString("artifact_root"); scratch = detail.optString("scratch_root"); cache = detail.optString("cache_root")
            revision = detail.optLong("rules_revision", detail.optLong("revision", 0))
        }
    }
    LazyColumn(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Text("工作区", style = MaterialTheme.typography.headlineSmall)
            Text("目录由 Core 工作区注册表管理。Android SAF URI 与 Linux 路径分别授权。")
            ResourceActions {
                TextButton(onClick = { collapsed = !collapsed }) { Text(if (collapsed) "展开全部" else "折叠全部") }
                TextButton(onClick = { selected = ""; name = ""; root = ""; project = ""; revision = 0; artifacts = ""; scratch = ""; cache = "" }) { Text("新增工作区") }
                TextButton(onClick = ::reload) { Text("刷新") }
            }
            ResourceFeedback(view, ::reload)
            rows.exceptionOrNull()?.let { Text(it.message.orEmpty()) }
            if (view?.data != null && rows.getOrDefault(emptyList()).isEmpty()) Text("Core 没有返回工作区记录。")
        }
        if (!collapsed) items(rows.getOrDefault(emptyList()), key = { it.optString("workspace_id", it.optString("id")) }) { row ->
            val id = row.optString("workspace_id", row.optString("id"))
            ResourceSection(row.optString("name", row.optString("title", id))) {
                ResourceObject(row, listOf("workspace_id", "root", "project", "runtime", "rules_revision"))
                ResourceActions {
                    TextButton(onClick = { model.selectWorkspace(WorkbenchItem(id, row.optString("name", id))) }) { Text("查看对话与任务") }
                    TextButton(onClick = {
                        selected = id; revision = 0
                        model.resources.read("workspace-detail", "/internal/runtime/workspaces/${ManagementContract.id(id)}")
                    }) { Text("读取并编辑") }
                }
            }
        }
        item {
            ResourceSection(if (selected.isEmpty()) "登记工作区" else "编辑工作区 $selected") {
                ResourceField("名称", name, { name = it.take(256) })
                ResourceField("Core 原生绝对路径", root, { root = it.take(4096) })
                ResourceField("项目标识", project, { project = it.take(256) })
                ResourceField("产物根目录（可选）", artifacts, { artifacts = it.take(4096) })
                ResourceField("暂存根目录（可选）", scratch, { scratch = it.take(4096) })
                ResourceField("缓存根目录（可选）", cache, { cache = it.take(4096) })
                Row { Checkbox(create, { create = it }); Text("允许创建上面明确指定的目录", Modifier.padding(top = 12.dp)) }
                Text("当前修订号：${if (selected.isBlank()) "新记录" else revision.toString()}")
                Button(onClick = { prompt.ask({ WorkbenchCommands.workspace(name, root, project, create, selected, revision, artifacts, scratch, cache) }) { reload() } }, enabled = !resources.actionBusy && (selected.isBlank() || revision > 0)) { Text("保存工作区") }
                if (selected.isNotBlank()) {
                    ResourceField("待解析的相对路径", resolve, { resolve = it.take(4096) })
                    OutlinedButton(onClick = { prompt.ask({ WorkbenchCommands.workspaceResolve(selected, resolve) }) }) { Text("校验路径归属") }
                }
                Text("当前共享接口没有注销或设置默认工作区操作，相关按钮不发送替代命令。")
            }
        }
        item { ResourceReceipt(resources.actionResult) }
    }
}
