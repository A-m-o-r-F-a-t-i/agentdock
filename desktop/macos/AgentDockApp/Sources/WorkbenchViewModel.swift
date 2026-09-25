import Foundation

@MainActor
final class WorkbenchViewModel {
    private(set) var snapshot: WorkbenchSnapshot
    private(set) var listView: WorkbenchListView = .active
    private(set) var searchText = ""
    private(set) var selectedConversationID = ""
    private(set) var selectedCallID = ""
    private(set) var isRefreshing = false
    private(set) var isOperating = false

    var onChange: ((WorkbenchViewModel) -> Void)?

    private let client: WorkbenchAPIClient
    private let fixtureMode: Bool
    private var workspaceModes = [String: String]()
    private var workspaceLimits = [String: Int]()
    private var workspaceCursors = [String: String]()
    private var refreshGeneration = 0
    private var refreshTask: Task<Void, Never>?
    private var searchTask: Task<Void, Never>?
    private var detailTask: Task<Void, Never>?
    private var streamTask: Task<Void, Never>?
    private var receiptTask: Task<Void, Never>?
    private var lastStreamEventID = ""
    private var streamBackoffSeconds: UInt64 = 1
    private var started = false

    init(client: WorkbenchAPIClient = WorkbenchAPIClient(), fixtureMode: Bool = false) {
        self.client = client
        self.fixtureMode = fixtureMode
        snapshot = fixtureMode ? .fixture() : WorkbenchSnapshot()
        selectedConversationID = snapshot.selectedConversation?.id ?? ""
        selectedCallID = snapshot.selectedCall?.id ?? ""
    }

    deinit {
        refreshTask?.cancel()
        searchTask?.cancel()
        detailTask?.cancel()
        streamTask?.cancel()
        receiptTask?.cancel()
    }

    func start() {
        guard !started else {
            notify()
            return
        }
        started = true
        if fixtureMode {
            notify()
            return
        }
        refresh(reason: "initial")
    }

    func stop() {
        started = false
        refreshTask?.cancel()
        searchTask?.cancel()
        detailTask?.cancel()
        streamTask?.cancel()
        receiptTask?.cancel()
    }

    func refresh(reason: String = "manual") {
        guard !fixtureMode else {
            snapshot = .fixture()
            notify()
            return
        }
        refreshTask?.cancel()
        refreshGeneration += 1
        let generation = refreshGeneration
        isRefreshing = true
        snapshot.message = reason == "manual" ? "正在刷新…" : "正在连接 AgentDock Core…"
        notify()

        let request = sidebarRequest()
        refreshTask = Task { [weak self] in
            guard let self else { return }
            do {
                async let overview = client.overview()
                async let page = client.sidebar(request)
                var (loadedOverview, loadedSidebar) = try await (overview, page)
                try Task.checkCancellation()
                guard generation == refreshGeneration else { return }

                // The shared sidebar accepts 5 as the compact history size.
                // Bootstrap known workspaces once, then ask Core for its stable
                // history ordering instead of sorting or filtering locally.
                if listView == .active,
                   searchText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
                   workspaceModes.isEmpty,
                   !loadedSidebar.groups.isEmpty {
                    for group in loadedSidebar.groups {
                        workspaceModes[group.id] = "history"
                        workspaceLimits[group.id] = 5
                        if !group.historyCursor.isEmpty { workspaceCursors[group.id] = group.historyCursor }
                    }
                    loadedSidebar = try await client.sidebar(sidebarRequest())
                    try Task.checkCancellation()
                    guard generation == refreshGeneration else { return }
                }

                snapshot.overview = loadedOverview
                snapshot.sidebar = loadedSidebar
                snapshot.stale = false
                snapshot.lastLoadedAt = Date()
                snapshot.message = connectedSummary(loadedSidebar)
                reconcileSelection(preferred: selectedConversationID)
                isRefreshing = false
                notify()
                if !selectedConversationID.isEmpty {
                    loadSelection(generation: generation)
                }
            } catch is CancellationError {
                return
            } catch let error as WorkbenchClientError {
                guard generation == refreshGeneration else { return }
                isRefreshing = false
                snapshot.stale = snapshot.lastLoadedAt != nil
                snapshot.message = error.localizedDescription
                notify()
            } catch {
                guard generation == refreshGeneration else { return }
                isRefreshing = false
                snapshot.stale = snapshot.lastLoadedAt != nil
                snapshot.message = error.localizedDescription
                notify()
            }
        }
    }

