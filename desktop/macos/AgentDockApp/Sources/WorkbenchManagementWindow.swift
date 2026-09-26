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
    private var parentCallID = ""
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
        window.title = L10n.text("Workbench management center"); window.isReleasedWhenClosed = false
        window.minSize = NSSize(width: 840, height: 560)
        super.init(window: window); window.delegate = self
        resourceControl.addItems(withTitles: WorkbenchResource.allCases.map(\.title))
        resourceControl.target = self; resourceControl.action = #selector(changeResource)
        resourceControl.setAccessibilityIdentifier("workbench.manager.resource")
        filter.target = self; filter.action = #selector(reload)
        search.placeholderString = L10n.text("Search names, tags or history"); search.target = self; search.action = #selector(reload)
        workspaceFilter.placeholderString = L10n.text("Workspace ID (empty for all)")
        workspaceFilter.target = self; workspaceFilter.action = #selector(reload)
        let controls = WorkbenchUI.stack(.horizontal)
        for control in [resourceControl, filter, search, workspaceFilter] as [NSView] { controls.addArrangedSubview(control) }
        controls.addArrangedSubview(WorkbenchUI.button(L10n.text("Refresh"), target: self, action: #selector(reload)))
        let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("resource")); column.title = L10n.text("Resource / State / ID")
        table.addTableColumn(column); table.headerView = nil; table.rowHeight = 50
        table.allowsMultipleSelection = true; table.dataSource = self; table.delegate = self
        table.setAccessibilityIdentifier("workbench.manager.table")
        let scroll = NSScrollView(); scroll.documentView = table; scroll.hasVerticalScroller = true
        let split = NSSplitView(); split.isVertical = true; split.dividerStyle = .thin
        split.addArrangedSubview(scroll); split.addArrangedSubview(text.0)
        scroll.widthAnchor.constraint(greaterThanOrEqualToConstant: 320).isActive = true
        text.0.widthAnchor.constraint(greaterThanOrEqualToConstant: 350).isActive = true
        actions.target = self; actions.action = #selector(performAction)
        previous.title = L10n.text("Previous page"); previous.target = self; previous.action = #selector(previousPage)
        next.title = L10n.text("Next page"); next.target = self; next.action = #selector(nextPage)
        let footer = WorkbenchUI.stack(.horizontal)
        for control in [previous, next, actions] as [NSView] { footer.addArrangedSubview(control) }
        footer.addArrangedSubview(WorkbenchUI.button(L10n.text("Copy details"), target: self, action: #selector(copyDetail)))
        footer.addArrangedSubview(WorkbenchUI.button(L10n.text("Export current page"), target: self, action: #selector(exportPage)))
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
    func present(_ value: WorkbenchResource = .tasks, parentCallID: String = "") {
        self.parentCallID = parentCallID
        if !busy { resourceControl.selectItem(at: WorkbenchResource.allCases.firstIndex(of: value) ?? 0); changeResource() }
        showWindow(nil); window?.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
    }
    func presentTask(_ id: String) {
        present(.tasks)
        detailRequest?.cancel()
        let current = generation
        detailRequest = Task { [weak self] in
            guard let self else { return }
            do {
                let value = try await client.task(id)
                try Task.checkCancellation(); guard generation == current else { return }
                detail = value; text.1.string = value.prettyPrinted
                status.stringValue = "task_id=" + id
            } catch {
                guard generation == current, !Task.isCancelled else { return }
                status.stringValue = error.localizedDescription
            }
        }
    }
    func windowWillClose(_ notification: Notification) {
        generation += 1; selectionGeneration += 1
        request?.cancel(); detailRequest?.cancel(); writeRequest?.cancel()
    }
    @objc private func changeResource() {
        guard !busy else { return }
        resource = WorkbenchResource.allCases[resourceControl.indexOfSelectedItem]
        if resource != .calls { parentCallID = "" }
        let previousFilter = filter.titleOfSelectedItem ?? "active"
        filter.removeAllItems()
        filter.addItems(withTitles: resource == .calls ? ["active", "all", "archived", "trash", "isolated"] : ["active", "all", "archived", "trash"])
        if filter.itemTitles.contains(previousFilter) { filter.selectItem(withTitle: previousFilter) }
        offset = 0; offsets.removeAll(); page = nil; detail = .null
        table.reloadData(); text.1.string = ""; configureActions(); load()
    }
    private func configureActions() {
        actions.removeAllItems(); actions.addItem(withTitle: L10n.text("Actions…"))
        let choices: [(String, String)]
        switch resource {
        case .tasks, .conversations:
            choices = [(L10n.text("Open details"), "detail"), (L10n.text("Rename"), "rename"), (L10n.text("Tags"), "tags"), (L10n.text("Pin"), "pin"),
                (L10n.text("Unpin"), "unpin"), (L10n.text("Archive"), "archive"), (L10n.text("Unarchive"), "unarchive"),
                (L10n.text("Move to Trash"), "trash"), (L10n.text("Restore"), "restore"), (L10n.text("Permanently delete"), "delete")]
        case .calls: choices = [(L10n.text("Open details"), "detail"), (L10n.text("Child calls"), "children"),
            (L10n.text("All calls"), "all_calls"), (L10n.text("Archive"), "archive"), (L10n.text("Unarchive"), "unarchive"),
            (L10n.text("Isolate"), "isolate"), (L10n.text("Remove isolation"), "unisolate"),
            (L10n.text("Move to Trash"), "trash"), (L10n.text("Restore"), "restore"), (L10n.text("Permanently delete"), "delete")]
        case .approvals: choices = [(L10n.text("View original request"), "detail"), (L10n.text("Approve this request"), "approve"), (L10n.text("Reject this request"), "reject")]
        case .skills: choices = [(L10n.text("View content"), "detail"), (L10n.text("Enable"), "enable"), (L10n.text("Disable"), "disable")]
        case .plugins: choices = [(L10n.text("Details"), "detail"), (L10n.text("Verify candidate package"), "validate"), (L10n.text("Install local package"), "install"), (L10n.text("Update local package"), "update"),
            (L10n.text("Enable"), "enable"), (L10n.text("Disable"), "disable"), (L10n.text("Load on demand"), "heavy_enable"), (L10n.text("Normal loading"), "heavy_disable"),
            (L10n.text("Enable member"), "member_enable"), (L10n.text("Disable member"), "member_disable"), (L10n.text("Remove"), "remove")]
        case .mcp: choices = [(L10n.text("Details"), "detail"), (L10n.text("Enable"), "enable"), (L10n.text("Disable"), "disable"),
            (L10n.text("Refresh catalog"), "refresh"), (L10n.text("Add HTTP MCP"), "add"), (L10n.text("Remove"), "remove")]
        case .workspaces: choices = [(L10n.text("View"), "detail"), (L10n.text("Register workspace"), "register")]
        case .display: choices = [(L10n.text("Edit display settings"), "display")]
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
        let workspace = workspaceFilter.stringValue, parent = parentCallID
        stale = true; actions.isEnabled = false
        status.stringValue = L10n.format("Reading %@…", String(describing: type.title))
        request = Task { [weak self] in
            guard let self else { return }
            do {
                let loaded = try await client.managementPage(type, offset: requestedOffset, search: query, view: view, workspaceID: workspace, parentCallID: parent)
                try Task.checkCancellation(); guard current == generation else { return }
                page = loaded; stale = false; table.reloadData()
                status.stringValue = L10n.format("Loaded %@ items · Offset %@ · Total %@", String(describing: loaded.items.count), String(describing: requestedOffset), String(describing: loaded.total.map(String.init) ?? L10n.text("Unknown")))
                if !parent.isEmpty { status.stringValue += " · parent_call_id=" + parent }
                if type == .workspaces { status.stringValue += L10n.text(" · Writes require the WB01 interface to be integrated") }
                actions.isEnabled = true; previous.isEnabled = !offsets.isEmpty; next.isEnabled = loaded.hasMore
                if type == .display { detail = loaded.items.first ?? .null; text.1.string = detail.prettyPrinted }
            } catch {
                guard current == generation, !Task.isCancelled else { return }
                status.stringValue = L10n.format("Read failed; the previous snapshot was retained: %@", String(describing: error.localizedDescription))
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
        guard selected.count == 1, let item = selected.first else { text.1.string = L10n.format("Selected %@ items", String(describing: selected.count)); detail = .null; return }
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
                status.stringValue = L10n.format("Details unavailable: %@", String(describing: error.localizedDescription))
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
        if action == "children" || action == "all_calls" {
            guard action == "all_calls" || ids.count == 1 else { return }
            parentCallID = action == "children" ? ids[0] : ""
            offset = 0; offsets.removeAll(); load(); return
        }
        if type == .tasks || type == .conversations || type == .calls {
            guard !ids.isEmpty, ids.count == selected.count else { return }
            var title = "", tags = [String]()
            if action == "rename" || action == "tags" {
                guard action != "rename" || ids.count == 1,
                      let value = WorkbenchForms.fields(title: action == "rename" ? L10n.text("Rename") : L10n.text("Batch tags"), message: L10n.format("Applies to %@ explicit resources.", String(describing: ids.count)), fields: [(L10n.text("Value"), "")])?.first else { return }
                title = action == "rename" ? value : ""
                tags = action == "tags" ? value.split(separator: ",").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) } : []
            }
            guard WorkbenchForms.confirm(L10n.text("Confirm batch operation?"), L10n.format("%@ · %@ · %@ items\n", String(describing: type.title), String(describing: action), String(describing: ids.count)) + ids.joined(separator: "\n")) else { return }
            mutate { [client] in try await client.manage(kind: type == .tasks ? "task" : (type == .calls ? "call" : "conversation"), ids: ids, action: action,
                title: title, tags: tags, confirmPermanent: action == "delete") }; return
        }
        if type == .approvals {
            guard selected.count == 1, let item = selected.first, item.text("status") == "pending",
                  detail["approval"].firstText("approval_id", "id") == ids.first,
                  detail.flag("request_available") else { status.stringValue = L10n.text("Read the original pending approval request first. Expired or already processed requests cannot be approved."); return }
            guard WorkbenchForms.confirm(L10n.format("%@ this request?", String(describing: action == "approve" ? L10n.text("Approve") : L10n.text("Reject"))), String(detail.prettyPrinted.prefix(4096))) else { return }
            mutate { [client] in try await client.decideApproval(ids[0], action: action) }; return
        }
        if type == .display { editDisplay(); return }
        var fields: [String: WorkbenchJSON] = ["action": .string(action)]
        if !["install", "validate", "add", "register"].contains(action) {
            guard ids.count == 1 else { status.stringValue = L10n.text("Select one resource for this operation."); return }
            fields[type == .skills ? "skill" : "name"] = .string(type == .skills ? (selected[0].optionalText("skill_ref") ?? ids[0]) : ids[0])
        }
        if action == "validate" || action == "install" || action == "update" {
            let panel = NSOpenPanel(); panel.canChooseFiles = true; panel.canChooseDirectories = true; panel.allowsMultipleSelection = false
            guard panel.runModal() == .OK, let url = panel.url else { return }; fields["source"] = .string(url.path)
        }
        if action.hasPrefix("member_") {
            guard let values = WorkbenchForms.fields(title: L10n.text("Plugin member"), message: L10n.text("The member type is skill or mcp_server; use the actual member name shown in Details."), fields: [(L10n.text("Member type"), "skill"), (L10n.text("Member name"), "")]) else { return }
            guard ["skill", "mcp_server"].contains(values[0]), !values[1].isEmpty else { return }
            fields["member_type"] = .string(values[0]); fields["member"] = .string(values[1])
        }
        if action == "add" {
            guard let values = WorkbenchForms.fields(title: L10n.text("Add HTTP MCP"), message: L10n.text("Service authentication uses the existing Core environment configuration; do not put secrets in the URL."), fields: [(L10n.text("Name"), ""), ("URL", "https://")]),
                  let url = URL(string: values[1]), url.scheme == "https", url.user == nil, url.password == nil else { return }
            fields["name"] = .string(values[0]); fields["url"] = .string(values[1]); fields["transport"] = .string("streamable_http")
        }
        if action == "register" {
            guard let values = WorkbenchForms.fields(title: L10n.text("Register workspace"), message: L10n.text("The WB01 workspace interface must be integrated. No second workspace store is created."), fields: [(L10n.text("Name"), ""), (L10n.text("Absolute directory"), "")]), values[1].hasPrefix("/") else { return }
            fields["name"] = .string(values[0]); fields["root"] = .string(values[1]); fields["kind"] = .string("repository"); fields["runtime"] = .string("unix")
        }
        guard WorkbenchForms.confirm(L10n.text("Confirm resource operation?"), type.title + " · " + action + "\n" + WorkbenchJSON.object(fields).prettyPrinted) else { return }
        mutate { [client] in try await client.post(type.endpoint, body: .object(fields)) }
    }
    private func editDisplay() {
        let value = detail["settings"].isNull ? detail : detail["settings"]
        guard let revision = value["revision"].int64Value, revision > 0,
              let fields = WorkbenchForms.fields(title: L10n.text("Display settings"), message: L10n.text("This updates only display configuration and does not restart Core. Enter true or false for switches."), fields: [
                ("MCP UI", String(value.flag("chatgpt_mcp_ui_enabled"))),
                (L10n.text("Output truncation"), String(value["tool_output"].flag("enabled"))),
                (L10n.text("Unicode scalar limit (1000–100000)"), String(value["tool_output"].integer("max_chars")))]),
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
                text.1.string = result.prettyPrinted; status.stringValue = L10n.text("Core returned the actual operation result.")
                load()
            } catch {
                guard current == generation, !Task.isCancelled else { return }
                status.stringValue = L10n.format("The operation result needs verification; no automatic retry was made. Refresh: %@", String(describing: error.localizedDescription))
                stale = true
            }
        }
    }
}
