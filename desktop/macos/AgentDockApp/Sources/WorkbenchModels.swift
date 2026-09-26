import Foundation

enum WorkbenchListView: String, CaseIterable, Sendable {
    case active
    case archived
    case trash
    case all

    var title: String {
        switch self {
        case .active: return "对话"
        case .archived: return "归档"
        case .trash: return "回收站"
        case .all: return "全部"
        }
    }
}

enum WorkbenchTheme: String, CaseIterable, Sendable {
    case system
    case light
    case dark

    var title: String {
        switch self {
        case .system: return "跟随系统"
        case .light: return "浅色"
        case .dark: return "深色"
        }
    }
}

struct WorkbenchOverview: Equatable, Sendable {
    var schemaVersion: Int
    var serverNow: Date?
    var permissionMode: String
    var policyRevision: UInt64
    var running: Int
    var pending: Int
    var unknown: Int
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON) {
        let statistics = json["statistics"]
        schemaVersion = Int(json.integer("schema_version"))
        serverNow = json.date("server_now")
        permissionMode = json.text("permission_mode", fallback: "unknown")
        policyRevision = json.unsigned("policy_revision")
        running = Int(statistics.integer("running"))
        pending = Int(statistics.integer("pending"))
        unknown = Int(statistics.integer("unknown"))
        raw = json
    }
}

struct WorkbenchConversation: Equatable, Identifiable, Sendable {
    let id: String
    var title: String
    var source: String
    var workspaceID: String
    var taskIDs: [String]
    var activeTaskID: String
    var activeThreadID: String
    var bindingRevision: UInt64?
    var tags: [String]
    var pinned: Bool
    var archived: Bool
    var trashed: Bool
    var terminated: Bool
    var unattributed: Bool
    var inFlight: Bool
    var lastActivityAt: Date?
    var lastInteractionAt: Date?
    var interactionExpiresAt: Date?
    var lastToolCallAt: Date?
    var recentlyActive: Bool
    var runningCount: Int
    var pendingCount: Int
    var insertionEligible: Bool?
    var stopEligible: Bool?
    var insertionEligibilityReason: String
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON, serverNow: Date?) {
        id = json.firstText("conversation_id", "id")
        let created = json.date("created_at")
        let providedTitle = json.text("title").trimmingCharacters(in: .whitespacesAndNewlines)
        if providedTitle.isEmpty || providedTitle == "新对话" {
            title = "对话 · " + WorkbenchFormatting.shortDate(created)
        } else {
            title = providedTitle
        }
        source = json.text("source")
        let state = json["state"]
        workspaceID = state.text("workspace_id")
        if workspaceID.isEmpty { workspaceID = json.strings("workspace_ids").first ?? "" }
        taskIDs = json.strings("task_ids")
        activeTaskID = state.text("active_task_id")
        activeThreadID = state.text("active_task_thread_id", fallback: "main")
        bindingRevision = state["binding_revision"].uint64Value
        tags = json.strings("tags")
        pinned = json.flag("pinned")
        archived = !json["archived_at"].isNull
        trashed = !json["trashed_at"].isNull
        terminated = !json["terminated_at"].isNull
        unattributed = json.flag("is_unattributed")
        inFlight = json.flag("in_flight")
        let statistics = json["statistics"]
        lastToolCallAt = statistics.date("last_tool_call_at")
        lastActivityAt = json.date("last_activity_at")
            ?? statistics.date("last_activity_at")
            ?? lastToolCallAt
        runningCount = Int(statistics.integer("running"))
        pendingCount = Int(statistics.integer("pending"))
        // WB04: process output is not a new user/model interaction. A live
        // execution is displayed independently and never extends the 120s dot.
        lastInteractionAt = json.date("last_interaction_at") ?? lastToolCallAt
        interactionExpiresAt = json.date("interaction_expires_at")
            ?? lastInteractionAt?.addingTimeInterval(120)
        recentlyActive = json.optionalFlag("recently_active")
            ?? Self.isRecentlyActive(lastActivityAt: lastInteractionAt, serverNow: serverNow, inFlight: false)
        let explicitInsertionEligibility = json.firstField([
            "insertion_eligible",
            "can_insert",
            "insertion.eligible",
            "insertion_state.eligible",
            "eligibility.can_insert"
        ]).boolValue
        if let explicitInsertionEligibility {
            insertionEligible = explicitInsertionEligibility
        } else if let serverNow, let lastToolCallAt, lastToolCallAt <= serverNow, !terminated, !trashed {
            // This is intentionally independent from the 120-second activity
            // indicator. Core still validates the action authoritatively.
            insertionEligible = serverNow.timeIntervalSince(lastToolCallAt) < 180
        } else {
            insertionEligible = nil
        }
        stopEligible = json.firstField([
            "stop_eligible",
            "can_stop",
            "insertion.can_stop",
            "eligibility.can_stop"
        ]).boolValue
        insertionEligibilityReason = json.firstField([
            "insertion_eligibility_reason",
            "insertion.reason",
            "insertion_state.reason",
            "eligibility.reason"
        ]).stringValue ?? ""
        raw = json
    }

    static func isRecentlyActive(lastActivityAt: Date?, serverNow: Date?, inFlight: Bool) -> Bool {
        guard let lastActivityAt, let serverNow, lastActivityAt <= serverNow else { return false }
        return serverNow.timeIntervalSince(lastActivityAt) < 120
    }

    /// Navigation identity is not a Runtime conversation ID.
    var navigationID: String { unattributed ? "unattributed" : id }

    mutating func advancePresentation(serverNow: Date) {
        if let expiry = interactionExpiresAt, let interaction = lastInteractionAt {
            recentlyActive = interaction <= serverNow && serverNow < expiry
        } else {
            recentlyActive = false
        }
        if let request = lastToolCallAt {
            insertionEligible = !terminated && !trashed && !unattributed
                && request <= serverNow && serverNow.timeIntervalSince(request) < 180
                && raw.firstField(["insertion_eligible", "can_insert"]).boolValue != false
        }
    }

    var stateText: String {
        if terminated { return "已终止" }
        if trashed { return "回收站" }
        if archived { return "已归档" }
        if inFlight { return "正在执行" }
        if recentlyActive { return "活动中" }
        return source.isEmpty ? "历史对话" : source
    }

    var metadataText: String {
        var values = [stateText]
        if runningCount > 0 { values.append("\(runningCount) 运行中") }
        if pendingCount > 0 { values.append("\(pendingCount) 待审批") }
        if let lastActivityAt { values.append(WorkbenchFormatting.relative(lastActivityAt)) }
        return values.joined(separator: " · ")
    }
}

