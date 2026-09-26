import XCTest
import Foundation
@testable import WorkbenchKit

final class RuntimeProtocol: URLProtocol {
    static let lock = NSLock()
    static var handler: ((URLRequest) throws -> (Int, String, Data, Double))?
    static var requests = [URLRequest]()
    private var work: DispatchWorkItem?
    static func configure(_ block: @escaping (URLRequest) throws -> (Int, String, Data, Double)) {
        lock.lock(); defer { lock.unlock() }; handler = block; requests = []
    }
    static func captured() -> [URLRequest] { lock.lock(); defer { lock.unlock() }; return requests }
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lock.lock(); Self.requests.append(request); let handler = Self.handler; Self.lock.unlock()
        do {
            guard let handler else { throw URLError(.cannotConnectToHost) }
            let (code, mime, body, delay) = try handler(request)
            let operation = DispatchWorkItem { [weak self] in
                guard let self else { return }
                let response = HTTPURLResponse(url: self.request.url!, statusCode: code, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": mime])!
                self.client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
                self.client?.urlProtocol(self, didLoad: body)
                self.client?.urlProtocolDidFinishLoading(self)
            }
            work = operation
            DispatchQueue.global().asyncAfter(deadline: .now() + delay, execute: operation)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() { work?.cancel(); work = nil }
}

final class TransportTests: XCTestCase {
    private func client(limit: Int = 8 * 1024 * 1024) -> WorkbenchAPIClient {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [RuntimeProtocol.self]
        let session = URLSession(configuration: configuration)
        return WorkbenchAPIClient(session: session, maximumResponseBytes: limit) {
            try WorkbenchConnection(baseURL: URL(string: "http://127.0.0.1:18765")!, bearerToken: "isolated-test-token")
        }
    }
    private func answer(_ value: WorkbenchJSON, status: Int = 200, delay: Double = 0) throws -> (Int, String, Data, Double) {
        (status, "application/json", try value.encodedData(), delay)
    }
    func testLoopbackBoundaryAndIDValidation() throws {
        for value in ["https://127.0.0.1:18765", "http://example.com", "http://user@localhost", "http://localhost/?secret=x"] {
            XCTAssertThrowsError(try WorkbenchConnection(baseURL: URL(string: value)!, bearerToken: "fixture"))
        }
        let api = client()
        for id in ["", ".", "..", "a/b", "a%2fb", "a\\b"] { XCTAssertThrowsError(try api.encodedPathComponent(id)) }
    }
    func testAuthenticatedQueryAndLazyPayload() async throws {
        RuntimeProtocol.configure { request in
            (200, "application/json", Data(#"{"calls":[],"latest_seq":8}"#.utf8), 0)
        }
        let api = client()
        _ = try await api.calls(conversationID: "conv_1")
        let request = try XCTUnwrap(RuntimeProtocol.captured().first)
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer isolated-test-token")
        XCTAssertFalse(request.url!.absoluteString.contains("isolated-test-token"))
        XCTAssertFalse(request.url!.absoluteString.contains("include_output=true"))
        XCTAssertTrue(request.url!.absoluteString.contains("top_level=true"))
        XCTAssertFalse(RuntimeProtocol.captured().contains { $0.url!.path.contains("payload") })
    }
    func testOversizedAndInvalidResponsesDoNotBecomeEmptyLists() async throws {
        RuntimeProtocol.configure { _ in (200, "application/json", Data(repeating: 32, count: 2048), 0) }
        do { _ = try await client(limit: 1024).overview(); XCTFail("expected bound") }
        catch let error as WorkbenchClientError { XCTAssertEqual(error, .responseTooLarge(limit: 1024)) }
        RuntimeProtocol.configure { _ in (200, "text/html", Data("error page".utf8), 0) }
        do { _ = try await client().overview(); XCTFail("expected MIME rejection") }
        catch { XCTAssertTrue(error.localizedDescription.contains("非 JSON")) }
    }
    func test403AndConflictAreNotRetried() async throws {
        for status in [403, 409] {
            RuntimeProtocol.configure { _ in (status, "application/json", Data(#"{"ok":false,"code":"POLICY_CONFLICT","error":"revision changed"}"#.utf8), 0) }
            do { _ = try await client().updatePermission(.object(["expected_revision": .integer(1)])); XCTFail("expected failure") }
            catch { XCTAssertTrue(error.localizedDescription.contains("revision changed")) }
            XCTAssertEqual(RuntimeProtocol.captured().count, 1)
        }
    }
    func testSSECursorAndBoundedStreamParsing() async throws {
        RuntimeProtocol.configure { _ in (200, "text/event-stream", Data("id: 9\nevent: call\ndata: {\"call_id\":\"call_9\"}\n\n".utf8), 0) }
        let api = client()
        var events = [WorkbenchStreamEvent]()
        for try await event in api.eventStream(path: "/internal/runtime/calls/stream", lastEventID: "8") { events.append(event) }
        XCTAssertEqual(events.map(\.id), ["9"])
        XCTAssertEqual(RuntimeProtocol.captured().first?.value(forHTTPHeaderField: "Last-Event-ID"), "8")
    }
    @MainActor func testOfflineSnapshotAndSelectionGeneration() async throws {
        let now = WorkbenchFormatting.iso(Date())
        let rows: [WorkbenchJSON] = ["conv_a", "conv_b"].map { .object(["conversation_id": .string($0), "title": .string($0),
            "statistics": .object(["last_tool_call_at": .string(now)])]) }
        RuntimeProtocol.configure { request in
            let path = request.url!.path
            if path.hasSuffix("sidebar") {
                return try self.answer(.object(["server_now": .string(now), "groups": .array([
                    .object(["workspace_id": .string("wsp_test"), "conversations": .array(rows)])])]))
            }
            if path.hasSuffix("/execution") { return try self.answer(.object(["server_now": .string(now)])) }
            if path.contains("/conversations/conv_a") && !path.hasSuffix("insertions") {
                return try self.answer(.object(["conversation": rows[0]]), delay: 0.35)
            }
            if path.contains("/conversations/conv_b") && !path.hasSuffix("insertions") { return try self.answer(.object(["conversation": rows[1]])) }
            if path.hasSuffix("/calls") { return try self.answer(.object(["calls": .array([])])) }
            if path.hasSuffix("insertions") { return try self.answer(.object(["insertions": .array([])])) }
            if path.hasSuffix("effective") { return try self.answer(.object(["effective": .object(["revision": .integer(1), "mode": .string("rules")])])) }
            return try self.answer(.object(["ok": .bool(false)]), status: 404)
        }
        let model = WorkbenchViewModel(client: client())
        model.start()
        for _ in 0..<100 where model.snapshot.sidebar.groups.isEmpty { try await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertEqual(model.snapshot.sidebar.groups.count, 1)
        model.selectConversation("conv_b")
        try await Task.sleep(nanoseconds: 500_000_000)
        XCTAssertEqual(model.selectedConversationID, "conv_b")
        XCTAssertEqual(model.snapshot.selectedConversation?.title, "conv_b")
        XCTAssertFalse(RuntimeProtocol.captured().contains { $0.url!.path.contains("payload") })
        RuntimeProtocol.configure { _ in throw URLError(.cannotConnectToHost) }
        model.refresh()
        try await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertTrue(model.snapshot.stale)
        XCTAssertEqual(model.snapshot.sidebar.groups.count, 1)
        model.stop()
    }
}
