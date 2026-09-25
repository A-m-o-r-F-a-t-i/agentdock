import Foundation

extension WorkbenchAPIClient {
    func overview() async throws -> WorkbenchOverview {
        WorkbenchOverview(json: try await get("/internal/runtime/execution"))
    }

    func sidebar(_ input: WorkbenchSidebarRequest) async throws -> WorkbenchSidebarPage {
        WorkbenchSidebarPage(json: try await post("/internal/runtime/execution/sidebar", body: input.json))
    }

    func conversation(_ id: String) async throws -> WorkbenchJSON {
        try await get("/internal/runtime/conversations/\(try encodedPathComponent(id))")
    }

    func calls(
        conversationID: String,
        unattributed: Bool = false,
        before: UInt64 = 0,
        after: UInt64? = nil,
        limit: Int = 100,
        includeOutput: Bool = false
    ) async throws -> WorkbenchCallPage {
        WorkbenchCallPage(json: try await get(callPath(
            resource: "/internal/runtime/calls",
            conversationID: conversationID,
            unattributed: unattributed,
            before: before,
            after: after,
            limit: limit,
            includeOutput: includeOutput
        )))
    }

    func callStreamPath(conversationID: String, unattributed: Bool = false, after: UInt64? = nil) -> String {
        callPath(
            resource: "/internal/runtime/calls/stream",
            conversationID: conversationID,
            unattributed: unattributed,
            before: 0,
            after: after,
            limit: 200,
            includeOutput: false
        )
    }

    func call(_ id: String) async throws -> WorkbenchCall {
        WorkbenchCall(json: try await get("/internal/runtime/calls/\(try encodedPathComponent(id))"))
    }

    func callEvents(_ id: String, after: UInt64 = 0, limit: Int = 200) async throws -> WorkbenchJSON {
        let path = "/internal/runtime/calls/\(try encodedPathComponent(id))/events"
        return try await get(queryPath(path, query: [
            URLQueryItem(name: "after", value: String(after)),
            URLQueryItem(name: "limit", value: String(min(max(limit, 1), 200)))
        ]))
    }

    func callPayload(_ id: String, source: String, offset: Int = 0, limit: Int = 64 * 1024) async throws -> WorkbenchJSON {
        guard source == "request" || source == "response" else {
            throw WorkbenchClientError.configuration("未知调用载荷来源：\(source)")
        }
        let path = "/internal/runtime/calls/\(try encodedPathComponent(id))/payload/\(source)"
        return try await get(queryPath(path, query: [
            URLQueryItem(name: "offset", value: String(max(0, offset))),
            URLQueryItem(name: "limit", value: String(min(max(limit, 1), 256 * 1024)))
        ]))
    }

    func task(_ id: String) async throws -> WorkbenchJSON {
        try await get("/internal/runtime/tasks/\(try encodedPathComponent(id))")
    }

    func taskThreads(_ id: String) async throws -> WorkbenchJSON {
        try await get("/internal/runtime/tasks/\(try encodedPathComponent(id))/threads")
    }

    func taskCalls(_ id: String, before: UInt64 = 0, limit: Int = 100) async throws -> WorkbenchCallPage {
        var query = [URLQueryItem(name: "limit", value: String(min(max(limit, 1), 200)))]
        if before > 0 { query.append(URLQueryItem(name: "before", value: String(before))) }
        return WorkbenchCallPage(json: try await get(queryPath(
            "/internal/runtime/tasks/\(try encodedPathComponent(id))/calls",
            query: query
        )))
    }

    func permission(conversationID: String = "", workspaceID: String = "") async throws -> WorkbenchPermissionState {
        var items = [URLQueryItem]()
        if !conversationID.isEmpty { items.append(URLQueryItem(name: "conversation_id", value: conversationID)) }
        if !workspaceID.isEmpty { items.append(URLQueryItem(name: "workspace_id", value: workspaceID)) }
        return WorkbenchPermissionState(json: try await get(queryPath("/internal/runtime/permissions/effective", query: items)))
    }

    func insertions(conversationID: String) async throws -> WorkbenchInsertionPage {
        WorkbenchInsertionPage(json: try await get("/internal/runtime/conversations/\(try encodedPathComponent(conversationID))/insertions"))
    }

    func pendingApprovals(offset: Int = 0, limit: Int = 100) async throws -> WorkbenchJSON {
        try await get(queryPath("/internal/runtime/approvals", query: [
            URLQueryItem(name: "status", value: "pending"),
            URLQueryItem(name: "offset", value: String(max(0, offset))),
            URLQueryItem(name: "limit", value: String(min(max(limit, 1), 200)))
        ]))
    }

    func connectionStatus() async throws -> WorkbenchJSON {
        try await get("/internal/runtime/execution/connection")
    }

    func displaySettings() async throws -> WorkbenchJSON {
        try await get("/internal/runtime/execution/display")
    }

    @discardableResult
    func updateDisplaySettings(_ change: WorkbenchJSON) async throws -> WorkbenchJSON {
        try await post("/internal/runtime/execution/display", body: change)
    }