    func setListView(_ value: WorkbenchListView) {
        guard value != listView else { return }
        listView = value
        workspaceCursors.removeAll()
        selectedConversationID = ""
        selectedCallID = ""
        snapshot.selectedConversation = nil
        snapshot.selectedCall = nil
        snapshot.calls = .empty
        refresh(reason: "view")
    }

    func setSearchText(_ value: String) {
        let normalized = String(value.prefix(512))
        guard normalized != searchText else { return }
        searchText = normalized
        searchTask?.cancel()
        searchTask = Task { [weak self] in
            do {
                try await Task.sleep(nanoseconds: 250_000_000)
                guard let self, !Task.isCancelled else { return }
                self.workspaceCursors.removeAll()
                self.refresh(reason: "search")
            } catch {
                return
            }
        }
    }

    func toggleWorkspace(_ id: String) {
        let current = workspaceModes[id] ?? "history"
        if current == "collapsed" {
            workspaceModes[id] = "history"
            workspaceLimits[id] = max(5, workspaceLimits[id] ?? 5)
        } else {
            workspaceModes[id] = "collapsed"
            workspaceLimits[id] = 0
        }
        refresh(reason: "workspace")
    }

    func loadMoreWorkspace(_ id: String) {
        workspaceModes[id] = "history"
        let current = workspaceLimits[id] ?? 5
        workspaceLimits[id] = current <= 5 ? 20 : min(200, current + 20)
        refresh(reason: "workspace-history")
    }

    func selectConversation(_ id: String) {
        guard id != selectedConversationID else { return }
        selectedConversationID = id
        selectedCallID = ""
        snapshot.selectedCall = nil
        reconcileSelection(preferred: id)
        notify()
        if !fixtureMode {
            loadSelection(generation: refreshGeneration)
        }
    }

    func selectCall(_ id: String) {
        guard id != selectedCallID else { return }
        selectedCallID = id
        if let summary = snapshot.calls.calls.first(where: { $0.id == id }) {
            snapshot.selectedCall = summary
        }
        notify()
        guard !fixtureMode, !id.isEmpty else { return }
        let conversationAtStart = selectedConversationID
        detailTask?.cancel()
        detailTask = Task { [weak self] in
            guard let self else { return }
            do {
                let detail = try await client.call(id)
                try Task.checkCancellation()
                guard conversationAtStart == selectedConversationID, id == selectedCallID else { return }
                snapshot.selectedCall = detail
                upsertCall(detail)
                notify()
            } catch is CancellationError {
                return
            } catch {
                guard conversationAtStart == selectedConversationID, id == selectedCallID else { return }
                snapshot.message = "调用详情读取失败：\(error.localizedDescription)"
                notify()
            }
        }
    }

    func loadOlderCalls() {
        guard !fixtureMode,
              !selectedConversationID.isEmpty,
              snapshot.calls.hasMore,
              snapshot.calls.nextBefore > 0 else { return }
        let conversation = selectedConversationID
        let before = snapshot.calls.nextBefore
        performOperation(successMessage: "已加载更早的执行记录", refreshAfter: false) { [weak self] in
            guard let self else { return }
            let older = try await client.calls(
                conversationID: conversation,
                unattributed: snapshot.selectedConversation?.unattributed ?? false,
                before: before,
                limit: 100
            )
            guard conversation == selectedConversationID else { return }
            var merged = snapshot.calls.calls
            let known = Set(merged.map(\.id))
            merged.append(contentsOf: older.calls.filter { !known.contains($0.id) })
            snapshot.calls.calls = merged
            snapshot.calls.hasMore = older.hasMore
            snapshot.calls.nextBefore = older.nextBefore
            snapshot.calls.latestSequence = max(snapshot.calls.latestSequence, older.latestSequence)
            notify()
        }
    }