struct WorkbenchWorkspaceGroup: Equatable, Identifiable, Sendable {
    let id: String
    var title: String
    var root: String
    var total: Int
    var recentCount: Int
    var shown: Int
    var mode: String
    var hasMore: Bool
    var historyCursor: String
    var historyReset: Bool
    var conversations: [WorkbenchConversation]

    init(json: WorkbenchJSON, serverNow: Date?) {
        id = json.text("workspace_id", fallback: "unassigned")
        title = json.text("title", fallback: "历史工作区")
        root = json.text("root")
        total = Int(json.integer("total"))
        recentCount = Int(json.integer("recent_count"))
        shown = Int(json.integer("shown"))
        mode = json.text("mode", fallback: "auto")
        hasMore = json.flag("has_more")
        historyCursor = json.text("history_cursor")
        historyReset = json.flag("history_reset")
        conversations = json.values("conversations").map { WorkbenchConversation(json: $0, serverNow: serverNow) }
    }
}

struct WorkbenchSidebarPage: Equatable, Sendable {
    var serverNow: Date?
    var latestSequence: UInt64
    var total: Int
    var groups: [WorkbenchWorkspaceGroup]
    var selected: WorkbenchConversation?
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON) {
        serverNow = json.date("server_now")
        latestSequence = json.unsigned("latest_seq")
        total = Int(json.integer("total"))
        groups = json.values("groups").map { WorkbenchWorkspaceGroup(json: $0, serverNow: serverNow) }
        selected = json["selected"].isNull ? nil : WorkbenchConversation(json: json["selected"], serverNow: serverNow)
        raw = json
    }

    static let empty = WorkbenchSidebarPage(json: .object([
        "groups": .array([]),
        "latest_seq": .integer(0),
        "total": .integer(0)
    ]))
}