    @discardableResult
    func manage(
        kind: String,
        ids: [String],
        action: String,
        title: String = "",
        tags: [String] = [],
        retentionDays: Int = 0,
        confirmPermanent: Bool = false
    ) async throws -> WorkbenchJSON {
        guard kind == "conversation" || kind == "task" || kind == "call" else {
            throw WorkbenchClientError.configuration("未知管理对象类型：\(kind)")
        }
        guard !ids.isEmpty, ids.count <= 200 else {
            throw WorkbenchClientError.configuration("批量操作必须包含 1–200 个明确对象。")
        }
        let endpoint = kind == "conversation" ? "conversations" : (kind == "task" ? "tasks" : "calls")
        return try await post("/internal/runtime/\(endpoint)/batch", body: .object([
            "ids": .stringArray(ids),
            "action": .string(action),
            "title": .string(title),
            "tags": .stringArray(tags),
            "retention_days": .integer(Int64(retentionDays)),
            "confirm_permanent": .bool(confirmPermanent)
        ]))
    }

    @discardableResult
    func conversationLifecycle(id: String, action: String) async throws -> WorkbenchJSON {
        guard action == "terminate" || action == "resume" else {
            throw WorkbenchClientError.configuration("未知对话生命周期操作：\(action)")
        }
        return try await post("/internal/runtime/conversations/\(try encodedPathComponent(id))/\(action)", body: .object([
            "confirm": .bool(true)
        ]))
    }

    @discardableResult
    func linkConversation(id: String, taskID: String) async throws -> WorkbenchJSON {
        try await post("/internal/runtime/conversations/\(try encodedPathComponent(id))/link-task", body: .object([
            "task_id": .string(taskID)
        ]))
    }

    @discardableResult
    func setCurrentTask(
        conversationID: String,
        taskID: String,
        threadID: String = "main",
        bindingRevision: UInt64
    ) async throws -> WorkbenchJSON {
        guard bindingRevision <= UInt64(Int64.max) else {
            throw WorkbenchClientError.configuration("对话绑定修订号超出客户端可编码范围。")
        }
        return try await post("/internal/runtime/conversations/\(try encodedPathComponent(conversationID))/current-task", body: .object([
            "task_id": .string(taskID),
            "task_thread_id": .string(threadID),
            "binding_revision": .integer(Int64(bindingRevision))
        ]))
    }

    @discardableResult
    func stopCall(_ id: String) async throws -> WorkbenchJSON {
        try await post("/internal/runtime/calls/\(try encodedPathComponent(id))/stop", body: .object([:]))
    }

    @discardableResult
    func decideApproval(_ id: String, action: String, allowWorkspace: Bool = false) async throws -> WorkbenchJSON {
        guard action == "approve" || action == "reject" else {
            throw WorkbenchClientError.configuration("未知审批操作：\(action)")
        }
        return try await post("/internal/runtime/approvals/\(try encodedPathComponent(id))/\(action)", body: .object([
            "allow_workspace": .bool(allowWorkspace)
        ]))
    }

    @discardableResult
    func updatePermission(_ change: WorkbenchJSON) async throws -> WorkbenchJSON {
        try await post("/internal/runtime/permissions", body: change)
    }

    @discardableResult
    func enqueueInsertion(conversationID: String, submissionID: String, text: String) async throws -> WorkbenchJSON {
        let normalized = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !normalized.isEmpty, normalized.utf8.count <= 16 * 1024 else {
            throw WorkbenchClientError.configuration("补充内容必须为 1–16384 bytes。")
        }
        return try await post("/internal/runtime/conversations/\(try encodedPathComponent(conversationID))/insertions", body: .object([
            "submission_id": .string(submissionID),
            "text": .string(normalized)
        ]))
    }

    @discardableResult
    func insertionAction(conversationID: String, insertionID: String, action: String) async throws -> WorkbenchJSON {
        guard action == "cancel" || action == "retry" else {
            throw WorkbenchClientError.configuration("未知插入操作：\(action)")
        }
        return try await post(
            "/internal/runtime/conversations/\(try encodedPathComponent(conversationID))/insertions/\(try encodedPathComponent(insertionID))/\(action)",
            body: .object([:])
        )
    }

    private func callPath(
        resource: String,
        conversationID: String,
        unattributed: Bool,
        before: UInt64,
        after: UInt64?,
        limit: Int,
        includeOutput: Bool
    ) -> String {
        var items = [URLQueryItem(name: "limit", value: String(min(max(limit, 1), 200)))]
        if !conversationID.isEmpty { items.append(URLQueryItem(name: "conversation_id", value: conversationID)) }
        if unattributed { items.append(URLQueryItem(name: "unattributed", value: "true")) }
        if before > 0 { items.append(URLQueryItem(name: "before", value: String(before))) }
        if let after { items.append(URLQueryItem(name: "after", value: String(after))) }
        if includeOutput { items.append(URLQueryItem(name: "include_output", value: "true")) }
        return queryPath(resource, query: items)
    }
}