    func manageSelectedConversation(
        action: String,
        title: String = "",
        tags: [String] = [],
        retentionDays: Int = 0,
        confirmPermanent: Bool = false
    ) {
        guard !selectedConversationID.isEmpty else { return }
        let id = selectedConversationID
        performOperation(successMessage: "对话操作已提交") { [weak self] in
            guard let self else { return }
            _ = try await client.manage(
                kind: "conversation",
                ids: [id],
                action: action,
                title: title,
                tags: tags,
                retentionDays: retentionDays,
                confirmPermanent: confirmPermanent
            )
        }
    }

    func setConversationTerminated(_ terminated: Bool) {
        guard !selectedConversationID.isEmpty else { return }
        let id = selectedConversationID
        performOperation(successMessage: terminated ? "已请求终止对话" : "已恢复对话") { [weak self] in
            guard let self else { return }
            _ = try await client.conversationLifecycle(id: id, action: terminated ? "terminate" : "resume")
        }
    }

    func linkSelectedConversation(to taskID: String) {
        guard !selectedConversationID.isEmpty, !taskID.isEmpty else { return }
        let conversation = selectedConversationID
        performOperation(successMessage: "任务已关联") { [weak self] in
            guard let self else { return }
            _ = try await client.linkConversation(id: conversation, taskID: taskID)
        }
    }

    func setSelectedCurrentTask(taskID: String, threadID: String = "main") {
        guard !selectedConversationID.isEmpty, !taskID.isEmpty else { return }
        guard let bindingRevision = snapshot.selectedConversation?.bindingRevision else {
            snapshot.message = "Core 未返回 binding_revision；为避免覆盖更新，当前任务修改已禁用。"
            notify()
            return
        }
        let conversation = selectedConversationID
        performOperation(successMessage: "当前任务已更新") { [weak self] in
            guard let self else { return }
            _ = try await client.setCurrentTask(
                conversationID: conversation,
                taskID: taskID,
                threadID: threadID.isEmpty ? "main" : threadID,
                bindingRevision: bindingRevision
            )
        }
    }

    func stopSelectedCall() {
        guard let call = snapshot.selectedCall, call.canStop else { return }
        performOperation(successMessage: "停止请求已提交") { [weak self] in
            guard let self else { return }
            _ = try await client.stopCall(call.id)
        }
    }

    func decideSelectedApproval(approve: Bool, allowWorkspace: Bool = false) {
        guard let call = snapshot.selectedCall, !call.approvalID.isEmpty else { return }
        performOperation(successMessage: approve ? "审批已通过" : "审批已拒绝") { [weak self] in
            guard let self else { return }
            _ = try await client.decideApproval(call.approvalID, action: approve ? "approve" : "reject", allowWorkspace: allowWorkspace)
        }
    }

    func updatePermissionMode(_ mode: String) {
        guard let permission = snapshot.permission else { return }
        var fields: [String: WorkbenchJSON] = [
            "scope": .string(permission.scope),
            "scope_id": .string(permission.scopeID),
            "mode": .string(mode),
            "expected_revision": .integer(Int64(permission.revision))
        ]
        if mode == "full" { fields["confirm_full"] = .bool(true) }
        performOperation(successMessage: "权限配置已更新") { [weak self] in
            guard let self else { return }
            _ = try await client.updatePermission(.object(fields))
        }
    }

