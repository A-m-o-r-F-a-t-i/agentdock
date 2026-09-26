import Foundation

/// Window-scoped presentation. All durable business decisions belong to Core.
@MainActor
final class WorkbenchViewModel {
    private(set) var snapshot: WorkbenchSnapshot
    private(set) var listView: WorkbenchListView = .active
    private(set) var searchText = ""
    private(set) var selectedNavigationID = ""
    var selectedConversationID: String { snapshot.selectedConversation?.id ?? "" }
    private(set) var selectedCallID = ""
    private(set) var isRefreshing = false
    private(set) var isOperating = false
    private(set) var payloadSlices = [String: WorkbenchPayloadSlice]()
    private(set) var isReadingPayload = false
    var onChange: ((WorkbenchViewModel) -> Void)?
    let client: WorkbenchAPIClient
    let fixtureMode: Bool
    static let maximumCalls = 1000
    private var modes = [String: String]()
    private var limits = [String: Int]()
    private var cursors = [String: String]()
    private var epoch = 0
    private var refreshGeneration = 0
    private var started = false
    private var refreshTask: Task<Void, Never>?
    private var selectionTask: Task<Void, Never>?
    private var detailTask: Task<Void, Never>?
    private var payloadTask: Task<Void, Never>?
    private var searchTask: Task<Void, Never>?
    private var streamTask: Task<Void, Never>?
    private var timerTask: Task<Void, Never>?
    private var receiptTask: Task<Void, Never>?
    private var operationTask: Task<Void, Never>?
    private var notificationTask: Task<Void, Never>?
    private var serverAnchor: Date?
    private var uptimeAnchor = ProcessInfo.processInfo.systemUptime
    private var streamCursor: UInt64 = 0
    private var submissionKeys = [String: (text: String, id: String)]()