struct WorkbenchCall: Equatable, Identifiable, Sendable {
    let id: String
    var conversationID: String
    var taskID: String
    var threadID: String
    var parentCallID: String
    var approvalID: String
    var toolName: String
    var title: String
    var kind: String
    var status: String
    var sequence: UInt64
    var createdAt: Date?
    var updatedAt: Date?
    var command: String
    var workdir: String
    var summary: String
    var requestText: String
    var responseText: String
    var progressText: String
    var timingText: String
    var fileEditText: String
    var isInsertion: Bool
    var canStop: Bool
    var needsApproval: Bool
    var canRetry: Bool
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON) {
        let insertionID = json.text("insertion_id")
        let callID = json.text("call_id")
        id = insertionID.isEmpty ? callID : insertionID
        conversationID = json.text("conversation_id")
        taskID = json.text("task_id")
        threadID = json.text("thread_id")
        parentCallID = json.text("parent_call_id")
        approvalID = json.text("approval_id")
        toolName = json.text("tool_name")
        kind = json.text("kind")
        status = json.text("status", fallback: "unknown")
        sequence = max(json.unsigned("updated_seq"), json.unsigned("created_seq"))
        createdAt = json.date("created_at") ?? json.date("started_at")
        updatedAt = json.date("last_activity_at") ?? json.date("completed_at")
        command = json.text("display_command")
        workdir = json.text("workdir")
        summary = json.firstText("summary", "message")
        isInsertion = !insertionID.isEmpty || kind.hasPrefix("insertion.") || toolName == "conversation.insertion"
        let originalTitle = json.firstText("display_title", "activity_label", "title")
        if isInsertion {
            title = "用户补充"
        } else if !originalTitle.isEmpty {
            title = originalTitle
        } else if !toolName.isEmpty {
            title = WorkbenchFormatting.toolTitle(toolName)
        } else {
            title = "执行记录"
        }
        requestText = Self.payloadText(json["request"])
        let response = json["output_source"].text("ref").isEmpty ? json["response"] : json["output_source"]
        responseText = Self.payloadText(response)
        let stdout = json.text("output_preview")
        let stderr = json.text("stderr_preview")
        progressText = stdout + (stderr.isEmpty ? "" : (stdout.isEmpty ? "" : "\n\n") + "[stderr]\n" + stderr)
        if responseText.isEmpty { responseText = progressText }
        timingText = Self.timing(json)
        fileEditText = Self.fileEdit(json["file_edit"])
        canStop = !isInsertion && ["created", "running", "pending_approval"].contains(status)
        needsApproval = !isInsertion && status == "pending_approval" && !approvalID.isEmpty
        canRetry = !isInsertion && ["failed", "cancelled"].contains(status)
        raw = json
    }

    private static func payloadText(_ value: WorkbenchJSON) -> String {
        if value.isNull { return "" }
        if let text = value.stringValue { return text }
        if let inline = value.optionalText("inline") { return inline }
        if let text = value.optionalText("text") { return text }
        if let preview = value.optionalText("preview") { return preview }
        if let reference = value.optionalText("ref") {
            let bytes = value.integer("bytes")
            return bytes > 0 ? "外部载荷：\(reference) · \(bytes) bytes" : "外部载荷：\(reference)"
        }
        return value.prettyPrinted
    }

    private static func timing(_ value: WorkbenchJSON) -> String {
        let rows: [(String, Int64)] = [
            ("RPC", value.integer("rpc_elapsed_ms", fallback: -1)),
            ("执行", value.integer("execution_elapsed_ms", fallback: -1)),
            ("等待", value.integer("wait_elapsed_ms", fallback: -1)),
            ("操作", value.integer("operation_elapsed_ms", fallback: -1)),
            ("进程", value.integer("process_elapsed_ms", fallback: -1)),
            ("总计", value.integer("elapsed_ms", fallback: -1))
        ]
        let available = rows.filter { $0.1 >= 0 }
        if available.isEmpty { return "耗时未记录。" }
        return available.map { "\($0.0)：\(String(format: "%.3f s", Double($0.1) / 1000))" }.joined(separator: "\n")
    }

    private static func fileEdit(_ value: WorkbenchJSON) -> String {
        guard !value.isNull else { return "文件修改详情未记录。" }
        var lines = [String]()
        let action = value.text("action")
        let path = value.text("path")
        if !action.isEmpty { lines.append("操作：\(action)") }
        if !path.isEmpty { lines.append("目标：\(path)") }
        if let changed = value.optionalFlag("changed") { lines.append("实际修改：\(changed ? "是" : "否")") }
        if value.optionalText("stats_state") != nil { lines.append("统计：\(value.text("stats_state"))") }
        if !value["insertions"].isNull || !value["deletions"].isNull {
            lines.append("新增/删除：\(value.integer("insertions")) / \(value.integer("deletions"))")
        }
        for file in value.values("affected_files") {
            lines.append("• \(file.text("path"))  +\(file.integer("insertions")) −\(file.integer("deletions"))")
        }
        let diff = value.text("diff_preview")
        if !diff.isEmpty { lines.append("\n" + diff) }
        return lines.isEmpty ? value.prettyPrinted : lines.joined(separator: "\n")
    }

    var stateText: String { WorkbenchFormatting.state(status) }

    var metadataText: String {
        var values = [stateText]
        if let createdAt { values.append(WorkbenchFormatting.clock(createdAt)) }
        if !toolName.isEmpty { values.append(toolName) }
        return values.joined(separator: " · ")
    }
}

