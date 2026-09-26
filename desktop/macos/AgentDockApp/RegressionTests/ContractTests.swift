import XCTest
@testable import WorkbenchKit

final class ContractTests: XCTestCase {
    private var previousLanguage: UILanguagePreference = .system
    override func setUp() {
        super.setUp()
        previousLanguage = L10n.languagePreference()
        L10n.setLanguagePreference(.simplifiedChinese)
    }
    override func tearDown() {
        L10n.setLanguagePreference(previousLanguage)
        super.tearDown()
    }

    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func conversation(_ id: String, age: TimeInterval = 5, live: Bool = false) -> WorkbenchJSON {
        .object(["conversation_id": .string(id), "title": .string("中文 e\u{301} 👩‍💻"),
            "in_flight": .bool(live), "last_activity_at": .string(WorkbenchFormatting.iso(now)),
            "last_interaction_at": .string(WorkbenchFormatting.iso(now.addingTimeInterval(-age))),
            "statistics": .object(["last_tool_call_at": .string(WorkbenchFormatting.iso(now.addingTimeInterval(-age)))])])
    }
    private func sidebar(_ rows: [WorkbenchJSON], workspace: String = "wsp_test") -> WorkbenchJSON {
        .object(["server_now": .string(WorkbenchFormatting.iso(now)), "groups": .array([
            .object(["workspace_id": .string(workspace), "conversations": .array(rows)])])])
    }
    func testInteractionAndLiveExecutionRemainIndependent() {
        let expired = WorkbenchConversation(json: conversation("conv_1", age: 121, live: true), serverNow: now)
        XCTAssertTrue(expired.inFlight)
        XCTAssertFalse(expired.recentlyActive)
        XCTAssertTrue(expired.insertionEligible == true)
        XCTAssertFalse(WorkbenchConversation(json: conversation("conv_1", age: 180), serverNow: now).insertionEligible == true)
        XCTAssertTrue(WorkbenchConversation(json: conversation("conv_1", age: 119.99), serverNow: now).recentlyActive)
        XCTAssertFalse(WorkbenchConversation(json: conversation("conv_1", age: -2), serverNow: now).recentlyActive)
    }
    func testUnknownTimesStayUnknownAndAsyncOutputDoesNotRenew() {
        let item = WorkbenchConversation(json: .object(["conversation_id": .string("conv_1"),
            "last_activity_at": .string(WorkbenchFormatting.iso(now)), "in_flight": .bool(true)]), serverNow: now)
        XCTAssertFalse(item.recentlyActive)
        XCTAssertNil(item.insertionEligible)
    }
    func testServerClockProgressExpiresPresentation() {
        var item = WorkbenchConversation(json: conversation("conv_1", age: 119), serverNow: now)
        item.advancePresentation(serverNow: now.addingTimeInterval(2))
        XCTAssertFalse(item.recentlyActive)
        item.advancePresentation(serverNow: now.addingTimeInterval(61))
        XCTAssertEqual(item.insertionEligible, false)
    }
    func testSidebarRejectsDuplicateAndMissingOrdinaryIDs() throws {
        XCTAssertThrowsError(try WorkbenchValidation.sidebar(sidebar([conversation("conv_1"), conversation("conv_1")])))
        XCTAssertThrowsError(try WorkbenchValidation.sidebar(sidebar([conversation("")])))
        XCTAssertThrowsError(try WorkbenchValidation.sidebar(sidebar([conversation("footer:page")])))
        XCTAssertThrowsError(try WorkbenchValidation.sidebar(sidebar([conversation("unattributed")])))
        try WorkbenchValidation.sidebar(sidebar([conversation("conv_1"), conversation("conv_2")]))
    }
    func testUnattributedUsesNavigationOnly() throws {
        let row: WorkbenchJSON = .object(["conversation_id": .string(""), "is_unattributed": .bool(true)])
        try WorkbenchValidation.sidebar(sidebar([row], workspace: "unattributed"))
        XCTAssertThrowsError(try WorkbenchValidation.sidebar(sidebar([row, row], workspace: "unattributed")))
        XCTAssertThrowsError(try WorkbenchValidation.sidebar(sidebar([row], workspace: "wsp_test")))
        let item = WorkbenchConversation(json: row, serverNow: now)
        XCTAssertEqual(item.id, "")
        XCTAssertEqual(item.navigationID, "unattributed")
    }
    func testRecentFiveExpandedFifteenAndCompleteHistory() {
        let rows = (0..<1000).map { conversation("conv_\($0)", age: Double($0)) }
        let group = WorkbenchSidebarPage(json: sidebar(rows)).groups[0]
        XCTAssertEqual(WorkbenchSidebarPolicy.rows(group, expanded: false, selected: "", now: now, fullHistory: false).count, 5)
        XCTAssertEqual(WorkbenchSidebarPolicy.rows(group, expanded: true, selected: "", now: now, fullHistory: false).count, 15)
        XCTAssertEqual(WorkbenchSidebarPolicy.rows(group, expanded: false, selected: "conv_900", now: now, fullHistory: false).count, 6)
        XCTAssertEqual(WorkbenchSidebarPolicy.rows(group, expanded: false, selected: "", now: now, fullHistory: true).count, 1000)
    }
    func testOldPinnedAndLiveRowsSurviveRecentFilter() {
        var old = conversation("conv_old", age: 5 * 86400).objectValue!
        old["last_activity_at"] = .string(WorkbenchFormatting.iso(now.addingTimeInterval(-5 * 86400)))
        old["pinned"] = .bool(true)
        let group = WorkbenchSidebarPage(json: sidebar([.object(old)])).groups[0]
        XCTAssertEqual(WorkbenchSidebarPolicy.rows(group, expanded: false, selected: "", now: now, fullHistory: false).count, 1)
    }
    func testNewPermissionFieldsKeepConfiguredAndEffectiveSeparate() {
        let state = WorkbenchPermissionState(json: .object(["effective": .object([
            "revision": .integer(3), "mode": .string("full"), "custom_permissions_enabled": .bool(false),
            "settings_source": .string("execution_mode"),
            "settings": .object(["permission_profile": .object(["filesystem": .string("write")])]),
            "configured_settings": .object(["permission_profile": .object(["filesystem": .string("deny")])])])]))
        XCTAssertEqual(state.customSettingsEnabled, false)
        XCTAssertEqual(state.settingsSource, "execution_mode")
        XCTAssertEqual(state.settings["permission_profile"].text("filesystem"), "write")
        XCTAssertEqual(state.configuredSettings["permission_profile"].text("filesystem"), "deny")
    }
    func testCanonicalNoneReceiptIsUnconfirmedNotUnknown() {
        for kind in ["", "none"] {
            let item = WorkbenchInsertion(json: .object([
                "insertion_id": .string("ins_none"), "status": .string("delivery_unknown"),
                "receipt_type": .string(kind), "manual_retry_available": .bool(true),
                "automatic_attempts_remaining": .integer(0), "total_attempts_remaining": .integer(3)]))
            XCTAssertEqual(item.receiptDescription, L10n.text("Receipt unconfirmed"))
            XCTAssertTrue(item.manualRetryAvailable)
            XCTAssertFalse(item.terminal)
        }
    }
    func testReceiptTypesDoNotAssertModelExecution() {
        for kind in ["receiver_receipt", "outer_forwarded", "host_context_committed", "new_unknown_kind"] {
            let item = WorkbenchInsertion(json: .object(["insertion_id": .string("ins_1"),
                "status": .string("inner_appended"), "receipt_type": .string(kind),
                "manual_retry_available": .bool(false), "delivery_attempts": .integer(3),
                "automatic_attempts_remaining": .integer(0), "total_attempts_remaining": .integer(3)]))
            XCTAssertEqual(item.receiptType, kind)
            XCTAssertFalse(item.terminal)
            XCTAssertFalse(item.manualRetryAvailable)
            XCTAssertEqual(item.automaticAttemptsRemaining, 0)
            XCTAssertFalse(item.receiptDescription.contains("模型已执行"))
        }
    }
    func testPayloadScalarAndByteUnits() throws {
        let text = "中😀e\u{301}"
        let item = try WorkbenchPayloadSlice(json: .object(["text": .string(text),
            "next_offset": .integer(Int64(text.utf8.count)), "has_more": .bool(true)]))
        XCTAssertEqual(item.returnedScalars, 4)
        XCTAssertEqual(item.nextOffset, 10)
        XCTAssertTrue(item.caption.contains("字节"))
        XCTAssertThrowsError(try WorkbenchPayloadSlice(json: .object(["text": .string(text), "next_offset": .integer(-1)])))
    }
    func testCallPagingMovesBackwardAndScopedSkillsRemainDistinct() throws {
        let calls: WorkbenchJSON = .object(["calls": .array([.object(["call_id": .string("call_1")])]),
            "has_more": .bool(true), "next_before": .integer(80)])
        XCTAssertEqual(try WorkbenchManagementPage(calls, resource: .calls, offset: 100).nextOffset, 80)
        XCTAssertThrowsError(try WorkbenchManagementPage(calls, resource: .calls, offset: 80))
        let skills: WorkbenchJSON = .object(["skills": .array([
            .object(["name": .string("shared"), "skill_ref": .string("skill://managed/shared")]),
            .object(["name": .string("shared"), "skill_ref": .string("skill://plugin/example/shared")])])])
        XCTAssertEqual(try WorkbenchManagementPage(skills, resource: .skills, offset: 0).items.count, 2)
    }
    func testNumericOverflowAndUnknownFields() throws {
        XCTAssertNil(WorkbenchJSON.number(Double(Int64.max)).int64Value)
        let decoded = try WorkbenchJSON.decode(Data(#"{"unknown":{"x":1},"large":9223372036854775807}"#.utf8))
        XCTAssertEqual(decoded["large"].int64Value, Int64.max)
        XCTAssertEqual(try WorkbenchJSON.decode(decoded.encodedData()), decoded)
    }
    func testPagingAndPartialFailuresAreVisible() throws {
        let json: WorkbenchJSON = .object(["tasks": .array([.object(["task_id": .string("task_1")])]), "has_more": .bool(true), "next_offset": .integer(1)])
        XCTAssertTrue(try WorkbenchManagementPage(json, resource: .tasks, offset: 0).hasMore)
        XCTAssertThrowsError(try WorkbenchManagementPage(json, resource: .tasks, offset: 1))
        XCTAssertThrowsError(try WorkbenchValidation.mutation(.object(["failed": .integer(1), "succeeded": .integer(2)])))
        XCTAssertThrowsError(try WorkbenchValidation.mutation(.object(["ok": .bool(false)])))
    }
    func testSSEFragmentationCRLFAndLimits() throws {
        let input = "id: 5\revent: call\rdata: {\"call_id\":\"call_5\"}\r\r"
        var parser = WorkbenchSSEParser(maximumLineBytes: 128, maximumEventBytes: 256)
        var events = [WorkbenchStreamEvent]()
        for byte in input.utf8 { events += try parser.feed(byte) }
        XCTAssertEqual(events.count, 1)
        XCTAssertEqual(events[0].id, "5")
        var limited = WorkbenchSSEParser(maximumLineBytes: 8, maximumEventBytes: 16)
        XCTAssertThrowsError(try Array("data: 0123456789".utf8).forEach { _ = try limited.feed($0) })
    }
    @MainActor func testTenThousandCallsAreBoundedAndMonotonic() async {
        let incoming = (1...10000).map { WorkbenchCall(json: .object([
            "call_id": .string("call_\($0)"), "updated_seq": .integer(Int64($0)), "status": .string("succeeded")])) }
        let merged = WorkbenchViewModel.mergeCalls([], incoming)
        XCTAssertEqual(merged.count, 1000)
        XCTAssertEqual(merged.first?.sequence, 10000)
        let stale = WorkbenchCall(json: .object(["call_id": .string("call_10000"), "updated_seq": .integer(1), "status": .string("running")]))
        let again = WorkbenchViewModel.mergeCalls(merged, [stale, merged[0]])
        XCTAssertEqual(again.count, 1000)
        XCTAssertEqual(again.first?.status, "succeeded")
    }
}
