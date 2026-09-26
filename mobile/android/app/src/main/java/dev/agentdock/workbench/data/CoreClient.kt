package dev.agentdock.workbench.data

import dev.agentdock.workbench.model.ActionOutcome
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.URI
import java.net.URL
import java.nio.charset.StandardCharsets
import kotlin.coroutines.coroutineContext

class CoreRequestException(
    val statusCode: Int,
    val errorCode: String,
    override val message: String
) : IOException(message)

data class CoreEndpoint(val origin: URI, val bearerToken: String)

data class SseMessage(val event: String, val id: Long, val data: String)

object EndpointPolicy {
    fun resolve(value: String, remoteEnabled: Boolean): URI {
        val uri = runCatching { URI(value.trim().trimEnd('/')) }
            .getOrElse { throw IllegalArgumentException("Core 地址无效") }
        require(uri.path.isNullOrEmpty() && uri.query == null && uri.fragment == null && uri.userInfo == null) {
            "Core 地址只能包含 scheme、host 和 port"
        }
        val host = uri.host?.removeSurrounding("[", "]")?.lowercase() ?: throw IllegalArgumentException("Core 地址缺少 host")
        val loopback = isLoopbackHost(host)
        if (loopback) {
            require(uri.scheme == "http" || uri.scheme == "https") { "本机 Core 仅支持 HTTP/HTTPS" }
        } else {
            require(remoteEnabled) { "远程 Core 必须先由用户显式启用" }
            require(uri.scheme == "https") { "远程 Core 必须使用 HTTPS" }
        }
        require(uri.port == -1 || uri.port in 1..65535) { "Core 端口无效" }
        return uri
    }

    private fun isLoopbackHost(host: String): Boolean {
        if (host == "localhost" || host == "::1" || host == "0:0:0:0:0:0:0:1") return true
        val parts = host.split('.')
        return parts.size == 4 && parts.firstOrNull() == "127" && parts.all { part ->
            part.length in 1..3 && part.all(Char::isDigit) && part.toIntOrNull() in 0..255
        }
    }
}

class CoreClient(private val endpoint: CoreEndpoint) {
    suspend fun get(path: String): JSONObject = request("GET", path, null)
    suspend fun post(path: String, body: JSONObject = JSONObject()): JSONObject = request("POST", path, body)

    suspend fun action(path: String, body: JSONObject = JSONObject()): ActionOutcome = try {
        ManagementContract.outcome(post(path, body))
    } catch (error: CancellationException) {
        throw error
    } catch (error: Exception) {
        ActionOutcome(false, if (error is CoreRequestException) error.errorCode else "transport_error", ManagementContract.failure(error))
    }

    private suspend fun request(method: String, path: String, body: JSONObject?): JSONObject = withContext(Dispatchers.IO) {
        coroutineContext.ensureActive()
        require(path.startsWith('/') && !path.startsWith("//")) { "Core path 必须是绝对站内路径" }
        val url = endpoint.origin.resolve(path).toURL()
        val connection = open(url)
        try {
            connection.requestMethod = method
            connection.connectTimeout = CONNECT_TIMEOUT_MS
            connection.readTimeout = READ_TIMEOUT_MS
            connection.instanceFollowRedirects = false
            connection.setRequestProperty("Accept", "application/json")
            connection.setRequestProperty("Cache-Control", "no-store")
            if (endpoint.bearerToken.isNotBlank()) {
                connection.setRequestProperty("Authorization", "Bearer ${endpoint.bearerToken}")
            }
            if (body != null) {
                val bytes = body.toString().toByteArray(StandardCharsets.UTF_8)
                require(bytes.size <= MAX_REQUEST_BYTES) { "请求正文超过 ${MAX_REQUEST_BYTES} 字节" }
                connection.doOutput = true
                connection.setFixedLengthStreamingMode(bytes.size)
                connection.setRequestProperty("Content-Type", "application/json; charset=utf-8")
                connection.outputStream.use { it.write(bytes) }
            }
            val status = connection.responseCode
            if (status in 300..399) throw CoreRequestException(status, "REDIRECT_REJECTED", "Core 响应重定向已被拒绝")
            val input = if (status in 200..299) connection.inputStream else connection.errorStream
            val bytes = input?.use { readBounded(it, MAX_RESPONSE_BYTES) } ?: ByteArray(0)
            val text = bytes.toString(StandardCharsets.UTF_8)
            if (status !in 200..299) {
                val parsed = runCatching { JSONObject(text) }.getOrNull()
                val error = parsed?.optJSONObject("error")
                throw CoreRequestException(
                    status,
                    error?.optString("code")?.takeIf { it.isNotBlank() } ?: parsed?.optString("code")?.takeIf { it.isNotBlank() } ?: "HTTP_$status",
                    error?.optString("message")?.takeIf { it.isNotBlank() }
                        ?: parsed?.optString("message")?.takeIf { it.isNotBlank() }
                        ?: (parsed?.opt("error") as? String)?.takeIf { it.isNotBlank() }
                        ?: "Core 请求失败（HTTP $status）"
                )
            }
            coroutineContext.ensureActive()
            if (text.isBlank()) return@withContext JSONObject()
            runCatching { JSONObject(text) }.getOrElse { throw IOException("Core 返回了无效 JSON", it) }
        } finally {
            connection.disconnect()
        }
    }

