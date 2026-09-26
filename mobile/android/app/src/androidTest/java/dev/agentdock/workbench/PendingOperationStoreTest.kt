package dev.agentdock.workbench

import android.content.Context
import android.content.ContextWrapper
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import dev.agentdock.workbench.model.BridgeOperation
import dev.agentdock.workbench.termux.PendingOperationStore
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File
import java.util.UUID

@RunWith(AndroidJUnit4::class)
class PendingOperationStoreTest {
    private fun withStore(block: (PendingOperationStore) -> Unit) {
        val base = InstrumentationRegistry.getInstrumentation().targetContext
        val directory = File(base.cacheDir, "operations-test-${UUID.randomUUID()}").apply { check(mkdirs()) }
        val context = object : ContextWrapper(base) { override fun getFilesDir(): File = directory }
        try { block(PendingOperationStore(context)) } finally { directory.deleteRecursively() }
    }
    private fun operation(index: Int) = BridgeOperation(
        operationId = "op_$index", requestId = "req_$index", nonce = "abcdefghijklmnopqrstuvwxyzABCDE123456",
        operation = "probe", createdAtEpochMs = System.currentTimeMillis()
    )
    @Test fun lateDispatchCannotReplaceAnAlreadyCompletedCallback() = withStore { store ->
        val request = operation(1); store.create(request)
        store.finish(request, "succeeded", "callback complete", 0, false, false)
        val after = store.finish(request, "running", "late dispatch", null, false, false)
        assertEquals("succeeded", after.phase); assertEquals("callback complete", after.message)
        assertEquals(0, after.exitCode)
    }
    @Test fun unresolvedOperationsAreNotEvictedToMakeRoom() = withStore { store ->
        repeat(128) { store.create(operation(it)) }
        assertThrows(IllegalStateException::class.java) { store.create(operation(129)) }
        assertNotNull(store.get("op_0"))
        val request = checkNotNull(store.get("op_0"))
        store.finish(request, "failed", "terminal", 1, false, false)
        store.create(operation(129))
        assertNull(store.get("op_0")); assertNotNull(store.get("op_129"))
    }
}
