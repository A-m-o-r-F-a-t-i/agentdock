package dev.agentdock.workbench.data

import com.sun.net.httpserver.HttpServer
import java.net.InetSocketAddress
import java.net.URI
import kotlinx.coroutines.runBlocking
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** Exercises real loopback HTTP; response fixtures are not a substitute for a live Core test. */
class CoreClientHttpTest {
    private fun withServer(status: Int, body: String, test: (CoreClient) -> Unit) {
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/") { exchange ->
            exchange.requestBody.use { it.readBytes() }
            val bytes = body.toByteArray(Charsets.UTF_8)
            exchange.responseHeaders.set("Content-Type", "application/json")
            exchange.sendResponseHeaders(status, bytes.size.toLong())
            exchange.responseBody.use { it.write(bytes) }
        }
        server.start()
        try { test(CoreClient(CoreEndpoint(URI("http://127.0.0.1:${server.address.port}"), "fixture-token"))) }
        finally { server.stop(0) }
    }

    @Test fun preservesStructuredForbiddenReason() = withServer(403, """{"ok":false,"code":"LOCAL_ONLY","error":"local management only"}""") { client ->
        runBlocking {
            val result = client.action("/internal/runtime/tasks/batch", JSONObject())
            assertFalse(result.accepted)
            assertEquals("LOCAL_ONLY", result.status)
            assertTrue(result.message.contains("local management only"))
        }
    }

    @Test fun doesNotTreatHttp200AsBusinessSuccess() = withServer(200, """{"ok":false,"status":"denied","message":"denied by Core"}""") { client ->
        runBlocking { assertFalse(client.action("/internal/runtime/permissions").accepted) }
    }

    @Test fun rejectsMalformedSuccessBody() = withServer(200, "not JSON") { client ->
        runBlocking { assertFalse(client.action("/internal/runtime/tasks/batch").accepted) }
    }

    @Test fun returnsExactPagedJson() = withServer(200, """{"tasks":[],"total":0,"has_more":false}""") { client ->
        runBlocking {
            val value = client.get(ManagementContract.listPath("tasks", ListQuery()))
            assertEquals(0, ManagementContract.parsePage(value, "tasks").total)
        }
    }
}
