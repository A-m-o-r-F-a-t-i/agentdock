package dev.agentdock.workbench.termux

import android.app.Service
import android.content.Intent
import android.os.Bundle
import android.os.IBinder
import dev.agentdock.workbench.WorkbenchApplication
import dev.agentdock.workbench.model.BridgeOperation
import org.json.JSONObject

class TermuxResultService : Service() {
    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        try {
            intent?.let(::handle)
        } finally {
            stopSelf(startId)
        }
        return START_NOT_STICKY
    }

    private fun handle(intent: Intent) {
        val operationId = intent.getStringExtra(EXTRA_OPERATION_ID).orEmpty()
        val requestId = intent.getStringExtra(EXTRA_REQUEST_ID).orEmpty()
        val nonce = intent.getStringExtra(EXTRA_NONCE).orEmpty()
        val store = (application as WorkbenchApplication).graph.operations
        val expected = store.get(operationId) ?: return
        if (expected.requestId != requestId || expected.nonce != nonce || intent.action != TermuxContract.CALLBACK_ACTION_PREFIX + requestId) return

        val bundle = intent.getBundleExtra(TermuxContract.EXTRA_RESULT_BUNDLE) ?: Bundle.EMPTY
        val stdout = bundle.getString(TermuxContract.RESULT_STDOUT).orEmpty()
        val stderr = bundle.getString(TermuxContract.RESULT_STDERR).orEmpty()
        val stdoutOriginal = bundle.getInt(TermuxContract.RESULT_STDOUT_ORIGINAL_LENGTH, stdout.length)
        val stderrOriginal = bundle.getInt(TermuxContract.RESULT_STDERR_ORIGINAL_LENGTH, stderr.length)
        val exitCode = bundle.getInt(TermuxContract.RESULT_EXIT_CODE, Int.MIN_VALUE).takeIf { it != Int.MIN_VALUE }
        val pluginError = bundle.getInt(TermuxContract.RESULT_ERR, 0)
        val pluginMessage = bundle.getString(TermuxContract.RESULT_ERRMSG).orEmpty()
        val result = TermuxResultValidator.validate(expected, stdout, stderr, exitCode, pluginError, pluginMessage)
        if (result.phase != "failed") {
            runCatching {
                val credential = JSONObject(stdout).optJSONObject("credential")
                val type = credential?.optString("type").orEmpty()
                val value = credential?.optString("value").orEmpty()
                if (type == "bearer" && Regex("^[A-Fa-f0-9]{64}$").matches(value)) {
                    (application as WorkbenchApplication).graph.credentials.put("core_bearer", value)
                }
            }
        }
        store.finish(
            expected = expected,
            phase = result.phase,
            message = result.message,
            exitCode = exitCode,
            stdoutTruncated = stdoutOriginal > stdout.length || stdout.length >= TermuxContract.MAX_RESULT_CHARS,
            stderrTruncated = stderrOriginal > stderr.length || stderr.length >= TermuxContract.MAX_RESULT_CHARS
        )
    }

    companion object {
        const val EXTRA_OPERATION_ID = "operation_id"
        const val EXTRA_REQUEST_ID = "request_id"
        const val EXTRA_NONCE = "nonce"
    }
}

data class ValidatedTermuxResult(val phase: String, val message: String)

object TermuxResultValidator {
    fun validate(
        expected: BridgeOperation,
        stdout: String,
        stderr: String,
        exitCode: Int?,
        pluginError: Int,
        pluginMessage: String
    ): ValidatedTermuxResult {
        if (pluginError != 0) return ValidatedTermuxResult("failed", pluginMessage.ifBlank { "Termux 插件错误 $pluginError" })
        if (stdout.length > TermuxContract.MAX_RESULT_CHARS || stderr.length > TermuxContract.MAX_RESULT_CHARS) {
            return ValidatedTermuxResult("failed", "Termux 回执超过大小限制")
        }
        val json = runCatching { JSONObject(stdout) }.getOrElse {
            val fallback = stderr.trim().take(512).ifBlank { "Termux 未返回有效 JSON" }
            return ValidatedTermuxResult("failed", fallback)
        }
        if (json.optInt("schema_version") != 1 ||
            json.optString("operation_id") != expected.operationId ||
            json.optString("request_id") != expected.requestId ||
            json.optString("nonce") != expected.nonce ||
            json.optString("operation") != expected.operation
        ) return ValidatedTermuxResult("failed", "Termux 回执与请求绑定不一致")

        val status = json.optString("status")
        val message = json.optString("message").take(2048).ifBlank { status }
        return when {
            status == "pending_manifest" -> ValidatedTermuxResult("pending_manifest", message)
            status == "requires_user_action" -> ValidatedTermuxResult("requires_user_action", message)
            status in setOf("ok", "healthy", "running", "stopped", "adopted", "installed", "updated", "rolled_back") && exitCode == 0 ->
                ValidatedTermuxResult("succeeded", message)
            else -> ValidatedTermuxResult("failed", message.ifBlank { "Termux operation failed (exit ${exitCode ?: "unknown"})" })
        }
    }
}
