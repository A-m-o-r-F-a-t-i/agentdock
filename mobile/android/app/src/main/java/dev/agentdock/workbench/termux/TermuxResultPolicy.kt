package dev.agentdock.workbench.termux

import dev.agentdock.workbench.model.BridgeOperation
import org.json.JSONArray
import org.json.JSONObject
import java.util.ArrayDeque

/** Callback transport never carries Core credentials or changes a terminal result. */
object TermuxResultPolicy {
    const val MAX_CALLBACK_AGE_MS = 2 * 60 * 60 * 1000L
    val pendingPhases = setOf("queued", "running")
    val terminalPhases = setOf("succeeded", "failed", "pending_manifest", "requires_user_action")

    fun mayComplete(operation: BridgeOperation, nowEpochMs: Long): Boolean {
        val age = nowEpochMs - operation.createdAtEpochMs
        return operation.phase in pendingPhases && age in 0..MAX_CALLBACK_AGE_MS
    }

    fun containsSecretFields(value: JSONObject): Boolean {
        val pending = ArrayDeque<Any>()
        pending.add(value)
        var visited = 0
        while (pending.isNotEmpty()) {
            if (++visited > 4096) return true
            when (val item = pending.removeFirst()) {
                is JSONObject -> {
                    val keys = item.keys()
                    while (keys.hasNext()) {
                        val key = keys.next()
                        val normalized = key.lowercase()
                        if (secretNames.any { normalized == it || normalized.endsWith("_" + it) }) return true
                        val child = item.opt(key)
                        if (child is JSONObject || child is JSONArray) pending.add(child)
                    }
                }
                is JSONArray -> for (index in 0 until item.length()) {
                    val child = item.opt(index)
                    if (child is JSONObject || child is JSONArray) pending.add(child)
                }
            }
        }
        return false
    }

    fun safeMessage(value: String): String {
        val bounded = value.take(8192)
        val bearerRedacted = bearer.replace(bounded, "Bearer <redacted>")
        val assignmentsRedacted = assignment.replace(bearerRedacted) { match -> match.groupValues[1] + "<redacted>" }
        return opaqueSecret.replace(assignmentsRedacted, "<redacted>").take(2048)
    }

    private val secretNames = setOf("token", "bearer", "secret", "password", "credential", "credentials", "authorization", "cookie")
    private val bearer = Regex("""(?i)bearer[ \t]+[^\s,;"]+""")
    private val assignment = Regex("""(?i)((?:token|secret|password|authorization|cookie)["' \t:=]+)(?:"[^"]*"|'[^']*'|[^\s,;]+)""")
    private val opaqueSecret = Regex("(?<![A-Za-z0-9])[A-Fa-f0-9]{64}(?![A-Za-z0-9])")
}
