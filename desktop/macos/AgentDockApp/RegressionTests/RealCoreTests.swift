import XCTest
import AppKit
@testable import WorkbenchKit

final class RealCoreTests: XCTestCase {
    private func fixture() throws -> WorkbenchJSON {
        let path = try XCTUnwrap(ProcessInfo.processInfo.environment["WB06_CORE_FIXTURE"], "real Core fixture must be running")
        return try WorkbenchJSON.decode(Data(contentsOf: URL(fileURLWithPath: path)))
    }
    private func client(_ value: WorkbenchJSON, wrongToken: Bool = false) -> WorkbenchAPIClient {
        WorkbenchAPIClient {
            try WorkbenchConnection(baseURL: URL(string: value.text("base_url"))!, bearerToken: wrongToken ? "wrong-fixture-token" : value.text("token"))
        }
    }
    func testAuthenticatedNativeReadsAndPayload() async throws {
        let value = try fixture(), api = client(try fixture())
        var sidebarRequest = WorkbenchSidebarRequest()
        let discovery = try await api.sidebar(sidebarRequest)
        for group in discovery.groups {
            sidebarRequest.modes[group.id] = "history"
            sidebarRequest.limits[group.id] = 20
        }
        let page = try await api.sidebar(sidebarRequest)
        let all = page.groups.flatMap(\.conversations)
        XCTAssertTrue(all.contains { $0.id == value.text("conversation_a") })
        XCTAssertTrue(all.contains { $0.id == value.text("conversation_b") })
        XCTAssertTrue(all.contains { $0.unattributed && $0.id.isEmpty })
        let calls = try await api.calls(conversationID: value.text("conversation_a"))
        XCTAssertTrue(calls.calls.contains { $0.id == value.text("read_call_id") })
        let task = try await api.task(value.text("task_id"))
        XCTAssertEqual(task["task"].firstText("id", "task_id"), value.text("task_id"))
        let payload = try await api.callPayload(value.text("read_call_id"), source: "response", limit: 10000)
        XCTAssertTrue(payload.text("text").contains("fixture"))
        XCTAssertGreaterThan(payload.integer("next_offset"), 0)
        do { _ = try await client(value, wrongToken: true).overview(); XCTFail("expected authentication failure") }
        catch let error as WorkbenchClientError {
            if case let .http(status, _, _) = error { XCTAssertEqual(status, 401) }
            else { XCTFail("unexpected error: \(error)") }
        }
    }
    func testNativeMutationApprovalAndInsertionIdempotency() async throws {
        let value = try fixture(), api = client(try fixture())
        let conversationID = value.text("conversation_a")
        _ = try await api.manage(kind: "conversation", ids: [conversationID], action: "rename", title: "WB06 原生 Core 集成验证")
        let detail = try await api.conversation(conversationID)
        XCTAssertEqual(detail["conversation"].text("title"), "WB06 原生 Core 集成验证")
        let decision = try await api.decideApproval(value.text("approval_id"), action: "approve")
        XCTAssertTrue(decision.flag("dispatched") || decision.flag("already_decided"))
        var terminal = false
        for _ in 0..<100 {
            let call = try await api.call(value.text("approval_call_id"))
            if call.status == "succeeded" { terminal = true; break }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        XCTAssertTrue(terminal)
        let submission = "native-fixture-" + UUID().uuidString
        let first = try await api.enqueueInsertion(conversationID: conversationID, submissionID: submission, text: "原生客户端补充")
        let second = try await api.enqueueInsertion(conversationID: conversationID, submissionID: submission, text: "原生客户端补充")
        let insertionID = first["insertion"].firstText("insertion_id", "id")
        XCTAssertFalse(insertionID.isEmpty)
        XCTAssertEqual(second["insertion"].firstText("insertion_id", "id"), insertionID)
        _ = try await api.insertionAction(conversationID: conversationID, insertionID: insertionID, action: "cancel")
        let insertions = try await api.insertions(conversationID: conversationID)
        XCTAssertEqual(insertions.items.filter { $0.id == insertionID }.count, 1)
        XCTAssertTrue(insertions.items.first { $0.id == insertionID }?.terminal == true)
        let permission = try await api.permission(conversationID: conversationID)
        do {
            _ = try await api.updatePermission(.object(["scope": .string("global"), "mode": .string("rules"),
                "expected_revision": .integer(Int64(permission.revision) + 100)]))
            XCTFail("stale permission revision must fail")
        } catch let error as WorkbenchClientError {
            if case let .http(status, _, _) = error { XCTAssertEqual(status, 409) }
            else { XCTFail("unexpected error: \(error)") }
        }
    }
    @MainActor func testNativeApplicationDisplaysRealCore() async throws {
        _ = NSApplication.shared
        let value = try fixture()
        let controller = WorkbenchWindowController(client: client(value))
        controller.present()
        let window = try XCTUnwrap(controller.window)
        func descendants(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(descendants) }
        let outline = try XCTUnwrap(descendants(window.contentView!).first { $0.accessibilityIdentifier() == "workbench.sidebar" } as? NSOutlineView)
        for _ in 0..<150 where outline.numberOfRows < 2 { try await Task.sleep(nanoseconds: 50_000_000) }
        XCTAssertGreaterThan(outline.numberOfRows, 1)
        let directory = try XCTUnwrap(ProcessInfo.processInfo.environment["WB06_EVIDENCE_DIR"])
        try controller.capturePNG(to: URL(fileURLWithPath: directory).appendingPathComponent("native-real-core.png"))
        controller.close()
        _ = try await client(value).overview()
        // Closing a native UI never stops the separately owned Core node.
    }
}
