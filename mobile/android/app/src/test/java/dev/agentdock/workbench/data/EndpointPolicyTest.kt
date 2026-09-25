package dev.agentdock.workbench.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class EndpointPolicyTest {
    @Test
    fun loopbackHttpIsAllowed() {
        assertEquals("http", EndpointPolicy.resolve("http://127.0.0.1:8765", false).scheme)
        assertEquals("localhost", EndpointPolicy.resolve("http://localhost:8765", false).host)
    }

    @Test
    fun remoteRequiresExplicitHttps() {
        assertThrows(IllegalArgumentException::class.java) {
            EndpointPolicy.resolve("https://example.com", false)
        }
        assertThrows(IllegalArgumentException::class.java) {
            EndpointPolicy.resolve("http://example.com", true)
        }
        assertEquals("https", EndpointPolicy.resolve("https://example.com", true).scheme)
    }

    @Test
    fun pathsQueriesAndUserInfoAreRejected() {
        listOf(
            "https://example.com/path",
            "https://example.com?a=b",
            "https://user@example.com"
        ).forEach { value ->
            assertThrows(IllegalArgumentException::class.java) {
                EndpointPolicy.resolve(value, true)
            }
        }
    }
}
