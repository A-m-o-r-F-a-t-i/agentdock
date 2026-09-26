package dev.agentdock.workbench.data

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test
import java.net.URI

class CoreCredentialBindingTest {
    private val origin = URI("https://core.example")
    private val bearer = "fixture-bearer-not-a-production-secret"

    @Test fun sameOriginReceivesItsCredential() {
        assertEquals(bearer, CoreCredentialBinding.decode(CoreCredentialBinding.encode(origin, bearer), origin))
    }
    @Test fun normalizesHostCaseAndDefaultPortOnly() {
        val stored = CoreCredentialBinding.encode(URI("https://CORE.example:443/"), bearer)
        assertEquals(bearer, CoreCredentialBinding.decode(stored, origin))
    }
    @Test fun schemeHostAndPortChangesNeverReuseTheCredential() {
        val stored = CoreCredentialBinding.encode(origin, bearer)
        for (url in listOf("http://core.example", "https://different.example", "https://core.example:8443")) {
            assertEquals("", CoreCredentialBinding.decode(stored, URI(url)))
        }
        val local = CoreCredentialBinding.encode(URI("http://localhost:8765"), bearer)
        assertEquals("", CoreCredentialBinding.decode(local, URI("http://127.0.0.1:8765")))
    }
    @Test fun legacyCorruptAndWronglyTypedRecordsAreNotSent() {
        val stored = CoreCredentialBinding.encode(origin, bearer)
        for (value in listOf(bearer, "{", "x".repeat(20000),
            JSONObject(stored).put("schema_version", 2).toString(),
            JSONObject(stored).put("schema_version", "1").toString(),
            JSONObject(stored).put("bearer", true).toString())) {
            assertEquals("", CoreCredentialBinding.decode(value, origin))
        }
    }
    @Test fun rejectsControlCharactersWhitespaceAndUnboundedValues() {
        for (value in listOf("", "one two", "bad\nheader", "bad\rheader", "x".repeat(8193))) {
            assertThrows(IllegalArgumentException::class.java) { CoreCredentialBinding.encode(origin, value) }
        }
    }
    @Test fun refusesOriginsContainingCredentialsOrRequestPaths() {
        for (url in listOf("https://user@core.example", "https://core.example/path", "https://core.example?token=x", "ftp://core.example")) {
            assertThrows(IllegalArgumentException::class.java) { CoreCredentialBinding.encode(URI(url), bearer) }
        }
    }
}