    func submitInsertion(_ text: String) {
        guard let conversation = snapshot.selectedConversation,
              !conversation.id.isEmpty,
              conversation.insertionEligible != false else {
            snapshot.message = snapshot.selectedConversation?.insertionEligibilityReason.isEmpty == false
                ? snapshot.selectedConversation!.insertionEligibilityReason
                : "当前对话不在 180 秒插入窗口内。"
            notify()
            return
        }
        let normalized = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !normalized.isEmpty else { return }
        let submissionID = "macos-\(UUID().uuidString.lowercased())"
        let conversationID = conversation.id
        receiptTask?.cancel()
        isOperating = true
        snapshot.message = "正在提交用户补充…"
        notify()
        receiptTask = Task { [weak self] in
            guard let self else { return }
            do {
                let response = try await client.enqueueInsertion(conversationID: conversationID, submissionID: submissionID, text: normalized)
                guard conversationID == selectedConversationID else { return }
                let returned = response["insertion"]
                let returnedID = returned.firstText("insertion_id", "id")
                isOperating = false
                snapshot.message = returnedID.isEmpty ? "补充已提交，正在等待持久化回执…" : "补充已进入队列，等待模型回执…"
                notify()
                await waitForInsertionReceipt(conversationID: conversationID, submissionID: submissionID, insertionID: returnedID)
            } catch is CancellationError {
                return
            } catch {
                guard conversationID == selectedConversationID else { return }
                isOperating = false
                snapshot.message = "补充提交失败：\(error.localizedDescription)"
                notify()
            }
        }
    }

    func insertionAction(_ insertionID: String, action: String) {
        guard !selectedConversationID.isEmpty else { return }
        let conversation = selectedConversationID
        performOperation(successMessage: action == "retry" ? "重投请求已提交" : "补充已取消") { [weak self] in
            guard let self else { return }
            _ = try await client.insertionAction(conversationID: conversation, insertionID: insertionID, action: action)
        }
    }

    func exportSelectedCall() -> String {
        snapshot.selectedCall?.raw.prettyPrinted ?? ""
    }

    private func sidebarRequest() -> WorkbenchSidebarRequest {
        WorkbenchSidebarRequest(
            view: listView,
            search: searchText,
            limits: workspaceLimits,
            modes: workspaceModes,
            cursors: workspaceCursors,
            defaultMode: "auto",
            selectedConversationID: selectedConversationID
        )
    }

    private func connectedSummary(_ page: WorkbenchSidebarPage) -> String {
        let active = page.groups.reduce(0) { $0 + $1.recentCount }
        return "已连接 · \(page.total) 个对话 · \(active) 个活动中"
    }

    private func reconcileSelection(preferred: String) {
        let all = snapshot.sidebar.groups.flatMap(\.conversations)
        let preferredItem = all.first(where: { $0.id == preferred })
            ?? snapshot.sidebar.selected
            ?? all.first
        snapshot.selectedConversation = preferredItem
        selectedConversationID = preferredItem?.id ?? ""
        if selectedConversationID.isEmpty {
            snapshot.calls = .empty
            snapshot.selectedCall = nil
            selectedCallID = ""
        }
        for group in snapshot.sidebar.groups where !group.historyCursor.isEmpty {
            workspaceCursors[group.id] = group.historyCursor
        }
    }

