package dev.agentdock.workbench.termux

import android.content.Context
import android.util.AtomicFile
import dev.agentdock.workbench.model.BridgeOperation
import org.json.JSONObject
import java.io.File
import java.nio.charset.StandardCharsets

class PendingOperationStore(context: Context) {
    private val directory = File(context.filesDir, "operations").apply { mkdirs() }

    @Synchronized
    fun create(value: BridgeOperation) {
        require(validId(value.operationId) && validId(value.requestId))
        check(get(value.operationId) == null) { "Operation ID already exists" }
        trim(reserve = 1)
        check(list(MAX_FILES).size < MAX_FILES) { "Unresolved operation capacity reached" }
        write(value)
    }

    @Synchronized
    fun get(operationId: String): BridgeOperation? {
        if (!validId(operationId)) return null
        val file = file(operationId)
        if (!file.isFile || file.length() > MAX_FILE_BYTES) return null
        return runCatching { decode(file.readText(StandardCharsets.UTF_8)) }.getOrNull()
    }

    @Synchronized
    fun list(limit: Int = 50): List<BridgeOperation> = directory.listFiles()
        .orEmpty()
        .filter { it.isFile && it.name.endsWith(".json") && it.length() <= MAX_FILE_BYTES }
        .sortedByDescending { it.lastModified() }
        .take(limit.coerceIn(1, MAX_FILES))
        .mapNotNull { runCatching { decode(it.readText(StandardCharsets.UTF_8)) }.getOrNull() }

    @Synchronized
    fun finish(
        expected: BridgeOperation,
        phase: String,
        message: String,
        exitCode: Int?,
        stdoutTruncated: Boolean,
        stderrTruncated: Boolean
    ): BridgeOperation {
        val current = checkNotNull(get(expected.operationId)) { "Unknown operation" }
        require(current.requestId == expected.requestId && current.nonce == expected.nonce)
        if (current.phase in TermuxResultPolicy.terminalPhases) return current
        require(phase in TermuxResultPolicy.pendingPhases || phase in TermuxResultPolicy.terminalPhases)
        val next = current.copy(
            phase = phase,
            message = TermuxResultPolicy.safeMessage(message).take(MAX_MESSAGE_CHARS),
            updatedAtEpochMs = System.currentTimeMillis(),
            exitCode = exitCode,
            stdoutTruncated = stdoutTruncated,
            stderrTruncated = stderrTruncated
        )
        write(next)
        return next
    }

    private fun write(value: BridgeOperation) {
        val target = AtomicFile(file(value.operationId))
        val bytes = encode(value).toString().toByteArray(StandardCharsets.UTF_8)
        require(bytes.size <= MAX_FILE_BYTES)
        val stream = target.startWrite()
        try {
            stream.write(bytes)
            stream.write('\n'.code)
            target.finishWrite(stream)
        } catch (error: Throwable) {
            target.failWrite(stream)
            throw error
        }
    }

    private fun trim(reserve: Int) {
        val records = list(MAX_FILES)
        val removable = records.filter { it.phase in TermuxResultPolicy.terminalPhases }
            .sortedBy { it.updatedAtEpochMs }
        val count = (records.size + reserve - MAX_FILES).coerceAtLeast(0)
        removable.take(count).forEach { AtomicFile(file(it.operationId)).delete() }
    }

    private fun file(operationId: String) = File(directory, "$operationId.json")

    private fun encode(value: BridgeOperation) = JSONObject()
        .put("schema_version", value.schemaVersion)
        .put("operation_id", value.operationId)
        .put("request_id", value.requestId)
        .put("nonce", value.nonce)
        .put("operation", value.operation)
        .put("phase", value.phase)
        .put("message", value.message)
        .put("created_at_epoch_ms", value.createdAtEpochMs)
        .put("updated_at_epoch_ms", value.updatedAtEpochMs)
        .put("exit_code", value.exitCode ?: JSONObject.NULL)
        .put("stdout_truncated", value.stdoutTruncated)
        .put("stderr_truncated", value.stderrTruncated)

    private fun decode(value: String): BridgeOperation {
        val json = JSONObject(value)
        require(json.optInt("schema_version") == 1)
        return BridgeOperation(
            operationId = json.getString("operation_id"),
            requestId = json.getString("request_id"),
            nonce = json.getString("nonce"),
            operation = json.getString("operation"),
            phase = json.optString("phase", "queued"),
            message = json.optString("message"),
            createdAtEpochMs = json.getLong("created_at_epoch_ms"),
            updatedAtEpochMs = json.optLong("updated_at_epoch_ms", json.getLong("created_at_epoch_ms")),
            exitCode = if (json.isNull("exit_code")) null else json.getInt("exit_code"),
            stdoutTruncated = json.optBoolean("stdout_truncated"),
            stderrTruncated = json.optBoolean("stderr_truncated")
        )
    }

    companion object {
        private const val MAX_FILE_BYTES = 16 * 1024L
        private const val MAX_FILES = 128
        private const val MAX_MESSAGE_CHARS = 2048
        private fun validId(value: String) = Regex("^[A-Za-z0-9_-]{1,96}$").matches(value)
    }
}
