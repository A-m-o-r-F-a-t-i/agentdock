package dev.agentdock.workbench.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import java.io.IOException
import java.io.StringReader

class BoundedSseParserTest {
    @Test
    fun parsesStableEventFields() {
        val parser = BoundedSseParser(StringReader("id: 42\nevent: call\ndata: first\ndata: second\n\n"))
        assertEquals(SseMessage("call", 42, "first\nsecond"), parser.read())
    }

    @Test
    fun commentsBecomeHeartbeatOnlyWhenNoDataExists() {
        val parser = BoundedSseParser(StringReader(": keepalive\n\n"))
        assertEquals("heartbeat", parser.read()?.event)
    }

    @Test
    fun rejectsOversizedLine() {
        val parser = BoundedSseParser(
            StringReader("data: " + "x".repeat(BoundedSseParser.MAX_LINE_CHARS + 1) + "\n\n")
        )
        assertThrows(IOException::class.java) { parser.read() }
    }
}