struct WorkbenchCallPage: Equatable, Sendable {
    var calls: [WorkbenchCall]
    var hasMore: Bool
    var nextBefore: UInt64
    var latestSequence: UInt64
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON) {
        calls = json.values("calls").map(WorkbenchCall.init)
        hasMore = json.flag("has_more")
        nextBefore = json.unsigned("next_before")
        latestSequence = max(json.unsigned("latest_seq"), json.unsigned("cursor"))
        raw = json
    }

    static let empty = WorkbenchCallPage(json: .object(["calls": .array([])]))
}

struct WorkbenchTaskSummary: Equatable, Sendable {
    var id: String
    var title: String
    var status: String
    var outcome: String
    var summary: String
    var workspaceID: String
    var activeThreadID: String
    var steps: [String]
    var conditions: [String]
    var review: String
    var threads: [String]
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON, threads threadJSON: WorkbenchJSON = .null) {
        let task = json["task"].isNull ? json : json["task"]
        id = task.firstText("id", "task_id")
        title = task.text("title", fallback: "任务")
        status = task.text("status", fallback: "unknown")
        outcome = task.text("outcome")
        summary = task.text("summary")
        workspaceID = task.text("workspace_id")
        activeThreadID = task.text("active_thread_id", fallback: "main")
        steps = task.values("steps").map { step in
            let title = step.text("title", fallback: step.prettyPrinted)
            return "\(WorkbenchFormatting.state(step.text("status")))  \(title)"
        }
        let conditionValues = task.values("conditions").isEmpty ? task.values("completion_conditions") : task.values("conditions")
        conditions = conditionValues.map { $0.stringValue ?? $0.text("text", fallback: $0.prettyPrinted) }
        let finalReview = task["final_review"]
        review = [finalReview.text("status"), finalReview.text("summary")].filter { !$0.isEmpty }.joined(separator: " · ")
        threads = threadJSON.values("threads").map { thread in
            let title = thread.text("title", fallback: thread.firstText("id", "thread_id"))
            return "\(title) · \(WorkbenchFormatting.state(thread.text("status")))"
        }
        raw = json
    }

    var detailText: String {
        var sections = [String]()
        sections.append("状态：\(WorkbenchFormatting.state(status))")
        if !outcome.isEmpty { sections.append("结果：\(outcome)") }
        if !summary.isEmpty { sections.append("\n\(summary)") }
        if !steps.isEmpty { sections.append("\n步骤\n" + steps.joined(separator: "\n")) }
        if !conditions.isEmpty { sections.append("\n验收条件\n" + conditions.map { "• \($0)" }.joined(separator: "\n")) }
        if !threads.isEmpty { sections.append("\n分支\n" + threads.joined(separator: "\n")) }
        if !review.isEmpty { sections.append("\n复核\n\(review)") }
        return sections.joined(separator: "\n")
    }
}

