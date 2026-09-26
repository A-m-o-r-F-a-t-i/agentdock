package dev.agentdock.workbench

import android.content.Context
import android.content.ContextWrapper
import android.content.SharedPreferences
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import dev.agentdock.workbench.data.CredentialStore
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import java.net.URI
import java.util.UUID

@RunWith(AndroidJUnit4::class)
class CoreCredentialStoreTest {
    private fun withStore(block: (CredentialStore, SharedPreferences) -> Unit) {
        val base = InstrumentationRegistry.getInstrumentation().targetContext
        val name = "credential-test-${UUID.randomUUID()}"
        val preferences = base.getSharedPreferences(name, Context.MODE_PRIVATE)
        val context = object : ContextWrapper(base) {
            override fun getSharedPreferences(ignored: String, mode: Int): SharedPreferences = preferences
        }
        try { block(CredentialStore(context), preferences) }
        finally { check(preferences.edit().clear().commit()) }
    }
    @Test fun keystoreEnvelopeBindsTheCredentialToItsOrigin() = withStore { store, preferences ->
        val original = URI("https://first.example")
        val other = URI("https://second.example")
        val bearer = "fixture-keystore-bearer-not-production"
        store.putCore(original, bearer)
        assertEquals(bearer, store.getCore(original))
        assertEquals("", store.getCore(other))
        assertTrue(preferences.all.isNotEmpty())
        assertFalse(preferences.all.values.any { it.toString().contains(bearer) || it.toString().contains("first.example") })
        store.putCore(other, "fixture-replacement")
        assertEquals("", store.getCore(original))
        assertEquals("fixture-replacement", store.getCore(other))
    }
    @Test fun preservesButDoesNotSendAnUnboundLegacyCredential() = withStore { store, _ ->
        store.put("core_bearer", "fixture-unbound-legacy")
        assertEquals("", store.getCore(URI("http://127.0.0.1:8765")))
        assertEquals("fixture-unbound-legacy", store.get("core_bearer"))
        store.clear("core_bearer")
        assertEquals("", store.getCore(URI("http://127.0.0.1:8765")))
    }
}
