package dev.agentdock.workbench.data

import android.content.Context
import android.net.Uri
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.time.Instant
import kotlin.coroutines.coroutineContext

/** Build a complete bounded snapshot before touching the user-selected export URI. */
class RecordExporter(private val context: Context) {
    suspend fun write(uri: Uri, records: JSONObject) = withContext(Dispatchers.IO) {
        val envelope = JSONObject().put("schema_version", 1).put("exported_at", Instant.now().toString())
            .put("source", "AgentDock Core explicit client export").put("records", records)
        val bytes = envelope.toString(2).toByteArray(Charsets.UTF_8)
        require(bytes.size <= 16 * 1024 * 1024) { "导出超过 16 MiB，请缩小筛选范围" }
        val temporary = File.createTempFile("workbench-export-", ".json", context.cacheDir)
        try {
            temporary.outputStream().use { stream -> stream.write(bytes); stream.fd.sync() }
            coroutineContext.ensureActive()
            context.contentResolver.openOutputStream(uri, "wt")?.use { output ->
                temporary.inputStream().use { input ->
                    val buffer = ByteArray(65536)
                    while (true) {
                        coroutineContext.ensureActive()
                        val length = input.read(buffer)
                        if (length == -1) break
                        output.write(buffer, 0, length)
                    }
                }
                output.flush()
            } ?: error("无法打开用户选择的导出文件")
        } finally { temporary.delete() }
    }

    suspend fun calls(client: CoreClient, filter: CallFilter): JSONObject {
        var before = filter.before
        val seen = HashSet<String>()
        val records = JSONArray()
        var bytes = 0
        for (page in 0 until 100) {
            coroutineContext.ensureActive()
            val value = client.get(filter.copy(before = before).path())
            for (record in CorePages.rows(value, "calls")) {
                val id = record.optString("call_id")
                require(id.isNotBlank() && seen.add(id)) { "导出收到重复或无标识记录，未写文件" }
                bytes += record.toString().toByteArray(Charsets.UTF_8).size
                require(bytes <= 12 * 1024 * 1024 && records.length() < 10000) { "导出超过 10,000 条或 12 MiB，请缩小范围" }
                records.put(record)
            }
            val next = CorePages.next(value, before, "next_before", descending = true)
            if (next == null) return JSONObject().put("calls", records).put("includes_output", false).put("filter", filter.path())
            before = next
        }
        error("导出超过 100 页，未写文件。请缩小筛选范围。")
    }
}