struct WorkbenchPermissionState: Equatable, Sendable {
    var mode: String
    var scope: String
    var scopeID: String
    var revision: UInt64
    var customSettingsEnabled: Bool?
    var settings: WorkbenchJSON
    var configuredSettings: WorkbenchJSON
    var settingsSource: String
    var workspaces: [String]
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON) {
        let effective = json["effective"].isNull ? json : json["effective"]
        mode = effective.text("mode", fallback: json["policy"].text("global_mode", fallback: "unknown"))
        scope = effective.text("scope", fallback: "global")
        scopeID = effective.text("scope_id")
        revision = max(effective.unsigned("revision"), json["policy"].unsigned("revision"))
        settings = effective["settings"]
        configuredSettings = effective["configured_settings"].isNull ? settings : effective["configured_settings"]
        settingsSource = effective.text("settings_source", fallback: "legacy")
        customSettingsEnabled = effective.firstField([
            "custom_permissions_enabled",
            "custom_settings_enabled",
            "settings.custom_enabled",
            "settings.enabled"
        ]).boolValue
        workspaces = json.values("workspaces").map { $0.firstText("name", "title", "workspace_id") }.filter { !$0.isEmpty }
        raw = json
    }

    var summaryText: String {
        var values = ["模式：\(WorkbenchFormatting.permissionMode(mode))", "范围：\(scope)"]
        if !scopeID.isEmpty { values.append("对象：\(scopeID)") }
        values.append("修订：\(revision) · 来源：\(settingsSource)")
        if let customSettingsEnabled { values.append("自定义权限设置：\(customSettingsEnabled ? "已启用" : "未启用")") }
        return values.joined(separator: "\n")
    }
}

struct WorkbenchInsertion: Equatable, Identifiable, Sendable {
    let id: String
    var text: String
    var status: String
    var attempts: Int
    var receiptState: String
    var receiptType: String
    var manualRetryAvailable: Bool
    var automaticAttemptsRemaining: Int?
    var totalAttemptsRemaining: Int?
    var nextRetryAt: Date?
    var terminalReason: String
    var createdAt: Date?
    var expiresAt: Date?
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON) {
        id = json.firstText("insertion_id", "id")
        text = json.firstText("text", "message")
        status = json.text("status", fallback: "unknown")
        attempts = Int(max(json.integer("attempts"), json.integer("delivery_attempts")))
        receiptState = json.firstText("receipt_state", "ack_state", "delivery_state")
        receiptType = json.text("receipt_type")
        manualRetryAvailable = json.flag("manual_retry_available")
        automaticAttemptsRemaining = json["automatic_attempts_remaining"].int64Value.map(Int.init)
        totalAttemptsRemaining = json["total_attempts_remaining"].int64Value.map(Int.init)
        nextRetryAt = json.date("next_retry_at")
        terminalReason = json.firstText("delivery_reason", "terminal_reason", "reason", "error")
        createdAt = json.date("created_at")
        expiresAt = json.date("expires_at") ?? json.date("deadline")
        raw = json
    }

    var terminal: Bool {
        ["acknowledged", "failed", "expired", "cancelled", "canceled"].contains(status)
    }

    var receiptDescription: String {
        switch receiptType {
        case "receiver_receipt": return "接收方回执（不等同模型上下文确认）"
        case "outer_forwarded": return "宿主已转发（未确认模型上下文）"
        case "host_context_committed": return "宿主已确认写入模型上下文（不代表执行完成）"
        case "": return "回执待确认"
        default: return "未知回执类型：\(receiptType)"
        }
    }

    var detailText: String {
        var values = [WorkbenchFormatting.state(status), receiptDescription]
        if !receiptState.isEmpty { values.append("回执：\(receiptState)") }
        if attempts > 0 { values.append("尝试：\(attempts)") }
        if let automaticAttemptsRemaining { values.append("自动余量：\(automaticAttemptsRemaining)") }
        if let totalAttemptsRemaining { values.append("总余量：\(totalAttemptsRemaining)") }
        if let nextRetryAt { values.append("可重投时间：\(WorkbenchFormatting.clock(nextRetryAt))") }
        if let expiresAt { values.append("到期：\(WorkbenchFormatting.clock(expiresAt))") }
        if !terminalReason.isEmpty { values.append(terminalReason) }
        return values.joined(separator: " · ")
    }
}

struct WorkbenchInsertionPage: Equatable, Sendable {
    var serverNow: Date?
    var items: [WorkbenchInsertion]
    var raw: WorkbenchJSON

    init(json: WorkbenchJSON) {
        serverNow = json.date("server_now")
        items = json.values("insertions").map(WorkbenchInsertion.init)
        raw = json
    }

    static let empty = WorkbenchInsertionPage(json: .object(["insertions": .array([])]))
}

