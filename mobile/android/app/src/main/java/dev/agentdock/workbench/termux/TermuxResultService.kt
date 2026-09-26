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
            intent?.let { runCatching { handle(it) } }
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
        if (!TermuxResultPolicy.mayComplete(expected, System.currentTimeMillis())) return
        if (expected.requestId != requestId || expected.nonce != nonce || intent.action != TermuxContract.CALLBACK_ACTION_PREFIX + requestId) return

        val bundle = intent.getBundleExtra(TermuxContract.EXTRA_RESULT_BUNDLE) ?: Bundle.EMPTY
        val stdout = bundle.getString(TermuxContract.RESULT_STDOUT).orEmpty()
        val stderr = bundle.getString(TermuxContract.RESULT_STDERR).orEmpty()
        val stdoutOriginal = bundle.getInt(TermuxContract.RESULT_STDOUT_ORIGINAL_LENGTH, stdout.length)
        val stderrOriginal = bundle.getInt(TermuxContract.RESULT_STDERR_ORIGINAL_LENGTH, stderr.length)
        val exitCode = bundle.getInt(TermuxContract.RESULT_EXIT_CODE, Int.MIN_VALUE).takeIf { it != Int.MIN_VALUE }
        val pluginError = bundle.getInt(TermuxContract.RESULT_ERR, 0)
        val pluginMessage = bundle.getString(TermuxContract.RESULT_ERRMSG).orEmpty()
        val result = if (stdoutOriginal > stdout.length) {
            ValidatedTermuxResult("failed", "Termux 回执被截断，结果待重新核对")
        } else {
            TermuxResultValidator.validate(expected, stdout, stderr, exitCode, pluginError, pluginMessage)
        }
        store.finish(
            expected = expected,
            phase = result.phase,
            message = result.message,
            exitCode = exitCode,
            stdoutTruncated = stdoutOriginal > stdout.length || stdout.length >= TermuxContract.MAX_RESULT_CHARS,
            stderrTruncated = stderrOriginal > stderr.length || stderr.length >= TermuxContract.MAX_RESULT_CHARS,
            resultJson = result.dataJson
        )
    }

    companion object {
        const val EXTRA_OPERATION_ID = "operation_id"
        const val EXTRA_REQUEST_ID = "request_id"
        const val EXTRA_NONCE = "nonce"
    }
}

data class ValidatedTermuxResult(val phase: String, val message: String, val dataJson: String = "")

object TermuxResultValidator {
    fun validate(
        expected: BridgeOperation,
        stdout: String,
        stderr: String,
        exitCode: Int?,
        pluginError: Int,
        @Suppress("UNUSED_PARAMETER") pluginMessage: String,
        nowEpochMs: Long = System.currentTimeMillis()
    ): ValidatedTermuxResult {
        if (!TermuxResultPolicy.mayComplete(expected, nowEpochMs)) {
            return ValidatedTermuxResult("failed", "回执已过期或操作已有终态，请查询原操作记录")
        }
        if (pluginError != 0) return ValidatedTermuxResult("failed", "Termux 执行通道错误（$pluginError）")
        if (stdout.length > TermuxContract.MAX_RESULT_CHARS || stderr.length > TermuxContract.MAX_RESULT_CHARS ||
            stdout.toByteArray(Charsets.UTF_8).size > TermuxContract.MAX_RESULT_CHARS) {
            return ValidatedTermuxResult("failed", "Termux 回执超过大小限制")
        }
        val json = runCatching { JSONObject(stdout) }.getOrElse {
            return ValidatedTermuxResult("failed", "Termux 未返回有效 JSON；原始输出不写入操作摘要")
        }
        if (json.opt("schema_version") != 1 ||
            json.optString("operation_id") != expected.operationId ||
            json.optString("request_id") != expected.requestId ||
            json.optString("nonce") != expected.nonce ||
            json.optString("operation") != expected.operation
        ) return ValidatedTermuxResult("failed", "Termux 回执与请求绑定不一致")

        if (TermuxResultPolicy.containsSecretFields(json)) {
            return ValidatedTermuxResult("failed", "旧桥返回了凭据字段，已拒绝导入；请更新桥并使用管理连接配对")
        }
        val dataJson = BridgeResultData.sanitize(json.optJSONObject("data"))
        if (exitCode != 0) return ValidatedTermuxResult("failed",
            TermuxResultPolicy.safeMessage(json.optString("message")).ifBlank { "Termux 执行失败（exit ${exitCode ?: "unknown"}）" }, dataJson)
        val status = json.optString("status")
        val message = TermuxResultPolicy.safeMessage(json.optString("message")).ifBlank { status }
        return when {
            status == "pending_manifest" -> ValidatedTermuxResult("pending_manifest", message, dataJson)
            status == "requires_user_action" -> ValidatedTermuxResult("requires_user_action", message, dataJson)
            status in setOf("ok", "healthy", "running", "stopped", "adopted", "installed", "updated", "rolled_back") && exitCode == 0 ->
                ValidatedTermuxResult("succeeded", message, dataJson)
            else -> ValidatedTermuxResult("failed", message.ifBlank { "Termux operation failed (exit ${exitCode ?: "unknown"})" })
        }
    }
}
