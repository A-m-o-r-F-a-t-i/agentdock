import Foundation

@main
struct WorkbenchSSETests {
    static func main() throws {
        var parser = WorkbenchSSEParser(maximumLineBytes: 1024, maximumEventBytes: 2048)
        let stream = """
        retry: 1000

        : heartbeat
        id: 7
        event: call
        data: {"call_id":"call_7",
        data: "status":"running"}

        event: cursor
        data: {"seq":8}

        """
        var events = [WorkbenchStreamEvent]()
        for byte in Data(stream.utf8) {
            events.append(contentsOf: try parser.feed(byte))
        }
        events.append(contentsOf: try parser.finish())
        precondition(events.count == 2)
        precondition(events[0].id == "7")
        precondition(events[0].name == "call")
        precondition(events[0].data.text("call_id") == "call_7")
        precondition(events[0].data.text("status") == "running")
        precondition(events[1].id == "7")
        precondition(events[1].name == "cursor")
        precondition(events[1].data.unsigned("seq") == 8)

        var lineLimited = WorkbenchSSEParser(maximumLineBytes: 3, maximumEventBytes: 64)
        do {
            for byte in Data("data".utf8) { _ = try lineLimited.feed(byte) }
            preconditionFailure("expected line limit")
        } catch let error as WorkbenchClientError {
            precondition(error == .streamLineTooLarge(limit: 3))
        }

        var eventLimited = WorkbenchSSEParser(maximumLineBytes: 64, maximumEventBytes: 4)
        do {
            for byte in Data("data: 12345\n\n".utf8) { _ = try eventLimited.feed(byte) }
            preconditionFailure("expected event limit")
        } catch let error as WorkbenchClientError {
            precondition(error == .streamEventTooLarge(limit: 4))
        }

        var invalidJSON = WorkbenchSSEParser(maximumLineBytes: 64, maximumEventBytes: 64)
        do {
            for byte in Data("data: not-json\n\n".utf8) { _ = try invalidJSON.feed(byte) }
            preconditionFailure("expected invalid JSON")
        } catch let error as WorkbenchClientError {
            if case .invalidJSON = error {
                // expected
            } else {
                preconditionFailure("unexpected error: \(error)")
            }
        }

        precondition(WorkbenchClientError.transport("offline").retryable)
        precondition(WorkbenchClientError.http(status: 503, code: "ROUTE_UNAVAILABLE", message: "missing").capabilityUnavailable)
        precondition(!WorkbenchClientError.http(status: 403, code: "DENIED", message: "no").retryable)

        print("workbench SSE tests passed")
    }
}