struct WorkbenchSnapshot: Equatable, Sendable {
    var overview: WorkbenchOverview?
    var sidebar: WorkbenchSidebarPage = .empty
    var selectedConversation: WorkbenchConversation?
    var calls: WorkbenchCallPage = .empty
    var selectedCall: WorkbenchCall?
    var task: WorkbenchTaskSummary?
    var permission: WorkbenchPermissionState?
    var insertions: WorkbenchInsertionPage = .empty
    var stale: Bool = false
    var message: String = ""
    var lastLoadedAt: Date?

    static func fixture() -> WorkbenchSnapshot {
        let server = Date(timeIntervalSince1970: 1_800_000_000)
        let workspace = WorkbenchWorkspaceGroup(
            id: "wsp_fixture",
            title: "AgentDock Workbench",
            root: "~/AgentDock/workbench",
            total: 3,
            recentCount: 2,
            shown: 3,
            mode: "history",
            hasMore: false,
            historyCursor: "fixture",
            historyReset: false,
            conversations: [
                WorkbenchConversation.fixture(id: "conv_active", title: "macOS 原生 Workbench 完整对齐", state: "running", pinned: true, active: true, server: server),
                WorkbenchConversation.fixture(id: "conv_permissions", title: "权限设置与首次安装默认值", state: "pending", pinned: false, active: true, server: server),
                WorkbenchConversation.fixture(id: "conv_history", title: "安装速度与回退版本优化", state: "completed", pinned: false, active: false, server: server)
            ]
        )
        let sidebarJSON: WorkbenchJSON = .object([
            "server_now": .string(WorkbenchFormatting.iso(server)),
            "latest_seq": .integer(42),
            "total": .integer(3),
            "groups": .array([])
        ])
        var sidebar = WorkbenchSidebarPage(json: sidebarJSON)
        sidebar.groups = [workspace]
        sidebar.selected = workspace.conversations[0]
        let calls = [
            WorkbenchCall.fixture(id: "call_1", title: "读取完整任务与接口契约", tool: "files.read", status: "succeeded", sequence: 40, output: "已读取三份完整文档并锁定文件边界。"),
            WorkbenchCall.fixture(id: "call_2", title: "构建原生三栏窗口", tool: "file_edit", status: "running", sequence: 41, output: "AppKit navigation, timeline and detail panes"),
            WorkbenchCall.fixture(id: "call_3", title: "验证候选包", tool: "github.actions", status: "pending_approval", sequence: 42, output: "等待 Actions 原生架构验证")
        ]
        let callPageJSON: WorkbenchJSON = .object(["calls": .array([])])
        var page = WorkbenchCallPage(json: callPageJSON)
        page.calls = calls
        page.latestSequence = 42
        let permission = WorkbenchPermissionState(json: .object([
            "effective": .object([
                "mode": .string("full"),
                "scope": .string("workspace"),
                "scope_id": .string("wsp_fixture"),
                "revision": .integer(7),
                "custom_settings_enabled": .bool(false),
                "settings": .object([:])
            ])
        ]))
        let insertion = WorkbenchInsertion(json: .object([
            "insertion_id": .string("ins_fixture"),
            "submission_id": .string("macos-fixture"),
            "text": .string("继续执行，并在 Actions 中保留 Intel 与 Apple Silicon 的真实架构证据。"),
            "status": .string("queued"),
            "receipt_state": .string("pending"),
            "attempts": .integer(1),
            "created_at": .string("2027-01-15T08:00:30Z"),
            "expires_at": .string("2027-01-15T08:05:30Z")
        ]))
        var insertionPage = WorkbenchInsertionPage.empty
        insertionPage.items = [insertion]
        insertionPage.serverNow = server
        return WorkbenchSnapshot(
            overview: nil,
            sidebar: sidebar,
            selectedConversation: workspace.conversations[0],
            calls: page,
            selectedCall: calls[1],
            task: WorkbenchTaskSummary.fixture(),
            permission: permission,
            insertions: insertionPage,
            stale: false,
            message: "已连接 · 2 个活动对话",
            lastLoadedAt: server
        )
    }
}

private extension WorkbenchConversation {
    static func fixture(id: String, title: String, state: String, pinned: Bool, active: Bool, server: Date) -> WorkbenchConversation {
        let activity = active ? server.addingTimeInterval(-45) : server.addingTimeInterval(-7200)
        return WorkbenchConversation(json: .object([
            "conversation_id": .string(id),
            "title": .string(title),
            "source": .string("ChatGPT"),
            "pinned": .bool(pinned),
            "in_flight": .bool(state == "running"),
            "last_activity_at": .string(WorkbenchFormatting.iso(activity)),
            "state": .object([
                "workspace_id": .string("wsp_fixture"),
                "active_task_id": .string("tsk_fixture"),
                "active_task_thread_id": .string("main"),
                "binding_revision": .integer(3)
            ]),
            "statistics": .object(["running": .integer(state == "running" ? 1 : 0), "pending": .integer(state == "pending" ? 1 : 0)])
        ]), serverNow: server)
    }
}

