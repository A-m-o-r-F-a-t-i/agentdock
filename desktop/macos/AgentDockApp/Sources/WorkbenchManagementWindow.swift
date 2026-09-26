import AppKit

/// One reusable native manager for the existing Core resources. Pages replace
/// the prior page; selection is always a set of explicit server resource IDs.
@MainActor
final class WorkbenchManagementWindow: NSWindowController, NSWindowDelegate, NSTableViewDataSource, NSTableViewDelegate {
    private let client: WorkbenchAPIClient
    private let resourceControl = NSPopUpButton()
    private let filter = WorkbenchForms.popup(["active", "all", "archived", "trash"], selected: "active")
    private let search = NSSearchField()
    private let workspaceFilter = NSTextField(string: "")
    private let table = NSTableView()
    private let text = WorkbenchUI.scrollableText(monospaced: true)
    private let status = WorkbenchUI.label("", lines: 3)
    private let actions = NSPopUpButton()
    private let previous = NSButton(), next = NSButton()
    private var resource: WorkbenchResource = .tasks
    private var page: WorkbenchManagementPage?
    private var detail: WorkbenchJSON = .null
    private var offset = 0, generation = 0, selectionGeneration = 0
    private var offsets = [Int]()
    private var request: Task<Void, Never>?, detailRequest: Task<Void, Never>?, writeRequest: Task<Void, Never>?
    private var busy = false, stale = true
    var onConversation: ((String) -> Void)?

