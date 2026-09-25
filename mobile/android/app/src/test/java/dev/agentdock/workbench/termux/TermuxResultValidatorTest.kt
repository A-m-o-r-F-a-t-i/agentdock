package dev.agentdock.workbench.termux

import dev.agentdock.workbench.model.BridgeOperation
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test

class TermuxResultValidatorTest {
    private val expected = BridgeOperation(
        operationId = "op_123",
        requestId = "req_123",
        nonce = "abcdefghijklmnopqrstuvwxyzABCDE123456",
        operation = "probe",
        createdAtEpochMs = 1
    )

    @Test
    fun acceptsBoundSuccessfulResult() {
        val value = JSONObject()
            .put("schema_version", 1)
            .put("operation_id", expected.operationId)
            .put("request_id", expected.requestId)
            .put("nonce", expected.nonce)
            .put("operation", expected.operation)
            .put("status", "healthy")
            .put("message", "ok")
        assertEquals(
            "succeeded",
            TermuxResultValidator.validate(expected, value.toString(), "", 0, 0, "").phase
        )
    }

    @Test
    fun rejectsNonceMismatchEvenWithExitZero() {
        val value = JSONObject()
            .put("schema_version", 1)
            .put("operation_id", expected.operationId)
            .put("request_id", expected.requestId)
            .put("nonce", "wrong-nonce-value-123456789")
            .put("operation", expected.operation)
            .put("status", "healthy")
        assertEquals(
            "failed",
            TermuxResultValidator.validate(expected, value.toString(), "", 0, 0, "").phase
        )
    }

    @Test
    fun pendingManifestIsNotSuccess() {
        val value = JSONObject()
            .put("schema_version", 1)
            .put("operation_id", expected.operationId)
            .put("request_id", expected.requestId)
            .put("nonce", expected.nonce)
            .put("operation", expected.operation)
            .put("status", "pending_manifest")
        assertEquals(
            "pending_manifest",
            TermuxResultValidator.validate(expected, value.toString(), "", 0, 0, "").phase
        )
    }
}