    init(client: WorkbenchAPIClient = WorkbenchAPIClient(), fixtureMode: Bool = false) {
        self.client = client
        self.fixtureMode = fixtureMode
        snapshot = fixtureMode ? .fixture() : WorkbenchSnapshot()
        selectedNavigationID = snapshot.selectedConversation?.navigationID ?? ""
        selectedCallID = snapshot.selectedCall?.id ?? ""
    }
    deinit {
        refreshTask?.cancel(); selectionTask?.cancel(); detailTask?.cancel()
        payloadTask?.cancel(); searchTask?.cancel(); streamTask?.cancel()
        timerTask?.cancel(); receiptTask?.cancel(); operationTask?.cancel(); notificationTask?.cancel()
    }
    func start() {
        guard !started else { notify(); return }
        started = true
        guard !fixtureMode else { notify(); return }
        refresh()
        timerTask = Task { [weak self] in
            var tick = 0
            while !Task.isCancelled {
                do { try await Task.sleep(nanoseconds: 1_000_000_000) } catch { return }
                guard let self, self.started else { return }
                tick += 1
                self.advanceClock()
                if tick % 15 == 0, !self.isRefreshing { self.refresh(reason: "poll") }
            }
        }
    }
    func stop() {
        started = false
        epoch += 1; refreshGeneration += 1
        refreshTask?.cancel(); selectionTask?.cancel(); detailTask?.cancel()
        payloadTask?.cancel(); searchTask?.cancel(); streamTask?.cancel()
        timerTask?.cancel(); receiptTask?.cancel(); operationTask?.cancel(); notificationTask?.cancel()
        streamTask = nil; notificationTask = nil
        isRefreshing = false; isReadingPayload = false; isOperating = false
    }
    func refresh(reason: String = "manual") {
        guard !fixtureMode else { notify(); return }
        refreshTask?.cancel()
        refreshGeneration += 1
        let generation = refreshGeneration
        let request = sidebarRequest()
        isRefreshing = true
        if reason != "poll" { snapshot.message = "正在读取 Core…" }
        notify()
        refreshTask = Task { [weak self] in
            guard let self else { return }
            do {
                async let overview = client.overview()
                async let sidebar = client.sidebar(request)
                let (newOverview, initialSidebar) = try await (overview, sidebar)
                try Task.checkCancellation()
                guard generation == refreshGeneration else { return }
                var newSidebar = initialSidebar
                // Load only summaries. Presentation applies 3-day/5/15 rules;
                // complete history is available through the paged manager.
                var discovered = false
                for group in newSidebar.groups where modes[group.id] == nil {
                    modes[group.id] = "history"; limits[group.id] = 20; discovered = true
                }
                if discovered {
                    newSidebar = try await client.sidebar(sidebarRequest())
                    try Task.checkCancellation()
                    guard generation == refreshGeneration else { return }
                }
                snapshot.overview = newOverview
                snapshot.sidebar = newSidebar
                serverAnchor = newSidebar.serverNow ?? newOverview.serverNow
                uptimeAnchor = ProcessInfo.processInfo.systemUptime
                for group in newSidebar.groups { cursors[group.id] = group.historyCursor }
                snapshot.stale = false; snapshot.lastLoadedAt = Date()
                snapshot.message = "已连接 · \(newSidebar.total) 个对话"
                isRefreshing = false
                let all = newSidebar.groups.flatMap(\.conversations)
                let selected = all.first { $0.navigationID == selectedNavigationID }
                    ?? (newSidebar.selected?.navigationID == selectedNavigationID ? newSidebar.selected : nil)
                    ?? all.first
                let changed = selected?.navigationID != selectedNavigationID
                if changed { selectConversation(selected?.navigationID ?? "") }
                else if let selected { snapshot.selectedConversation = selected }
                if !changed, reason != "poll" || snapshot.calls.calls.isEmpty { loadSelection() }
                if streamTask == nil { startStream(after: newSidebar.latestSequence) }
                notify()
            } catch {
                guard generation == refreshGeneration, !Task.isCancelled else { return }
                isRefreshing = false; snapshot.stale = true
                snapshot.message = "读取失败；原快照已保留：\(error.localizedDescription)"; notify()
            }
        }
    }
    func setListView(_ value: WorkbenchListView) {
        guard value != listView else { return }
        listView = value; cursors.removeAll()
        invalidateSelection(); selectedNavigationID = ""
        refresh(reason: "view")
    }
    func setSearchText(_ value: String) {
        let value = String(value.prefix(512))
        guard value != searchText else { return }
        searchText = value
        refreshGeneration += 1; refreshTask?.cancel(); isRefreshing = false
        searchTask?.cancel()
        searchTask = Task { [weak self] in
            do { try await Task.sleep(nanoseconds: 250_000_000) } catch { return }
            guard let self, !Task.isCancelled else { return }
            cursors.removeAll(); refresh(reason: "search")
        }
    }
    func loadMoreWorkspace(_ id: String) {
        modes[id] = "history"
        limits[id] = min(1000, (limits[id] ?? 20) + 20)
        refresh(reason: "workspace-history")
    }
    func selectConversation(_ navigationID: String) {
        guard navigationID != selectedNavigationID else { return }
        invalidateSelection()
        selectedNavigationID = navigationID
        snapshot.selectedConversation = snapshot.sidebar.groups.flatMap(\.conversations)
            .first { $0.navigationID == navigationID }
            ?? (snapshot.sidebar.selected?.navigationID == navigationID ? snapshot.sidebar.selected : nil)
        notify(); loadSelection()
    }
    private func invalidateSelection() {
        epoch += 1
        selectionTask?.cancel(); detailTask?.cancel(); payloadTask?.cancel(); receiptTask?.cancel()
        selectedCallID = ""; payloadSlices.removeAll(); isReadingPayload = false
        snapshot.selectedConversation = nil; snapshot.selectedCall = nil
        snapshot.calls = .empty; snapshot.task = nil; snapshot.permission = nil; snapshot.insertions = .empty
    }
    private func loadSelection() {
        selectionTask?.cancel()
        guard !fixtureMode, let selected = snapshot.selectedConversation else { return }
        let currentEpoch = epoch
        selectionTask = Task { [weak self] in
            guard let self else { return }
            do {
                let calls = try await client.calls(conversationID: selected.id, unattributed: selected.unattributed)
                var detailed = selected
                var permission: WorkbenchPermissionState?
                var insertions = WorkbenchInsertionPage.empty
                var task: WorkbenchTaskSummary?
                var warnings = [String]()
                if !selected.unattributed {
                    let json = try await client.conversation(selected.id)
                    let value = json["conversation"].isNull ? json : json["conversation"]
                    let merged = (selected.raw.objectValue ?? [:]).merging(value.objectValue ?? [:]) { _, new in new }
                    detailed = WorkbenchConversation(json: .object(merged), serverNow: snapshot.sidebar.serverNow)
                    do { permission = try await client.permission(conversationID: selected.id, workspaceID: detailed.workspaceID) }
                    catch { warnings.append("权限读取不可用：\(error.localizedDescription)") }
                    do { insertions = try await client.insertions(conversationID: selected.id) }
                    catch { warnings.append("用户补充读取不可用：\(error.localizedDescription)") }
                    if !detailed.activeTaskID.isEmpty {
                        do {
                            let data = try await client.task(detailed.activeTaskID)
                            let threads = (try? await client.taskThreads(detailed.activeTaskID)) ?? .null
                            task = WorkbenchTaskSummary(json: data, threads: threads)
                        } catch { warnings.append("任务读取不可用：\(error.localizedDescription)") }
                    }
                }
                try Task.checkCancellation()
                guard currentEpoch == epoch, selected.navigationID == selectedNavigationID else { return }
                snapshot.selectedConversation = detailed; snapshot.permission = permission
                snapshot.insertions = insertions; snapshot.task = task
                var page = calls
                page.calls = Self.mergeCalls(calls.calls, snapshot.calls.calls)
                snapshot.calls = page
                if !page.calls.contains(where: { $0.id == selectedCallID }) { selectedCallID = page.calls.first?.id ?? "" }
                snapshot.selectedCall = page.calls.first { $0.id == selectedCallID }
                snapshot.message = warnings.isEmpty ? "已同步对话详情" : warnings.joined(separator: " · ")
                notify()
            } catch {
                guard currentEpoch == epoch, !Task.isCancelled else { return }
                snapshot.message = "详情读取失败：\(error.localizedDescription)"; snapshot.stale = true; notify()
            }
        }
    }
    func selectCall(_ id: String) {
        guard id != selectedCallID else { return }
        detailTask?.cancel(); payloadTask?.cancel()
        selectedCallID = id; payloadSlices.removeAll(); isReadingPayload = false
        snapshot.selectedCall = snapshot.calls.calls.first { $0.id == id }; notify()
        guard !fixtureMode, !id.isEmpty else { return }
        let currentEpoch = epoch
        detailTask = Task { [weak self] in
            guard let self else { return }
            do {
                let value = try await client.call(id)
                try Task.checkCancellation()
                guard currentEpoch == epoch, id == selectedCallID else { return }
                snapshot.selectedCall = value
                snapshot.calls.calls = Self.mergeCalls(snapshot.calls.calls, [value]); notify()
            } catch {
                guard currentEpoch == epoch, id == selectedCallID, !Task.isCancelled else { return }
                snapshot.message = "调用详情不可用：\(error.localizedDescription)"; notify()
            }
        }
    }
    func loadPayload(source: String, restart: Bool = false) {
        guard !fixtureMode, !selectedCallID.isEmpty, !isReadingPayload else { return }
        let id = selectedCallID, currentEpoch = epoch
        let offset = restart ? 0 : (payloadSlices[source]?.nextOffset ?? 0)
        guard restart || payloadSlices[source]?.hasMore != false else { return }
        isReadingPayload = true; notify()
        payloadTask = Task { [weak self] in
            guard let self else { return }
            do {
                let json = try await client.callPayload(id, source: source, offset: offset)
                let slice = try WorkbenchPayloadSlice(json: json)
                try Task.checkCancellation()
                guard currentEpoch == epoch, id == selectedCallID else { return }
                guard !slice.hasMore || slice.nextOffset > offset else {
                    throw WorkbenchClientError.invalidResponse("输出游标未前进；已停止重复读取。")
                }
                payloadSlices[source] = slice; isReadingPayload = false; notify()
            } catch {
                guard currentEpoch == epoch, id == selectedCallID, !Task.isCancelled else { return }
                isReadingPayload = false
                snapshot.message = "分块输出读取失败：\(error.localizedDescription)"; notify()
            }
        }
    }
    func loadOlderCalls() {
        guard let selected = snapshot.selectedConversation, snapshot.calls.hasMore,
              snapshot.calls.nextBefore > 0 else { return }
        let before = snapshot.calls.nextBefore, currentEpoch = epoch
        performOperation { [weak self] in
            guard let self else { return .object([:]) }
            let older = try await client.calls(conversationID: selected.id, unattributed: selected.unattributed, before: before)
            guard currentEpoch == epoch else { return .object([:]) }
            var page = older
            if snapshot.calls.calls.count + older.calls.count <= Self.maximumCalls {
                page.calls = Self.mergeCalls(snapshot.calls.calls, older.calls)
            }
            snapshot.calls = page
            return .object(["message": .string("已读取更早记录；显示窗口最多 1000 条。")])
        }
    }
    static func mergeCalls(_ old: [WorkbenchCall], _ incoming: [WorkbenchCall]) -> [WorkbenchCall] {
        var byID = Dictionary(old.filter { !$0.id.isEmpty }.map { ($0.id, $0) },
                              uniquingKeysWith: { a, b in a.sequence >= b.sequence ? a : b })
        for item in incoming where !item.id.isEmpty {
            if let previous = byID[item.id], previous.sequence > item.sequence { continue }
            byID[item.id] = item
        }
        return Array(byID.values.sorted { a, b in
            a.sequence == b.sequence ? a.id > b.id : a.sequence > b.sequence
        }.prefix(maximumCalls))
    }
    private func startStream(after: UInt64) {
        streamCursor = after
        streamTask = Task { [weak self] in
            var backoff: UInt64 = 1
            while !Task.isCancelled {
                guard let self, started else { return }
                do {
                    let path = client.callStreamPath(conversationID: "", after: streamCursor)
                    for try await event in client.eventStream(path: path, lastEventID: String(streamCursor)) {
                        try Task.checkCancellation()
                        switch event.name {
                        case "call":
                            let call = WorkbenchCall(json: event.data)
                            let selected = snapshot.selectedConversation
                            let matches = selected?.unattributed == true ? call.conversationID.isEmpty :
                                (!selectedConversationID.isEmpty && call.conversationID == selectedConversationID)
                            if matches {
                                snapshot.calls.calls = Self.mergeCalls(snapshot.calls.calls, [call])
                                if call.id == selectedCallID {
                                    snapshot.selectedCall = snapshot.calls.calls.first { $0.id == call.id }
                                }
                            }
                            if !call.conversationID.isEmpty,
                               !snapshot.sidebar.groups.flatMap(\.conversations).contains(where: { $0.id == call.conversationID }),
                               !isRefreshing { refresh(reason: "arrival") }
                            backoff = 1; scheduleNotification()
                        case "reset", "gap":
                            streamTask = nil; refresh(reason: "stream-reset"); return
                        case "warning":
                            snapshot.message = event.data.firstText("reason", "message"); scheduleNotification()
                        default: break
                        }
                        streamCursor = max(streamCursor, UInt64(event.id) ?? event.data.unsigned("seq"))
                    }
                } catch {
                    guard !Task.isCancelled else { return }
                    snapshot.stale = true
                    snapshot.message = "活动流断开：\(error.localizedDescription)"; notify()
                    if let error = error as? WorkbenchClientError, !error.retryable {
                        streamTask = nil; return
                    }
                }
                do { try await Task.sleep(nanoseconds: backoff * 1_000_000_000) } catch { return }
                backoff = min(16, backoff * 2)
            }
        }
    }
    private func advanceClock() {
        guard !fixtureMode, let serverAnchor else { return }
        let now = serverAnchor.addingTimeInterval(max(0, ProcessInfo.processInfo.systemUptime - uptimeAnchor))
        for g in snapshot.sidebar.groups.indices {
            for c in snapshot.sidebar.groups[g].conversations.indices {
                snapshot.sidebar.groups[g].conversations[c].advancePresentation(serverNow: now)
            }
        }
        snapshot.selectedConversation?.advancePresentation(serverNow: now); scheduleNotification()
    }
    private func scheduleNotification() {
        guard notificationTask == nil else { return }
        notificationTask = Task { [weak self] in
            do { try await Task.sleep(nanoseconds: 100_000_000) } catch { return }
            guard let self else { return }
            notificationTask = nil; notify()
        }
    }
    private func sidebarRequest() -> WorkbenchSidebarRequest {
        WorkbenchSidebarRequest(view: listView, search: searchText, limits: limits,
            modes: modes, cursors: cursors, defaultMode: "auto", selectedConversationID: selectedConversationID)
    }
    func manageSelectedConversation(action: String, title: String = "", tags: [String] = [],
                                    retentionDays: Int = 0, confirmPermanent: Bool = false) {
        let id = selectedConversationID
        guard !id.isEmpty else { return }
        performOperation(refreshAfter: true) { [client] in
            try await client.manage(kind: "conversation", ids: [id], action: action,
                                    title: title, tags: tags, retentionDays: retentionDays, confirmPermanent: confirmPermanent)
        }
    }
    func setConversationTerminated(_ terminated: Bool) {
        let id = selectedConversationID
        guard !id.isEmpty else { return }
        performOperation(refreshAfter: true) { [client] in
            try await client.conversationLifecycle(id: id, action: terminated ? "terminate" : "resume")
        }
    }
    func linkSelectedConversation(to taskID: String) {
        let id = selectedConversationID
        guard !id.isEmpty, !taskID.isEmpty else { return }
        performOperation(refreshAfter: true) { [client] in try await client.linkConversation(id: id, taskID: taskID) }
    }
    func setSelectedCurrentTask(taskID: String, threadID: String = "main") {
        let id = selectedConversationID
        guard !id.isEmpty, let revision = snapshot.selectedConversation?.bindingRevision else { return }
        performOperation(refreshAfter: true) { [client] in
            try await client.setCurrentTask(conversationID: id, taskID: taskID, threadID: threadID, bindingRevision: revision)
        }
    }
    func stopSelectedCall() {
        guard let call = snapshot.selectedCall, call.canStop else { return }
        performOperation(refreshAfter: true) { [client] in try await client.stopCall(call.id) }
    }
    func decideSelectedApproval(approve: Bool, allowWorkspace: Bool = false) {
        guard let call = snapshot.selectedCall, call.needsApproval else { return }
        performOperation(refreshAfter: true) { [client] in
            try await client.decideApproval(call.approvalID, action: approve ? "approve" : "reject", allowWorkspace: allowWorkspace)
        }
    }
    func updatePermissionMode(_ mode: String) {
        guard let permission = snapshot.permission, !selectedConversationID.isEmpty,
              permission.revision > 0, permission.revision <= UInt64(Int64.max) else { return }
        let change: WorkbenchJSON = .object([
            "scope": .string("conversation"), "scope_id": .string(selectedConversationID),
            "mode": .string(mode), "expected_revision": .integer(Int64(permission.revision)),
            "confirm_full": .bool(mode == "full")])
        performOperation(refreshAfter: true) { [client] in try await client.updatePermission(change) }
    }
    func submitInsertion(_ text: String) {
        guard !fixtureMode, !isOperating, !snapshot.stale,
              let conversation = snapshot.selectedConversation, !conversation.id.isEmpty,
              conversation.insertionEligible == true else {
            snapshot.message = "插入资格未确认；请刷新 Core 状态。"; notify(); return
        }
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, text.utf8.count <= 16384 else { return }
        let key = submissionKeys[conversation.id]
        let submission = key?.text == text ? key!.id : "macos-\(UUID().uuidString.lowercased())"
        if submissionKeys.count >= 100 { submissionKeys.removeAll() }
        submissionKeys[conversation.id] = (text, submission)
        let currentEpoch = epoch
        performOperation { [weak self] in
            guard let self else { return .object([:]) }
            do {
                let result = try await client.enqueueInsertion(conversationID: conversation.id, submissionID: submission, text: text)
                if currentEpoch == epoch { observeInsertion(conversation.id, submission: submission) }
                return result
            } catch {
                if currentEpoch == epoch { observeInsertion(conversation.id, submission: submission) }
                throw error
            }
        }
    }
    private func observeInsertion(_ conversationID: String, submission: String) {
        receiptTask?.cancel()
        let currentEpoch = epoch
        receiptTask = Task { [weak self] in
            guard let self else { return }
            for _ in 0..<20 {
                do {
                    let page = try await client.insertions(conversationID: conversationID)
                    try Task.checkCancellation()
                    guard currentEpoch == epoch else { return }
                    snapshot.insertions = page
                    if let item = page.items.first(where: { $0.raw.text("submission_id") == submission }) {
                        snapshot.message = item.detailText; notify()
                        if item.terminal { return }
                    }
                } catch {
                    guard currentEpoch == epoch, !Task.isCancelled else { return }
                    snapshot.message = "写入结果待核对，未自动重试：\(error.localizedDescription)"; notify()
                }
                do { try await Task.sleep(nanoseconds: 1_500_000_000) } catch { return }
            }
            guard currentEpoch == epoch else { return }
            snapshot.message = "回执仍待确认；重投与到期时间以 Core 返回值为准。"; notify()
        }
    }
    func insertionAction(_ insertionID: String, action: String) {
        let id = selectedConversationID
        guard !id.isEmpty, let item = snapshot.insertions.items.first(where: { $0.id == insertionID }),
              !item.terminal, action != "retry" || item.manualRetryAvailable else { return }
        performOperation(refreshAfter: true) { [client] in
            try await client.insertionAction(conversationID: id, insertionID: insertionID, action: action)
        }
    }
    private func performOperation(refreshAfter: Bool = false,
                                  operation: @escaping @MainActor () async throws -> WorkbenchJSON) {
        guard !fixtureMode, !isOperating, !snapshot.stale else { return }
        isOperating = true
        let currentEpoch = epoch
        snapshot.message = "正在提交操作…"; notify()
        operationTask = Task { [weak self] in
            guard let self else { return }
            defer { isOperating = false; notify() }
            do {
                let result = try await operation()
                guard currentEpoch == epoch, !Task.isCancelled else { return }
                snapshot.message = result.firstText("message").isEmpty ?
                    "Core 已返回操作结果；不等同工具或任务执行完成。" : result.text("message")
                if refreshAfter { refresh(reason: "operation") }
            } catch {
                guard currentEpoch == epoch, !Task.isCancelled else { return }
                snapshot.message = "操作结果待核对，未自动重试：\(error.localizedDescription)"
                loadSelection()
            }
        }
    }
    func exportSelectedCall() -> String { snapshot.selectedCall?.raw.prettyPrinted ?? "" }
    private func notify() { onChange?(self) }
}