    suspend fun observeSse(
        path: String,
        initialCursor: Long = 0,
        receive: suspend (SseMessage) -> Unit
    ) = withContext(Dispatchers.IO) {
        require(path.startsWith('/') && !path.startsWith("//"))
        var cursor = initialCursor.coerceAtLeast(0)
        var failures = 0
        while (coroutineContext.isActive) {
            val url = endpoint.origin.resolve(path).toURL()
            val connection = open(url)
            try {
                connection.requestMethod = "GET"
                connection.connectTimeout = CONNECT_TIMEOUT_MS
                connection.readTimeout = SSE_SILENCE_TIMEOUT_MS
                connection.instanceFollowRedirects = false
                connection.setRequestProperty("Accept", "text/event-stream")
                connection.setRequestProperty("Cache-Control", "no-store")
                connection.setRequestProperty("Last-Event-ID", cursor.toString())
                if (endpoint.bearerToken.isNotBlank()) connection.setRequestProperty("Authorization", "Bearer ${endpoint.bearerToken}")
                val status = connection.responseCode
                if (status !in 200..299) throw CoreRequestException(status, "SSE_HTTP_$status", "SSE 连接失败（HTTP $status）")
                val type = connection.contentType.orEmpty().substringBefore(';').trim()
                if (type != "text/event-stream") throw CoreRequestException(status, "SSE_CONTENT_TYPE", "Core 未返回 text/event-stream")
                connection.inputStream.bufferedReader(StandardCharsets.UTF_8).use { reader ->
                    val parser = BoundedSseParser(reader)
                    while (coroutineContext.isActive) {
                        val message = parser.read() ?: throw IOException("Core SSE 已结束")
                        failures = 0
                        if (message.event == "heartbeat") continue
                        if (message.id <= cursor && message.event !in setOf("reset", "gap")) continue
                        receive(message)
                        cursor = if (message.event == "reset") message.id else maxOf(cursor, message.id)
                    }
                }
            } catch (error: CancellationException) {
                throw error
            } catch (error: Exception) {
                coroutineContext.ensureActive()
                failures++
                val terminal = error is CoreRequestException &&
                    (error.errorCode == "SSE_CONTENT_TYPE" || (error.statusCode < 500 && error.statusCode != 429))
                if (terminal || failures >= 8) {
                    receive(SseMessage("stopped", cursor, ManagementContract.failure(error)))
                    return@withContext
                }
                receive(SseMessage("disconnected", cursor, ManagementContract.failure(error)))
                delay((250L * (1L shl failures.coerceAtMost(5))).coerceAtMost(5000L))
            } finally {
                connection.disconnect()
            }
        }
    }

    private fun open(url: URL): HttpURLConnection =
        (url.openConnection(Proxy.NO_PROXY) as HttpURLConnection).apply { useCaches = false }

    companion object {
        const val MAX_RESPONSE_BYTES = 8 * 1024 * 1024
        const val MAX_REQUEST_BYTES = 256 * 1024
        private const val CONNECT_TIMEOUT_MS = 5_000
        private const val READ_TIMEOUT_MS = 12_000
        private const val SSE_SILENCE_TIMEOUT_MS = 45_000

        internal fun readBounded(input: java.io.InputStream, maximum: Int): ByteArray {
            val output = ByteArrayOutputStream(minOf(maximum, 8192))
            val buffer = ByteArray(8192)
            while (true) {
                val count = input.read(buffer)
                if (count < 0) break
                if (output.size() + count > maximum) throw IOException("Core 响应超过大小限制")
                output.write(buffer, 0, count)
            }
            return output.toByteArray()
        }
    }
}

class BoundedSseParser(private val reader: java.io.Reader) {
    fun read(): SseMessage? {
        var event = ""
        var id = ""
        val data = StringBuilder()
        var total = 0
        while (true) {
            val line = readLine() ?: return null
            total += line.length
            if (total > MAX_EVENT_CHARS) throw IOException("SSE 事件超过大小限制")
            if (line.isEmpty()) {
                if (data.isEmpty()) return SseMessage("heartbeat", 0, "")
                val sequence = id.toLongOrNull() ?: throw IOException("SSE 事件序号无效")
                return SseMessage(event.ifBlank { "message" }, sequence, data.toString().removeSuffix("\n"))
            }
            if (line.startsWith(':')) continue
            val colon = line.indexOf(':')
            val key = if (colon < 0) line else line.substring(0, colon)
            var value = if (colon < 0) "" else line.substring(colon + 1)
            if (value.startsWith(' ')) value = value.substring(1)
            when (key) {
                "event" -> event = value
                "id" -> {
                    if ('\u0000' in value) throw IOException("SSE 事件 ID 无效")
                    id = value
                }
                "data" -> data.append(value).append('\n')
            }
        }
    }

    private fun readLine(): String? {
        val value = StringBuilder()
        while (true) {
            val read = reader.read()
            if (read < 0) return if (value.isEmpty()) null else value.toString()
            val char = read.toChar()
            if (char == '\n') return value.toString().removeSuffix("\r")
            value.append(char)
            if (value.length > MAX_LINE_CHARS) throw IOException("SSE 行超过大小限制")
        }
    }

    companion object {
        const val MAX_LINE_CHARS = 64 * 1024
        const val MAX_EVENT_CHARS = 1024 * 1024
    }
}