    private func loadSelection(generation: Int) {
        detailTask?.cancel()
        streamTask?.cancel()
        receiptTask?.cancel()
        guard let selected = snapshot.selectedConversation, !selected.id.isEmpty else {
            snapshot.calls = .empty
            snapshot.selectedCall = nil
            notify()
            return
        }
        let conversationID = selected.id
        snapshot.message = "正在读取对话详情…"
        notify()

        detailTask = Task { [weak self] in
            guard let self else { return }
            var warnings = [String]()
            do {
                async let conversationResult = client.conversation(conversationID)
                async let callsResult = client.calls(
                    conversationID: conversationID,
                    unattributed: selected.unattributed,
                    limit: 100
                )
                let (conversationJSON, callPage) = try await (conversationResult, callsResult)
                try Task.checkCancellation()
                guard generation == refreshGeneration, conversationID == selectedConversationID else { return }

                let conversationValue = conversationJSON["conversation"].isNull ? conversationJSON : conversationJSON["conversation"]
                var detailed = WorkbenchConversation(json: conversationValue, serverNow: snapshot.sidebar.serverNow)
                // List projections carry counters that detail objects may omit.
                if detailed.runningCount == 0 { detailed.runningCount = selected.runningCount }
                if detailed.pendingCount == 0 { detailed.pendingCount = selected.pendingCount }
                if detailed.lastActivityAt == nil { detailed.lastActivityAt = selected.lastActivityAt }
                if detailed.lastToolCallAt == nil { detailed.lastToolCallAt = selected.lastToolCallAt }
                if detailed.insertionEligible == nil { detailed.insertionEligible = selected.insertionEligible }
                snapshot.selectedConversation = detailed
                snapshot.calls = callPage
                if let existing = callPage.calls.first(where: { $0.id == selectedCallID }) ?? callPage.calls.first {
                    selectedCallID = existing.id
                    snapshot.selectedCall = existing
                } else {
                    selectedCallID = ""
                    snapshot.selectedCall = nil
                }

                do {
                    snapshot.permission = try await client.permission(
                        conversationID: conversationID,
                        workspaceID: detailed.workspaceID
                    )
                } catch let error as WorkbenchClientError where error.capabilityUnavailable {
                    snapshot.permission = nil
                    warnings.append("权限接口不可用")
                } catch {
                    warnings.append("权限读取失败")
                }

                do {
                    snapshot.insertions = try await client.insertions(conversationID: conversationID)
                } catch let error as WorkbenchClientError where error.capabilityUnavailable {
                    snapshot.insertions = .empty
                    warnings.append("插入接口不可用")
                } catch {
                    warnings.append("插入队列读取失败")
                }

                if !detailed.activeTaskID.isEmpty {
                    do {
                        async let taskJSON = client.task(detailed.activeTaskID)
                        async let threadJSON = client.taskThreads(detailed.activeTaskID)
                        let (task, threads) = try await (taskJSON, threadJSON)
                        snapshot.task = WorkbenchTaskSummary(json: task, threads: threads)
                    } catch let error as WorkbenchClientError where error.capabilityUnavailable {
                        snapshot.task = nil
                        warnings.append("任务详情接口不可用")
                    } catch {
                        snapshot.task = nil
                        warnings.append("任务详情读取失败")
                    }
                } else {
                    snapshot.task = nil
                }

                guard generation == refreshGeneration, conversationID == selectedConversationID else { return }
                snapshot.stale = false
                snapshot.lastLoadedAt = Date()
                snapshot.message = warnings.isEmpty ? connectedSummary(snapshot.sidebar) : warnings.joined(separator: " · ")
                notify()
                if !selectedCallID.isEmpty {
                    let callID = selectedCallID
                    selectedCallID = ""
                    selectCall(callID)
                }
                startStream(conversationID: conversationID, unattributed: detailed.unattributed)
            } catch is CancellationError {
                return
            } catch {
                guard generation == refreshGeneration, conversationID == selectedConversationID else { return }
                snapshot.stale = snapshot.lastLoadedAt != nil
                snapshot.message = "对话详情读取失败：\(error.localizedDescription)"
                notify()
            }
        }
    }

