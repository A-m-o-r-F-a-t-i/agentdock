import AppKit

@MainActor
final class WorkbenchWindowController: NSWindowController, NSWindowDelegate, NSToolbarDelegate {
    var onOpenSettings: (() -> Void)?
    var onOpenPermissions: (() -> Void)?

    private let model: WorkbenchViewModel
    private let sidebar = WorkbenchSidebarViewController()
    private let timeline = WorkbenchTimelineViewController()
    private let detail = WorkbenchDetailViewController()
    private var selectedInsertionID: String?
    private let fixtureMode: Bool

    init(fixtureMode: Bool = false, client: WorkbenchAPIClient = WorkbenchAPIClient()) {
        self.fixtureMode = fixtureMode
        model = WorkbenchViewModel(client: client, fixtureMode: fixtureMode)

        let split = NSSplitViewController()
        split.splitView.dividerStyle = .thin
        split.splitView.isVertical = true

        let sidebarItem = NSSplitViewItem(sidebarWithViewController: sidebar)
        sidebarItem.minimumThickness = 230
        sidebarItem.maximumThickness = 350
        sidebarItem.canCollapse = false

        let timelineItem = NSSplitViewItem(viewController: timeline)
        timelineItem.minimumThickness = 440
        timelineItem.canCollapse = false

        let detailItem = NSSplitViewItem(viewController: detail)
        detailItem.minimumThickness = 360
        detailItem.maximumThickness = 650
        detailItem.canCollapse = true

        split.addSplitViewItem(sidebarItem)
        split.addSplitViewItem(timelineItem)
        split.addSplitViewItem(detailItem)

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1320, height: 820),
            styleMask: [.titled, .closable, .resizable, .miniaturizable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.title = "AgentDock Workbench"
        window.subtitle = "任务与执行中心"
        window.contentViewController = split
        window.minSize = NSSize(width: 1040, height: 650)
        window.collectionBehavior.insert(.fullScreenPrimary)
        window.isReleasedWhenClosed = false
        window.titlebarAppearsTransparent = true
        window.titleVisibility = .visible
        window.setFrameAutosaveName("AgentDockWorkbenchWindow")

        let toolbar = NSToolbar(identifier: "AgentDockWorkbenchToolbar")
        toolbar.delegate = nil
        toolbar.displayMode = .iconOnly
        toolbar.allowsUserCustomization = false
        toolbar.autosavesConfiguration = false
        window.toolbar = toolbar
        if #available(macOS 11.0, *) { window.toolbarStyle = .unified }

        super.init(window: window)
        window.delegate = self
        toolbar.delegate = self
        wireActions()

        model.onChange = { [weak self] model in
            self?.render(model)
        }
        WorkbenchAppearance.shared.onChange = { [weak self] _ in
            self?.window?.contentView?.needsDisplay = true
            self?.render(self?.model)
        }
        WorkbenchAppearance.shared.apply()
        render(model)
    }

    required init?(coder: NSCoder) { nil }

    func present() {
        guard let window else { return }
        model.start()
        showWindow(nil)
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    func refresh() { model.refresh() }

    func capturePNG(to url: URL) throws {
        guard let content = window?.contentView else {
            throw WorkbenchScreenshotError.missingWindow
        }
        window?.setFrame(NSRect(x: 0, y: 0, width: 1320, height: 820), display: true)
        content.layoutSubtreeIfNeeded()
        window?.displayIfNeeded()
        guard let representation = content.bitmapImageRepForCachingDisplay(in: content.bounds) else {
            throw WorkbenchScreenshotError.captureFailed
        }
        content.cacheDisplay(in: content.bounds, to: representation)
        guard let data = representation.representation(using: .png, properties: [:]) else {
            throw WorkbenchScreenshotError.encodingFailed
        }
        try data.write(to: url, options: .atomic)
    }

    func windowWillClose(_ notification: Notification) {
        model.stop()
    }

    func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [.refresh, .theme, .settings, .permissions, .flexibleSpace]
    }

