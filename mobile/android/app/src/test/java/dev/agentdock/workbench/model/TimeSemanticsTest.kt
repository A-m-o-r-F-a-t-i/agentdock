package dev.agentdock.workbench.model

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TimeSemanticsTest {
    @Test
    fun exactBoundariesRemainStable() {
        assertTrue(TimeSemantics.isRecent(120))
        assertFalse(TimeSemantics.isRecent(121))
        assertTrue(TimeSemantics.canInsertOrStop(180))
        assertFalse(TimeSemantics.canInsertOrStop(181))
        assertTrue(TimeSemantics.insertionStillValid(300))
        assertFalse(TimeSemantics.insertionStillValid(301))
        assertTrue(TimeSemantics.RECEIPT_WAIT_SECONDS == 30L)
    }

    @Test
    fun negativeAgesAreNeverActive() {
        assertFalse(TimeSemantics.isRecent(-1))
        assertFalse(TimeSemantics.canInsertOrStop(-1))
        assertFalse(TimeSemantics.insertionStillValid(-1))
    }
}
