package dev.agentdock.workbench.ui

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import dev.agentdock.workbench.data.CoreCommand
import dev.agentdock.workbench.data.ManagementContract
import org.json.JSONObject

class CommandPrompt(private val model: WorkbenchViewModel) {
    var pending by mutableStateOf<CoreCommand?>(null)
    var after: ((JSONObject) -> Unit)? = null
    fun ask(builder: () -> CoreCommand, accepted: (JSONObject) -> Unit = {}) {
        try { pending = builder(); after = accepted }
        catch (error: Exception) { model.showNotice(ManagementContract.failure(error).ifBlank { "请核对必填字段及参数范围。" }) }
    }
}

@Composable
fun rememberCommandPrompt(model: WorkbenchViewModel): CommandPrompt {
    val prompt = remember(model) { CommandPrompt(model) }
    prompt.pending?.let { command ->
        AlertDialog(onDismissRequest = { prompt.pending = null },
            title = { Text(command.title) },
            text = { Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(command.consequence)
                Text("目标：${command.path}")
                Text("确认后只发送一次。取消等待不能保证撤销已提交的服务器写入。")
            } },
            confirmButton = { TextButton(onClick = {
                val callback = prompt.after
                prompt.pending = null; prompt.after = null
                model.resources.execute(command) { callback?.invoke(it) }
            }, modifier = Modifier.testTag("confirm-core-action")) { Text("确认") } },
            dismissButton = { TextButton(onClick = { prompt.pending = null; prompt.after = null }) { Text("取消") } })
    }
    return prompt
}

@Composable
fun ResourceFeedback(view: ResourceView?, retry: () -> Unit) {
    when {
        view == null || view.loading -> LinearProgressIndicator(Modifier.fillMaxWidth())
        view.error.isNotEmpty() -> Card(Modifier.fillMaxWidth()) {
            Column(Modifier.padding(12.dp)) {
                Text("读取失败", style = MaterialTheme.typography.titleMedium)
                Text(view.error)
                TextButton(onClick = retry) { Text("重新读取") }
            }
        }
    }
}

@Composable
fun ResourceSection(title: String, content: @Composable ColumnScope.() -> Unit) {
    ElevatedCard(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(title, style = MaterialTheme.typography.titleMedium)
            content()
        }
    }
}

@Composable
fun ResourceField(label: String, value: String, change: (String) -> Unit, multiline: Boolean = false) {
    OutlinedTextField(value, change, label = { Text(label) }, modifier = Modifier.fillMaxWidth(),
        singleLine = !multiline, minLines = if (multiline) 3 else 1)
}

@Composable
fun ResourceActions(content: @Composable RowScope.() -> Unit) {
    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp), content = content)
}

@Composable
fun ResourceObject(value: JSONObject, keys: List<String> = listOf("title", "name", "summary", "status", "workspace_id", "call_id", "parent_call_id", "tool_name", "display_command", "error_code", "error_summary", "created_at", "updated_at", "completed_at")) {
    keys.forEach { key ->
        val raw = value.opt(key)
        if (raw != null && raw != JSONObject.NULL && raw !is JSONObject && raw !is org.json.JSONArray && raw.toString().isNotBlank()) {
            Text("$key：${raw.toString().take(4096)}")
        }
    }
}

@Composable
fun ResourceReceipt(value: JSONObject?) {
    if (value == null) return
    ResourceSection("最近管理回执") {
        ResourceObject(value, listOf("status", "message", "ok", "succeeded", "skipped", "failed", "has_remaining_activity", "revision"))
        value.optJSONArray("warnings")?.let { warnings ->
            for (index in 0 until minOf(warnings.length(), 100)) Text(warnings.optString(index).take(2048))
        }
        value.optJSONArray("items")?.let { items ->
            for (index in 0 until minOf(items.length(), 200)) items.optJSONObject(index)?.let {
                Text("${it.optString("id")} · ${it.optString("status")} · ${it.optString("message").take(2048)}")
            }
        }
    }
}