    init(client: WorkbenchAPIClient) {
        self.client = client
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1000, height: 700),
            styleMask: [.titled, .closable, .resizable, .miniaturizable], backing: .buffered, defer: false)
        window.title = "Workbench 管理中心"; window.isReleasedWhenClosed = false
        window.minSize = NSSize(width: 840, height: 560)
        super.init(window: window); window.delegate = self
        resourceControl.addItems(withTitles: WorkbenchResource.allCases.map(\.title))
        resourceControl.target = self; resourceControl.action = #selector(changeResource)
        resourceControl.setAccessibilityIdentifier("workbench.manager.resource")
        filter.target = self; filter.action = #selector(reload)
        search.placeholderString = "搜索名称、标签或历史"; search.target = self; search.action = #selector(reload)
        workspaceFilter.placeholderString = "工作区 ID（留空查询全部）"
        workspaceFilter.target = self; workspaceFilter.action = #selector(reload)
        let controls = WorkbenchUI.stack(.horizontal)
        for control in [resourceControl, filter, search, workspaceFilter] as [NSView] { controls.addArrangedSubview(control) }
        controls.addArrangedSubview(WorkbenchUI.button("刷新", target: self, action: #selector(reload)))
        let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("resource")); column.title = "资源 / 状态 / ID"
        table.addTableColumn(column); table.headerView = nil; table.rowHeight = 50
        table.allowsMultipleSelection = true; table.dataSource = self; table.delegate = self
        table.setAccessibilityIdentifier("workbench.manager.table")
        let scroll = NSScrollView(); scroll.documentView = table; scroll.hasVerticalScroller = true
        let split = NSSplitView(); split.isVertical = true; split.dividerStyle = .thin
        split.addArrangedSubview(scroll); split.addArrangedSubview(text.0)
        scroll.widthAnchor.constraint(greaterThanOrEqualToConstant: 320).isActive = true
        text.0.widthAnchor.constraint(greaterThanOrEqualToConstant: 350).isActive = true
        actions.target = self; actions.action = #selector(performAction)
        previous.title = "上一页"; previous.target = self; previous.action = #selector(previousPage)
        next.title = "下一页"; next.target = self; next.action = #selector(nextPage)
        let footer = WorkbenchUI.stack(.horizontal)
        for control in [previous, next, actions] as [NSView] { footer.addArrangedSubview(control) }
        footer.addArrangedSubview(WorkbenchUI.button("复制详情", target: self, action: #selector(copyDetail)))
        footer.addArrangedSubview(WorkbenchUI.button("导出当前页", target: self, action: #selector(exportPage)))
        let stack = WorkbenchUI.stack(.vertical, spacing: 10)
        for control in [controls, status, split, footer] { stack.addArrangedSubview(control) }
        window.contentView?.addSubview(stack)
        stack.pinEdges(to: window.contentView!, insets: NSEdgeInsets(top: 16, left: 16, bottom: 16, right: 16))
        for control in [controls, status, split, footer] { control.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true }
        split.heightAnchor.constraint(greaterThanOrEqualToConstant: 340).isActive = true
        split.setContentHuggingPriority(.defaultLow, for: .vertical)
        configureActions()
    }
    required init?(coder: NSCoder) { nil }
    deinit { request?.cancel(); detailRequest?.cancel(); writeRequest?.cancel() }
    func present(_ value: WorkbenchResource = .tasks) {
        if !busy { resourceControl.selectItem(at: WorkbenchResource.allCases.firstIndex(of: value) ?? 0); changeResource() }
        showWindow(nil); window?.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
    }
    func windowWillClose(_ notification: Notification) {
        generation += 1; selectionGeneration += 1
        request?.cancel(); detailRequest?.cancel(); writeRequest?.cancel()
    }
    @objc private func changeResource() {
        guard !busy else { return }
        resource = WorkbenchResource.allCases[resourceControl.indexOfSelectedItem]
        offset = 0; offsets.removeAll(); page = nil; detail = .null
        table.reloadData(); text.1.string = ""; configureActions(); load()
    }
    private func configureActions() {
        actions.removeAllItems(); actions.addItem(withTitle: "操作…")
        let choices: [(String, String)]
        switch resource {
        case .tasks, .conversations:
            choices = [("打开详情", "detail"), ("重命名", "rename"), ("标签", "tags"), ("置顶", "pin"),
                ("取消置顶", "unpin"), ("归档", "archive"), ("取消归档", "unarchive"),
                ("移入回收站", "trash"), ("恢复", "restore"), ("永久删除", "delete")]
        case .approvals: choices = [("查看原请求", "detail"), ("批准本次", "approve"), ("拒绝本次", "reject")]
        case .skills: choices = [("查看内容", "detail"), ("启用", "enable"), ("禁用", "disable")]
        case .plugins: choices = [("详情", "detail"), ("安装本地包", "install"), ("更新本地包", "update"),
            ("启用", "enable"), ("禁用", "disable"), ("按需加载", "heavy_enable"), ("普通加载", "heavy_disable"),
            ("启用成员", "member_enable"), ("禁用成员", "member_disable"), ("移除", "remove")]
        case .mcp: choices = [("详情", "detail"), ("启用", "enable"), ("禁用", "disable"),
            ("刷新目录", "refresh"), ("添加 HTTP MCP", "add"), ("移除", "remove")]
        case .workspaces: choices = [("查看", "detail"), ("注册工作区", "register")]
        case .display: choices = [("编辑显示设置", "display")]
        }
        for (title, key) in choices {
            actions.addItem(withTitle: title); actions.lastItem?.representedObject = key
        }
        filter.isHidden = !resource.pageable; workspaceFilter.isHidden = resource != .tasks && resource != .conversations
    }
    @objc private func reload() { guard !busy else { return }; offset = 0; offsets.removeAll(); load() }
    private func load() {
        request?.cancel(); detailRequest?.cancel(); generation += 1; selectionGeneration += 1
        let current = generation, type = resource
        let requestedOffset = offset, query = search.stringValue, view = filter.titleOfSelectedItem ?? "active"
        let workspace = workspaceFilter.stringValue
        stale = true; actions.isEnabled = false
        status.stringValue = "正在读取 \(type.title)…"
        request = Task { [weak self] in
            guard let self else { return }
            do {
                let loaded = try await client.managementPage(type, offset: requestedOffset, search: query, view: view, workspaceID: workspace)
                try Task.checkCancellation(); guard current == generation else { return }
                page = loaded; stale = false; table.reloadData()
                status.stringValue = "已加载 \(loaded.items.count) 项 · 偏移 \(requestedOffset) · 总量 \(loaded.total.map(String.init) ?? "未知")"
                if type == .workspaces { status.stringValue += " · 写入需 WB01 接口已集成" }
                actions.isEnabled = true; previous.isEnabled = !offsets.isEmpty; next.isEnabled = loaded.hasMore
                if type == .display { detail = loaded.items.first ?? .null; text.1.string = detail.prettyPrinted }
            } catch {
                guard current == generation, !Task.isCancelled else { return }
                status.stringValue = "读取失败；原快照保留：\(error.localizedDescription)"
            }
        }
    }
    @objc private func nextPage() {
        guard !busy, let page, page.hasMore, !stale else { return }
        offsets.append(offset); offset = page.nextOffset; load()
    }
    @objc private func previousPage() {
        guard !busy, let previous = offsets.popLast() else { return }; offset = previous; load()
    }
    func numberOfRows(in tableView: NSTableView) -> Int { page?.items.count ?? 0 }
    func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        guard let items = page?.items, items.indices.contains(row) else { return nil }
        let item = items[row], id = resource.identity(item)
        let label = WorkbenchUI.label(item.firstText("title", "name", "tool_name") + "  " + WorkbenchFormatting.state(item.text("status")) + "\n" + id,
            font: .systemFont(ofSize: 12), lines: 2)
        label.setAccessibilityValue(id); return label
    }
    func tableViewSelectionDidChange(_ notification: Notification) { showDetail() }
    private func selectedItems() -> [WorkbenchJSON] {
        guard let items = page?.items else { return [] }
        return table.selectedRowIndexes.compactMap { items.indices.contains($0) ? items[$0] : nil }
    }
    private func showDetail() {
        detailRequest?.cancel(); selectionGeneration += 1
        let selected = selectedItems()
        guard selected.count == 1, let item = selected.first else { text.1.string = "已选择 \(selected.count) 项"; detail = .null; return }
        let current = generation, selectedGeneration = selectionGeneration, type = resource
        detail = item; text.1.string = item.prettyPrinted
        guard !stale else { return }
        detailRequest = Task { [weak self] in
            guard let self else { return }
            do {
                let loaded = try await client.resourceDetail(type, item: item)
                try Task.checkCancellation()
                guard current == generation, selectedGeneration == selectionGeneration else { return }
                detail = loaded; text.1.string = String(loaded.prettyPrinted.prefix(100000))
            } catch {
                guard current == generation, selectedGeneration == selectionGeneration, !Task.isCancelled else { return }
                status.stringValue = "详情不可用：\(error.localizedDescription)"
            }
        }
    }
    @objc private func copyDetail() { WorkbenchForms.copy(text.1.string) }
    @objc private func exportPage() {
        WorkbenchForms.export(WorkbenchJSON.array(page?.items ?? []).prettyPrinted, name: "agentdock-\(resource.rawValue)-page-\(offset).json")
    }
    @objc private func performAction() {
        defer { actions.selectItem(at: 0) }
        guard !busy, !stale, let action = actions.selectedItem?.representedObject as? String else { return }
        let selected = selectedItems(), type = resource
        let ids = selected.map(type.identity).filter { !$0.isEmpty }
        if action == "detail" { showDetail(); return }
        if type == .tasks || type == .conversations {
            guard !ids.isEmpty, ids.count == selected.count else { return }
            var title = "", tags = [String]()
            if action == "rename" || action == "tags" {
                guard action != "rename" || ids.count == 1,
                      let value = WorkbenchForms.fields(title: action == "rename" ? "重命名" : "批量标签", message: "对 \(ids.count) 个明确资源生效。", fields: [("值", "")])?.first else { return }
                title = action == "rename" ? value : ""
                tags = action == "tags" ? value.split(separator: ",").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) } : []
            }
            guard WorkbenchForms.confirm("确认批量操作？", "\(type.title) · \(action) · \(ids.count) 项\n" + ids.joined(separator: "\n")) else { return }
            mutate { [client] in try await client.manage(kind: type == .tasks ? "task" : "conversation", ids: ids, action: action,
                title: title, tags: tags, confirmPermanent: action == "delete") }; return
        }
        if type == .approvals {
            guard selected.count == 1, let item = selected.first, item.text("status") == "pending",
                  detail["approval"].firstText("approval_id", "id") == ids.first,
                  detail.flag("request_available") else { status.stringValue = "先读取待审批原请求；请求过期或已处理时禁止批准。"; return }
            guard WorkbenchForms.confirm("\(action == "approve" ? "批准" : "拒绝")此请求？", String(detail.prettyPrinted.prefix(4096))) else { return }
            mutate { [client] in try await client.decideApproval(ids[0], action: action) }; return
        }
        if type == .display { editDisplay(); return }
        var fields: [String: WorkbenchJSON] = ["action": .string(action)]
        if !["install", "add", "register"].contains(action) {
            guard ids.count == 1 else { status.stringValue = "此操作需要选择一个资源。"; return }
            fields[type == .skills ? "skill" : "name"] = .string(type == .skills ? (selected[0].optionalText("skill_ref") ?? ids[0]) : ids[0])
        }
        if action == "install" || action == "update" {
            let panel = NSOpenPanel(); panel.canChooseFiles = true; panel.canChooseDirectories = true; panel.allowsMultipleSelection = false
            guard panel.runModal() == .OK, let url = panel.url else { return }; fields["source"] = .string(url.path)
        }
        if action.hasPrefix("member_") {
            guard let values = WorkbenchForms.fields(title: "插件成员", message: "成员类型为 skill 或 mcp，名称以详情中实际成员为准。", fields: [("成员类型", "skill"), ("成员名称", "")]) else { return }
            fields["member_type"] = .string(values[0]); fields["member"] = .string(values[1])
        }
        if action == "add" {
            guard let values = WorkbenchForms.fields(title: "添加 HTTP MCP", message: "服务鉴权通过既有 Core 环境配置；不把秘密放入 URL。", fields: [("名称", ""), ("URL", "https://")]),
                  let url = URL(string: values[1]), url.scheme == "https", url.user == nil, url.password == nil else { return }
            fields["name"] = .string(values[0]); fields["url"] = .string(values[1]); fields["transport"] = .string("streamable_http")
        }
        if action == "register" {
            guard let values = WorkbenchForms.fields(title: "注册工作区", message: "需要集成 WB01 工作区接口。不会创建第二套工作区存储。", fields: [("名称", ""), ("绝对目录", "")]), values[1].hasPrefix("/") else { return }
            fields["name"] = .string(values[0]); fields["root"] = .string(values[1]); fields["kind"] = .string("repository"); fields["runtime"] = .string("unix")
        }
        guard WorkbenchForms.confirm("确认资源操作？", type.title + " · " + action + "\n" + WorkbenchJSON.object(fields).prettyPrinted) else { return }
        mutate { [client] in try await client.post(type.endpoint, body: .object(fields)) }
    }
    private func editDisplay() {
        let value = detail["settings"].isNull ? detail : detail["settings"]
        guard let revision = value["revision"].int64Value, revision > 0,
              let fields = WorkbenchForms.fields(title: "显示设置", message: "修改只更新显示配置，不重启 Core。开关使用 true 或 false。", fields: [
                ("MCP UI", String(value.flag("chatgpt_mcp_ui_enabled"))),
                ("输出截断", String(value["tool_output"].flag("enabled"))),
                ("Unicode 字符上限（1000–100000）", String(value["tool_output"].integer("max_chars")))]),
              let ui = Bool(fields[0]), let enabled = Bool(fields[1]), let limit = Int64(fields[2]), (1000...100000).contains(limit) else { return }
        mutate { [client] in try await client.updateDisplaySettings(.object([
            "expected_revision": .integer(revision), "chatgpt_mcp_ui_enabled": .bool(ui),
            "tool_output": .object(["enabled": .bool(enabled), "max_chars": .integer(limit)])])) }
    }
    private func mutate(_ operation: @escaping @MainActor () async throws -> WorkbenchJSON) {
        guard !busy, !stale else { return }
        busy = true; actions.isEnabled = false; resourceControl.isEnabled = false; table.isEnabled = false
        request?.cancel(); detailRequest?.cancel(); generation += 1
        let current = generation
        writeRequest = Task { [weak self] in
            guard let self else { return }
            defer { busy = false; resourceControl.isEnabled = true; table.isEnabled = true }
            do {
                let result = try await operation()
                guard current == generation, !Task.isCancelled else { return }
                text.1.string = result.prettyPrinted; status.stringValue = "Core 已返回真实操作结果。"
                load()
            } catch {
                guard current == generation, !Task.isCancelled else { return }
                status.stringValue = "操作结果待核对；未自动重试。请刷新：\(error.localizedDescription)"
                stale = true
            }
        }
    }
}
