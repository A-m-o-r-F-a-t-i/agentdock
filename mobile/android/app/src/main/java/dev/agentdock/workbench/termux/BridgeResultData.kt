package dev.agentdock.workbench.termux

import org.json.JSONArray
import org.json.JSONObject

/** Only validated deployment metadata is retained; raw process output is not stored. */
object BridgeResultData {
    fun sanitize(value: JSONObject?): String {
        if (value == null) return ""
        if (value.toString().toByteArray(Charsets.UTF_8).size > 48 * 1024 || TermuxResultPolicy.containsSecretFields(value)) {
            return JSONObject().put("data_omitted", true).toString()
        }
        val copy = JSONObject(value.toString())
        fun visit(current: Any, depth: Int) {
            require(depth < 20)
            when (current) {
                is JSONObject -> current.keys().asSequence().toList().forEach { key ->
                    when (val child = current.opt(key)) {
                        is JSONObject, is JSONArray -> visit(child, depth + 1)
                        is String -> if (key in setOf("text", "message", "last_error", "detail")) {
                            val lines = child.lineSequence().take(200).map { line ->
                                if (line.length > 8192) "[超长日志行已省略]" else TermuxResultPolicy.safeMessage(line)
                            }.joinToString("\n")
                            current.put(key, lines.take(24000))
                            if (lines != child) current.put("text_redacted_or_bounded", true)
                        }
                    }
                }
                is JSONArray -> for (index in 0 until current.length()) {
                    val child = current.opt(index)
                    if (child is JSONObject || child is JSONArray) visit(child, depth + 1)
                }
            }
        }
        return runCatching { visit(copy, 0); copy.toString() }.getOrElse {
            JSONObject().put("data_omitted", true).toString()
        }
    }
}
