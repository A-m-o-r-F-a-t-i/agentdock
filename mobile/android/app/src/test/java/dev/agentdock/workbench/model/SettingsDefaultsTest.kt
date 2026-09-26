package dev.agentdock.workbench.model

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SettingsDefaultsTest {
    @Test
    fun customEditorIsClosedAndDesiredStateIsStopped() {
        val settings = WorkbenchSettings()
        assertFalse(settings.customPermissionEnabled)
        assertEquals("stopped", settings.desiredNodeState)
        assertEquals("http://127.0.0.1:8765", settings.endpoint)
        assertFalse(settings.remoteEndpointEnabled)
        assertFalse(settings.guardianEnabled)
        assertTrue(settings.toolOutputMaxChars in 1_000..100_000)
    }
}
