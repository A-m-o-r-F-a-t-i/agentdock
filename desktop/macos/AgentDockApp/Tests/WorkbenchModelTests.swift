import Foundation

@main
struct WorkbenchModelTests {
    static func main() throws {
        let server = date("2026-09-25T15:00:00Z")

        precondition(WorkbenchConversation.isRecentlyActive(
            lastActivityAt: server.addingTimeInterval(-119.999),
            serverNow: server,
            inFlight: false
        ))
        precondition(!WorkbenchConversation.isRecentlyActive(
            lastActivityAt: server.addingTimeInterval(-120),
            serverNow: server,
            inFlight: false
        ))
        precondition(!WorkbenchConversation.isRecentlyActive(
            lastActivityAt: server.addingTimeInterval(1),
            serverNow: server,
            inFlight: false
        ))
        precondition(!WorkbenchConversation.isRecentlyActive(
            lastActivityAt: nil,
            serverNow: server,
            inFlight: true
        ))

        let eligible = conversation(lastToolCall: server.addingTimeInterval(-179.999), server: server)
        precondition(eligible.insertionEligible == true)
        let expired = conversation(lastToolCall: server.addingTimeInterval(-180), server: server)
        precondition(expired.insertionEligible == false)
        let explicitFalse = WorkbenchConversation(json: .object([
            "conversation_id": .string("conv_explicit"),
            "title": .string("资格字段优先"),
            "insertion_eligible": .bool(false),
            "statistics": .object([
                "last_tool_call_at": .string(WorkbenchFormatting.iso(server.addingTimeInterval(-10)))
            ])
        ]), serverNow: server)
        precondition(explicitFalse.insertionEligible == false)

        let fallbackTitle = WorkbenchConversation(json: .object([
            "conversation_id": .string("conv_fallback"),
            "title": .string("新对话"),
            "created_at": .string("2026-09-25T14:30:00Z")
        ]), serverNow: server)
        precondition(fallbackTitle.title != "新对话")
        precondition(fallbackTitle.title.contains("09-25"))

        let unknownData = Data(#"{"schema_version":99,"future":{"nested":[1,true,"x"]},"large":9223372036854775807}"#.utf8)
        let unknown = try WorkbenchJSON.decode(unknownData)
        precondition(unknown["schema_version"].int64Value == 99)
        precondition(unknown["future"]["nested"][1].boolValue == true)
        precondition(unknown["large"].int64Value == Int64.max)
        let roundTrip = try WorkbenchJSON.decode(unknown.encodedData())
        precondition(roundTrip == unknown)

        let insertionCall = WorkbenchCall(json: .object([
            "insertion_id": .string("ins_1"),
            "kind": .string("insertion.queued"),
            "status": .string("queued"),
            "text": .string("继续执行")
        ]))
        precondition(insertionCall.isInsertion)
        precondition(insertionCall.title == "用户补充")
        precondition(!insertionCall.canStop)

        let runningCall = WorkbenchCall(json: .object([
            "call_id": .string("call_1"),
            "conversation_id": .string("conv_1"),
            "tool_name": .string("files.read"),
            "status": .string("running"),
            "request": .object(["text": .string("读取")]),
            "response": .object(["text": .string("处理中")]),
            "updated_seq": .integer(7)
        ]))
        precondition(runningCall.canStop)
        precondition(runningCall.sequence == 7)
        precondition(runningCall.requestText == "读取")
        precondition(runningCall.responseText == "处理中")

        let permission = WorkbenchPermissionState(json: .object([
            "effective": .object([
                "mode": .string("full"),
                "scope": .string("workspace"),
                "scope_id": .string("wsp_1"),
                "revision": .integer(12),
                "custom_settings_enabled": .bool(true),
                "settings": .object(["network": .string("allow")])
            ])
        ]))
        precondition(permission.mode == "full")
        precondition(permission.revision == 12)
        precondition(permission.customSettingsEnabled == true)

        let snapshot = WorkbenchSnapshot.fixture()
        precondition(snapshot.sidebar.groups.count == 1)
        precondition(snapshot.selectedConversation?.id == "conv_active")
        precondition(snapshot.selectedConversation?.bindingRevision == 3)
        precondition(snapshot.calls.calls.count == 3)
        precondition(snapshot.insertions.items.count == 1)
        precondition(snapshot.permission?.mode == "full")

        print("workbench model tests passed")
    }

    private static func conversation(lastToolCall: Date, server: Date) -> WorkbenchConversation {
        WorkbenchConversation(json: .object([
            "conversation_id": .string("conv_window"),
            "title": .string("插入窗口"),
            "statistics": .object([
                "last_tool_call_at": .string(WorkbenchFormatting.iso(lastToolCall))
            ])
        ]), serverNow: server)
    }

    private static func date(_ raw: String) -> Date {
        guard let value = ISO8601DateFormatter().date(from: raw) else {
            preconditionFailure("invalid fixture date")
        }
        return value
    }
}