private extension WorkbenchWorkspaceGroup {
    init(id: String, title: String, root: String, total: Int, recentCount: Int, shown: Int, mode: String, hasMore: Bool, historyCursor: String, historyReset: Bool, conversations: [WorkbenchConversation]) {
        self.id = id
        self.title = title
        self.root = root
        self.total = total
        self.recentCount = recentCount
        self.shown = shown
        self.mode = mode
        self.hasMore = hasMore
        self.historyCursor = historyCursor
        self.historyReset = historyReset
        self.conversations = conversations
    }
}

private extension WorkbenchCall {
    static func fixture(id: String, title: String, tool: String, status: String, sequence: Int64, output: String) -> WorkbenchCall {
        WorkbenchCall(json: .object([
            "call_id": .string(id),
            "conversation_id": .string("conv_active"),
            "display_title": .string(title),
            "tool_name": .string(tool),
            "status": .string(status),
            "updated_seq": .integer(sequence),
            "created_at": .string("2027-01-15T08:00:00Z"),
            "request": .object(["text": .string("执行：\(title)")]),
            "response": .object(["text": .string(output)]),
            "output_preview": .string(output),
            "rpc_elapsed_ms": .integer(status == "running" ? 1834 : 842)
        ]))
    }
}

private extension WorkbenchTaskSummary {
    static func fixture() -> WorkbenchTaskSummary {
        WorkbenchTaskSummary(json: .object([
            "task": .object([
                "task_id": .string("tsk_fixture"),
                "title": .string("WB06 macOS 原生 Workbench 完整对齐"),
                "status": .string("active"),
                "summary": .string("保留 Swift/AppKit，并用 Core Runtime API 对齐 Windows 的任务与执行中心。"),
                "steps": .array([
                    .object(["title": .string("功能矩阵与接口契约"), "status": .string("completed")]),
                    .object(["title": .string("原生窗口与数据客户端"), "status": .string("running")]),
                    .object(["title": .string("Actions 原生验证"), "status": .string("pending")])
                ]),
                "conditions": .array([.object(["text": .string("原生 AppKit 三栏界面")]), .object(["text": .string("Intel 与 Apple Silicon 真实 Actions 证据")])])
            ])
        ]))
    }
}

enum WorkbenchFormatting {
    static func state(_ value: String) -> String {
        switch value {
        case "created", "pending": return "未开始"
        case "running", "in_progress", "active": return "运行中"
        case "pending_approval": return "待审批"
        case "succeeded", "completed": return "已完成"
        case "failed": return "失败"
        case "partial": return "部分完成"
        case "cancelled", "canceled": return "已取消"
        case "blocked": return "受阻"
        case "unknown", "": return "结果待核对"
        default: return value
        }
    }

    static func permissionMode(_ value: String) -> String {
        switch value {
        case "full": return "完全权限"
        case "readonly", "read_only": return "只读"
        case "rules", "ask", "guarded", "default", "on-request": return "需要审批"
        default: return value.isEmpty ? "未知" : value
        }
    }

    static func toolTitle(_ value: String) -> String {
        let tail = value.split(separator: ".").last.map(String.init) ?? value
        return tail.replacingOccurrences(of: "_", with: " ").capitalized
    }

    static func shortDate(_ date: Date?) -> String {
        guard let date else { return "历史记录" }
        let formatter = DateFormatter()
        formatter.dateFormat = "MM-dd HH:mm"
        return formatter.string(from: date)
    }

    static func clock(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "HH:mm:ss"
        return formatter.string(from: date)
    }

    static func relative(_ date: Date, now: Date = Date()) -> String {
        let seconds = max(0, Int(now.timeIntervalSince(date)))
        if seconds < 60 { return "刚刚" }
        if seconds < 3600 { return "\(seconds / 60) 分钟前" }
        if seconds < 86400 { return "\(seconds / 3600) 小时前" }
        return "\(seconds / 86400) 天前"
    }

    static func iso(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.string(from: date)
    }
}
