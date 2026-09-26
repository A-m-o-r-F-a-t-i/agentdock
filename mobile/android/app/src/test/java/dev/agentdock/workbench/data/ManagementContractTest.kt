package dev.agentdock.workbench.data

import kotlinx.coroutines.CancellationException
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class ManagementContractTest {
    @Test fun taskQueryUsesManagedRouteAndEncodesFilters() {
        val path = ManagementContract.listPath("tasks", ListQuery(search = "串口 & CAN", workspaceId = "wsp_1", tag = "USB/C", offset = 100))
        assertTrue(path.startsWith("/internal/runtime/execution/tasks?"))
        assertTrue(path.contains("%26") && path.contains("USB%2FC") && path.contains("offset=100"))
        assertFalse(path.contains("activity/tasks") || path.contains("include_output=true"))
    }

    @Test fun validatesAllBatchActionsAndFreezesExplicitIds() {
        for (action in ManagementContract.actions) {
            val body = ManagementContract.batchBody(listOf("tsk_a"), action, "Name", listOf("tag"), true)
            assertEquals(action, body.getString("action"))
            assertEquals("tsk_a", body.getJSONArray("ids").getString(0))
        }
        assertThrows(IllegalArgumentException::class.java) { ManagementContract.batchBody(listOf("tsk_a"), "delete") }
        assertThrows(IllegalArgumentException::class.java) { ManagementContract.batchBody(listOf("tsk_a", "tsk_a"), "pin") }
        assertThrows(IllegalArgumentException::class.java) { ManagementContract.batchBody((0..200).map { "tsk_$it" }, "pin") }
        assertThrows(IllegalArgumentException::class.java) { ManagementContract.batchBody(listOf("../task"), "pin") }
        assertThrows(IllegalArgumentException::class.java) { ManagementContract.batchBody(listOf("tsk_a", "tsk_b"), "rename", "Title") }
    }

    @Test fun emptyPageAndMissingArrayAreDifferent() {
        val page = ManagementContract.parsePage(JSONObject("""{"tasks":[],"total":0,"has_more":false}"""), "tasks")
        assertEquals(0, page.total)
        assertTrue(page.items.isEmpty())
        assertThrows(IllegalStateException::class.java) { ManagementContract.parsePage(JSONObject(), "tasks") }
    }

    @Test fun preservesUnknownTotalsAndChecksContinuation() {
        val value = JSONObject("""{"tasks":[{"id":"tsk_1","title":"One","step_count":3,"completed_steps":1}],"has_more":true,"next_offset":100}""")
        val page = ManagementContract.parsePage(value, "tasks")
        assertNull(page.total)
        assertEquals(100, page.nextOffset)
        assertTrue(page.items.first().metadata.startsWith("1/3"))
        value.remove("next_offset")
        assertThrows(IllegalArgumentException::class.java) { ManagementContract.parsePage(value, "tasks") }
    }

    @Test fun rejectsDuplicateOrUnboundedRows() {
        val row = JSONObject().put("id", "tsk_1")
        assertThrows(IllegalArgumentException::class.java) {
            ManagementContract.parsePage(JSONObject().put("tasks", JSONArray().put(row).put(row)), "tasks")
        }
        val rows = JSONArray()
        repeat(201) { rows.put(JSONObject().put("id", "tsk_$it")) }
        assertThrows(IllegalArgumentException::class.java) { ManagementContract.parsePage(JSONObject().put("tasks", rows), "tasks") }
    }

    @Test fun batchPartialIsNeverReportedAsAllSucceeded() {
        val result = JSONObject("""{"items":[{"id":"tsk_b","status":"skipped"},{"id":"tsk_a","status":"succeeded"}],"status":"partial"}""")
        val outcome = ManagementContract.batchOutcome(result, listOf("tsk_a", "tsk_b"))
        assertFalse(outcome.accepted)
        assertEquals("partial", outcome.status)
        assertTrue(outcome.message.contains("跳过 1"))
        assertThrows(IllegalArgumentException::class.java) { ManagementContract.batchOutcome(result, listOf("tsk_a", "tsk_c")) }
    }

    @Test fun falseEnvelopeAndCancellationRemainFailures() {
        assertFalse(ManagementContract.outcome(JSONObject("""{"ok":false,"message":"denied"}""")).accepted)
        assertFalse(ManagementContract.outcome(JSONObject("""{"status":"partial"}""")).accepted)
        assertThrows(CancellationException::class.java) { ManagementContract.failure(CancellationException("stop")) }
    }

    @Test fun tenThousandRecordsStayPageBounded() {
        var loaded = 0
        repeat(100) { pageIndex ->
            val rows = JSONArray()
            repeat(100) { rows.put(JSONObject().put("id", "tsk_${pageIndex * 100 + it}")) }
            val page = ManagementContract.parsePage(JSONObject().put("tasks", rows).put("total", 10000)
                .put("has_more", pageIndex < 99).put("next_offset", (pageIndex + 1) * 100), "tasks")
            assertEquals(100, page.items.size)
            loaded += page.items.size
        }
        assertEquals(10000, loaded)
    }
}