    func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [.refresh, .flexibleSpace, .permissions, .theme, .settings]
    }

    func toolbar(
        _ toolbar: NSToolbar,
        itemForItemIdentifier itemIdentifier: NSToolbarItem.Identifier,
        willBeInsertedIntoToolbar flag: Bool
    ) -> NSToolbarItem? {
        let item = NSToolbarItem(itemIdentifier: itemIdentifier)
        switch itemIdentifier {
        case .refresh:
            item.label = "刷新"
            item.paletteLabel = "刷新"
            item.toolTip = "从 AgentDock Core 刷新"
            item.image = NSImage(systemSymbolName: "arrow.clockwise", accessibilityDescription: "刷新")
            item.target = self
            item.action = #selector(refreshToolbar(_:))
        case .theme:
            item.label = "主题"
            item.paletteLabel = "主题"
            item.toolTip = "切换系统、浅色与深色主题"
            item.image = NSImage(systemSymbolName: "circle.lefthalf.filled", accessibilityDescription: "主题")
            item.target = self
            item.action = #selector(cycleTheme(_:))
        case .settings:
            item.label = "设置"
            item.paletteLabel = "设置"
            item.toolTip = "打开 AgentDock 设置"
            item.image = NSImage(systemSymbolName: "gearshape", accessibilityDescription: "设置")
            item.target = self
            item.action = #selector(openSettings(_:))
        case .permissions:
            item.label = "系统权限"
            item.paletteLabel = "系统权限"
            item.toolTip = "检查 macOS 系统权限"
            item.image = NSImage(systemSymbolName: "lock.shield", accessibilityDescription: "系统权限")
            item.target = self
            item.action = #selector(openPermissions(_:))
        default:
            return nil
        }
        return item
    }

    private func wireActions() {
        sidebar.onSearch = { [weak self] in self?.model.setSearchText($0) }
        sidebar.onViewChanged = { [weak self] in self?.model.setListView($0) }
        sidebar.onConversationSelected = { [weak self] id in
            self?.selectedInsertionID = nil
            self?.model.selectConversation(id)
        }
        sidebar.onLoadMore = { [weak self] in self?.model.loadMoreWorkspace($0) }

        timeline.onCallSelected = { [weak self] id in
            self?.selectedInsertionID = nil
            self?.model.selectCall(id)
        }
        timeline.onInsertionSelected = { [weak self] insertion in
            guard let self else { return }
            selectedInsertionID = insertion.id
            detail.render(model, selectedInsertion: insertion)
            timeline.render(model, selectedInsertionID: insertion.id)
        }
        timeline.onSendInsertion = { [weak self] in
            self?.selectedInsertionID = nil
            self?.model.submitInsertion($0)
        }
        timeline.onLoadOlder = { [weak self] in self?.model.loadOlderCalls() }

        detail.onStopCall = { [weak self] in self?.model.stopSelectedCall() }
        detail.onApprovalDecision = { [weak self] approved in
            self?.model.decideSelectedApproval(approve: approved)
        }
        detail.onConversationAction = { [weak self] action in self?.handleConversationAction(action) }
        detail.onPermissionMode = { [weak self] mode in self?.applyPermissionMode(mode) }
        detail.onInsertionAction = { [weak self] id, action in
            self?.model.insertionAction(id, action: action)
        }
    }

    private func render(_ model: WorkbenchViewModel?) {
        guard let model else { return }
        if let selectedInsertionID,
           !model.snapshot.insertions.items.contains(where: { $0.id == selectedInsertionID }) {
            self.selectedInsertionID = nil
        }
        let selectedInsertion = self.selectedInsertionID.flatMap { id in
            model.snapshot.insertions.items.first(where: { $0.id == id })
        }
        sidebar.render(model)
        timeline.render(model, selectedInsertionID: selectedInsertion?.id)
        detail.render(model, selectedInsertion: selectedInsertion)
        window?.subtitle = model.snapshot.stale ? "离线快照" : model.snapshot.message
    }

    private func handleConversationAction(_ action: String) {
        guard let conversation = model.snapshot.selectedConversation else { return }
        switch action {
        case "rename":
            guard let value = prompt(
                title: "重命名对话",
                message: "名称只改变展示标题，不改变 Conversation ID。",
                fields: [("名称", conversation.title)]
            )?.first, !value.isEmpty else { return }
            model.manageSelectedConversation(action: "rename", title: value)
        case "tags":
            let current = conversation.tags.joined(separator: ", ")
            guard let raw = prompt(
                title: "编辑标签",
                message: "使用逗号分隔；提交后由 Core 规范化与校验。",
                fields: [("标签", current)]
            )?.first else { return }
            let tags = raw.split(separator: ",").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
            model.manageSelectedConversation(action: "tags", tags: tags)
        case "toggle_pin":
            model.manageSelectedConversation(action: conversation.pinned ? "unpin" : "pin")
        case "archive", "unarchive", "trash", "restore":
            if action == "trash", !confirm(title: "移入回收站？", message: "对话可从回收站恢复。", destructive: true) { return }
            model.manageSelectedConversation(action: action)
        case "delete":
            guard confirm(
                title: "永久删除此对话？",
                message: "此操作不可恢复。Core 仍会执行所有保留期与所有权检查。",
                destructive: true,
                confirmTitle: "永久删除"
            ) else { return }
            model.manageSelectedConversation(action: "delete", confirmPermanent: true)
        case "link_task":
            guard let taskID = prompt(
                title: "关联任务",
                message: "输入已存在的 Task ID。",
                fields: [("Task ID", conversation.activeTaskID)]
            )?.first, !taskID.isEmpty else { return }
            model.linkSelectedConversation(to: taskID)
        case "current_task":
            guard let values = prompt(
                title: "设为当前任务",
                message: "此绑定决定后续调用继承的任务与线程。",
                fields: [("Task ID", conversation.activeTaskID), ("Thread ID", conversation.activeThreadID)]
            ), values.count == 2, !values[0].isEmpty else { return }
            model.setSelectedCurrentTask(taskID: values[0], threadID: values[1])
        case "terminate":
            guard confirm(
                title: "终止此对话？",
                message: "Core 将拒绝新的业务调用；已运行操作不会被本按钮伪装成已停止。",
                destructive: true,
                confirmTitle: "终止"
            ) else { return }
            model.setConversationTerminated(true)
        case "resume":
            model.setConversationTerminated(false)
        default:
            break
        }
    }

    private func applyPermissionMode(_ mode: String) {
        if mode == "full" {
            guard confirm(
                title: "启用完全权限？",
                message: "这会减少 Core 对业务工具的审批。显式禁止规则与 macOS 系统权限仍然有效。",
                destructive: true,
                confirmTitle: "启用完全权限"
            ) else { return }
        }
        model.updatePermissionMode(mode)
    }

    private func prompt(title: String, message: String, fields: [(String, String)]) -> [String]? {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.alertStyle = .informational
        alert.addButton(withTitle: "确定")
        alert.addButton(withTitle: "取消")

        let stack = NSStackView()
        stack.orientation = .vertical
        stack.spacing = 8
        stack.alignment = .leading
        var controls = [NSTextField]()
        for field in fields {
            let label = WorkbenchUI.label(field.0, font: .systemFont(ofSize: 11, weight: .medium))
            let control = NSTextField(string: field.1)
            control.widthAnchor.constraint(equalToConstant: 360).isActive = true
            control.setAccessibilityLabel(field.0)
            stack.addArrangedSubview(label)
            stack.addArrangedSubview(control)
            controls.append(control)
        }
        alert.accessoryView = stack
        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        return controls.map { $0.stringValue.trimmingCharacters(in: .whitespacesAndNewlines) }
    }

    private func confirm(
        title: String,
        message: String,
        destructive: Bool,
        confirmTitle: String = "继续"
    ) -> Bool {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.alertStyle = destructive ? .warning : .informational
        alert.addButton(withTitle: confirmTitle)
        alert.addButton(withTitle: "取消")
        return alert.runModal() == .alertFirstButtonReturn
    }

    @objc private func refreshToolbar(_ sender: Any?) { model.refresh() }
    @objc private func cycleTheme(_ sender: Any?) { WorkbenchAppearance.shared.cycle() }
    @objc private func openSettings(_ sender: Any?) { onOpenSettings?() }
    @objc private func openPermissions(_ sender: Any?) { onOpenPermissions?() }
}

private extension NSToolbarItem.Identifier {
    static let refresh = NSToolbarItem.Identifier("workbench.refresh")
    static let theme = NSToolbarItem.Identifier("workbench.theme")
    static let settings = NSToolbarItem.Identifier("workbench.settings")
    static let permissions = NSToolbarItem.Identifier("workbench.permissions")
}

enum WorkbenchScreenshotError: LocalizedError {
    case missingWindow
    case captureFailed
    case encodingFailed

    var errorDescription: String? {
        switch self {
        case .missingWindow: return "Workbench window is unavailable."
        case .captureFailed: return "Could not allocate a bitmap for the Workbench window."
        case .encodingFailed: return "Could not encode the Workbench screenshot as PNG."
        }
    }
}