    private func startStream(conversationID: String, unattributed: Bool) {
        streamTask?.cancel()
        lastStreamEventID = snapshot.calls.latestSequence > 0 ? String(snapshot.calls.latestSequence) : ""
        streamBackoffSeconds = 1
        streamTask = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled, conversationID == selectedConversationID {
                let path = client.callStreamPath(
                    conversationID: conversationID,
                    unattributed: unattributed,
                    after: UInt64(lastStreamEventID)
                )
                do {
                    for try await event in client.eventStream(path: path, lastEventID: lastStreamEventID) {
                        try Task.checkCancellation()
                        guard conversationID == selectedConversationID else { return }
                        if !event.id.isEmpty { lastStreamEventID = event.id }
                        switch event.name {
                        case "call":
                            let call = WorkbenchCall(json: event.data)
                            upsertCall(call)
                            streamBackoffSeconds = 1
                            notify()
                        case "cursor":
                            let sequence = event.data.unsigned("seq")
                            snapshot.calls.latestSequence = max(snapshot.calls.latestSequence, sequence)
                        case "reset", "gap":
                            snapshot.message = event.name == "reset" ? "活动流游标已重置，正在重新同步…" : "活动历史存在保留缺口，正在重新同步…"
                            notify()
                            refresh(reason: "stream-reset")
                            return
                        case "warning":
                            snapshot.message = event.data.firstText("reason", "message")
                            notify()
                        default:
                            break
                        }
                    }
                    if Task.isCancelled { return }
                } catch is CancellationError {
                    return
                } catch let error as WorkbenchClientError where !error.retryable {
                    snapshot.message = "活动流不可用：\(error.localizedDescription)"
                    notify()
                    return
                } catch {
                    snapshot.stale = true
                    snapshot.message = "活动流已断开，\(streamBackoffSeconds) 秒后重连：\(error.localizedDescription)"
                    notify()
                }
                do {
                    try await Task.sleep(nanoseconds: streamBackoffSeconds * 1_000_000_000)
                } catch {
                    return
                }
                streamBackoffSeconds = min(16, streamBackoffSeconds * 2)
            }
        }
    }

    private func upsertCall(_ call: WorkbenchCall) {
        guard call.conversationID.isEmpty || call.conversationID == selectedConversationID else { return }
        if let index = snapshot.calls.calls.firstIndex(where: { $0.id == call.id }) {
            snapshot.calls.calls[index] = call
        } else {
            snapshot.calls.calls.insert(call, at: 0)
        }
        snapshot.calls.calls.sort {
            if $0.sequence == $1.sequence { return $0.id > $1.id }
            return $0.sequence > $1.sequence
        }
        snapshot.calls.latestSequence = max(snapshot.calls.latestSequence, call.sequence)
        if call.id == selectedCallID { snapshot.selectedCall = call }
    }

    private func performOperation(
        successMessage: String,
        refreshAfter: Bool = true,
        operation: @escaping @MainActor () async throws -> Void
    ) {
        guard !isOperating else { return }
        isOperating = true
        snapshot.message = "正在执行操作…"
        notify()
        Task { [weak self] in
            guard let self else { return }
            do {
                try await operation()
                isOperating = false
                snapshot.message = successMessage
                notify()
                if refreshAfter { refresh(reason: "operation") }
            } catch is CancellationError {
                isOperating = false
                notify()
            } catch {
                isOperating = false
                snapshot.message = "操作失败：\(error.localizedDescription)"
                notify()
            }
        }
    }

    private func waitForInsertionReceipt(conversationID: String, submissionID: String, insertionID: String) async {
        let deadline = Date().addingTimeInterval(30)
        while Date() < deadline, !Task.isCancelled, conversationID == selectedConversationID {
            do {
                let page = try await client.insertions(conversationID: conversationID)
                snapshot.insertions = page
                let item = page.items.first {
                    (!insertionID.isEmpty && $0.id == insertionID)
                        || $0.raw.text("submission_id") == submissionID
                }
                if let item {
                    let terminal = ["acknowledged", "received", "completed", "failed", "expired", "cancelled", "canceled"].contains(item.status)
                        || ["acknowledged", "received", "confirmed", "committed"].contains(item.receiptState)
                    snapshot.message = terminal ? "补充状态：\(item.detailText)" : "补充仍在等待回执：\(item.detailText)"
                    notify()
                    if terminal { return }
                }
            } catch {
                snapshot.message = "补充已提交，但回执查询失败：\(error.localizedDescription)"
                notify()
            }
            do {
                try await Task.sleep(nanoseconds: 1_500_000_000)
            } catch {
                return
            }
        }
        guard conversationID == selectedConversationID, !Task.isCancelled else { return }
        snapshot.message = "30 秒内未取得持久化回执；状态保持待确认，不视为送达。"
        notify()
    }

    private func notify() {
        onChange?(self)
    }
}
