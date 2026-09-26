package dev.agentdock.workbench.termux

import dev.agentdock.workbench.model.BridgeOperation
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class TermuxResultValidatorTest {
    private val now = 1_800_000_000_000L
    private val expected = BridgeOperation(
        operationId = "op_123", requestId = "req_123", nonce = "abcdefghijklmnopqrstuvwxyzABCDE123456",
        operation = "probe", createdAtEpochMs = now - 1000
    )
    private fun response(status: String = "healthy") = JSONObject()
        .put("schema_version", 1).put("operation_id", expected.operationId)
        .put("request_id", expected.requestId).put("nonce", expected.nonce)
        .put("operation", expected.operation).put("status", status).put("message", "ok")
    private fun validate(value: JSONObject, exit: Int? = 0, operation: BridgeOperation = expected) =
        TermuxResultValidator.validate(operation, value.toString(), "", exit, 0, "", now)

    @Test fun acceptsBoundSuccessfulResult() { assertEquals("succeeded", validate(response()).phase) }
    @Test fun rejectsNonceMismatchEvenWithExitZero() {
        assertEquals("failed", validate(response().put("nonce", "wrong")).phase)
    }
    @Test fun pendingManifestIsNotSuccess() {
        assertEquals("pending_manifest", validate(response("pending_manifest")).phase)
    }
    @Test fun rejectsCredentialsEvenInsideNestedArrays() {
        val value = response().put("extra", JSONArray().put(JSONObject().put("core_token", "fixture-only")))
        assertEquals("failed", validate(value).phase)
        assertFalse(validate(value).message.contains("fixture-only"))
    }
    @Test fun rejectsNonzeroAndMissingExitCode() {
        assertEquals("failed", validate(response("pending_manifest"), 1).phase)
        assertEquals("failed", validate(response(), null).phase)
    }
    @Test fun callbackAgeAndTerminalStateAreChecked() {
        assertEquals("failed", validate(response(), operation = expected.copy(createdAtEpochMs = now + 1)).phase)
        assertEquals("failed", validate(response(), operation = expected.copy(createdAtEpochMs = now - TermuxResultPolicy.MAX_CALLBACK_AGE_MS - 1)).phase)
        assertEquals("succeeded", validate(response(), operation = expected.copy(createdAtEpochMs = now - TermuxResultPolicy.MAX_CALLBACK_AGE_MS)).phase)
        for (phase in TermuxResultPolicy.terminalPhases) {
            assertEquals("failed", validate(response(), operation = expected.copy(phase = phase)).phase)
        }
    }
    @Test fun neverCopiesUnstructuredStderrOrPluginErrors() {
        val secret = "fixture-password-do-not-save"
        val malformed = TermuxResultValidator.validate(expected, "not-json", secret, 1, 0, secret, now)
        val plugin = TermuxResultValidator.validate(expected, "", secret, 1, 1, secret, now)
        assertFalse(malformed.message.contains(secret)); assertFalse(plugin.message.contains(secret))
    }
    @Test fun messageRedactionRetainsUsefulContext() {
        val message = "startup failed; Bearer abc.def.test token=private-test " + "a".repeat(64)
        val result = validate(response().put("message", message))
        assertTrue(result.message.contains("startup failed"))
        assertFalse(result.message.contains("abc.def.test"))
        assertFalse(result.message.contains("private-test"))
        assertFalse(result.message.contains("a".repeat(64)))
    }
    @Test fun enforcesUtf8SizeNotOnlyUtf16CharacterCount() {
        assertEquals("failed", validate(response().put("message", "中".repeat(24000))).phase)
    }
    @Test fun requestNonceIsNotASecretField() {
        assertFalse(TermuxResultPolicy.containsSecretFields(response()))
    }
}
