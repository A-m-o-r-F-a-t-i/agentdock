package dev.agentdock.workbench.data

import org.json.JSONObject
import java.net.URI
import java.util.Locale

/** The origin and Bearer are authenticated together inside the Keystore envelope. */
object CoreCredentialBinding {
    private const val MAX_BEARER_CHARS = 8192
    private const val MAX_ENVELOPE_CHARS = 16384

    fun encode(origin: URI, bearer: String): String {
        require(validBearer(bearer)) { "Bearer 必须是有界、无空白和控制字符的字符串" }
        val record = JSONObject().put("schema_version", 1).put("origin", originKey(origin))
            .put("bearer", bearer).toString()
        require(record.length <= MAX_ENVELOPE_CHARS) { "Core 凭据配置超过大小限制" }
        return record
    }

    fun decode(value: String, origin: URI): String {
        if (value.isBlank() || value.length > MAX_ENVELOPE_CHARS) return ""
        val record = runCatching { JSONObject(value) }.getOrNull() ?: return ""
        if (record.opt("schema_version") != 1 || record.opt("origin") != originKey(origin)) return ""
        val bearer = record.opt("bearer") as? String ?: return ""
        return if (validBearer(bearer)) bearer else ""
    }

    internal fun originKey(origin: URI): String {
        val scheme = origin.scheme?.lowercase(Locale.ROOT)
        require(scheme == "http" || scheme == "https") { "Invalid Core scheme" }
        require(origin.userInfo == null && origin.query == null && origin.fragment == null &&
            (origin.path.isNullOrEmpty() || origin.path == "/")) { "Core credential scope must be an origin" }
        val host = requireNotNull(origin.host).lowercase(Locale.ROOT)
        val port = if (origin.port == -1) (if (scheme == "https") 443 else 80) else origin.port
        require(port in 1..65535)
        return "$scheme://$host:$port"
    }

    private fun validBearer(value: String): Boolean = value.length in 1..MAX_BEARER_CHARS &&
        value.all { it.code in 33..126 }
}
