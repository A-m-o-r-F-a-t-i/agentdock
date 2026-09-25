package dev.agentdock.workbench.termux

import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import androidx.core.content.ContextCompat
import dev.agentdock.workbench.model.ActionOutcome
import dev.agentdock.workbench.model.BridgeOperation
import org.json.JSONObject
import java.security.SecureRandom
import java.util.Base64
import java.util.UUID
import java.util.concurrent.atomic.AtomicInteger

class TermuxCommandDispatcher(
    private val context: Context,
    private val operations: PendingOperationStore
) {
    fun availability(): ActionOutcome {
        val packageInstalled = runCatching { context.packageManager.getPackageInfo(TermuxContract.PACKAGE, 0) }.isSuccess
        if (!packageInstalled) return ActionOutcome(false, "termux_missing", "未安装外部 Termux")
        if (ContextCompat.checkSelfPermission(context, TermuxContract.PERMISSION_RUN_COMMAND) != PackageManager.PERMISSION_GRANTED) {
            return ActionOutcome(false, "permission_missing", "请授予 Termux RUN_COMMAND 权限")
        }
        val intent = baseIntent()
        if (context.packageManager.resolveService(intent, PackageManager.MATCH_DEFAULT_ONLY) == null) {
            return ActionOutcome(false, "service_missing", "Termux RUN_COMMAND 服务不可用；请启用 allow-external-apps")
        }
        return ActionOutcome(true, "available", "Termux RUN_COMMAND 可用")
    }

    fun dispatch(operation: String, payload: JSONObject = JSONObject()): BridgeOperation {
        require(operation in TermuxContract.OPERATIONS) { "Unsupported Termux operation" }
        val availability = availability()
        check(availability.accepted) { availability.message }
        val operationId = "op_${UUID.randomUUID().toString().replace("-", "")}"
        val requestId = "req_${UUID.randomUUID().toString().replace("-", "")}"
        val nonce = nonce()
        val now = System.currentTimeMillis()
        val pending = BridgeOperation(
            operationId = operationId,
            requestId = requestId,
            nonce = nonce,
            operation = operation,
            phase = "queued",
            createdAtEpochMs = now
        )
        operations.create(pending)

        val payloadValue = JSONObject(payload.toString())
            .put("schema_version", 1)
            .put("operation_id", operationId)
            .put("request_id", requestId)
            .put("nonce", nonce)
            .put("operation", operation)
        val payloadText = payloadValue.toString()
        require(payloadText.toByteArray().size <= TermuxContract.MAX_PAYLOAD_BYTES) { "Termux payload too large" }

        val callbackIntent = Intent(context, TermuxResultService::class.java)
            .setAction(TermuxContract.CALLBACK_ACTION_PREFIX + requestId)
            .putExtra(TermuxResultService.EXTRA_OPERATION_ID, operationId)
            .putExtra(TermuxResultService.EXTRA_REQUEST_ID, requestId)
            .putExtra(TermuxResultService.EXTRA_NONCE, nonce)
        val callback = PendingIntent.getService(
            context,
            requestCode.incrementAndGet(),
            callbackIntent,
            PendingIntent.FLAG_ONE_SHOT or PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
        )

        val intent = baseIntent()
            .putExtra(TermuxContract.EXTRA_COMMAND_PATH, TermuxContract.COMMAND)
            .putExtra(TermuxContract.EXTRA_ARGUMENTS, arrayOf(operation, operationId, requestId, nonce, "-"))
            .putExtra(TermuxContract.EXTRA_STDIN, payloadText)
            .putExtra(TermuxContract.EXTRA_WORKDIR, TermuxContract.WORKDIR)
            .putExtra(TermuxContract.EXTRA_BACKGROUND, true)
            .putExtra(TermuxContract.EXTRA_PENDING_INTENT, callback)
        try {
            context.startService(intent)
            operations.finish(pending, "running", "Termux 已接受命令，等待结构化回执", null, false, false)
        } catch (error: Exception) {
            operations.finish(pending, "failed", error.message ?: "无法启动 Termux RUN_COMMAND", null, false, false)
            throw error
        }
        return operations.get(operationId) ?: pending
    }

    private fun baseIntent() = Intent(TermuxContract.ACTION_RUN_COMMAND).apply {
        component = TermuxContract.RUN_COMMAND_COMPONENT
        setPackage(TermuxContract.PACKAGE)
    }

    private fun nonce(): String {
        val bytes = ByteArray(24)
        random.nextBytes(bytes)
        return Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)
    }

    companion object {
        private val random = SecureRandom()
        private val requestCode = AtomicInteger(10_000)
    }
}
