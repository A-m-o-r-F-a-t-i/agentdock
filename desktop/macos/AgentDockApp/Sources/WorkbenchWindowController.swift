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
    private lazy var manager = WorkbenchManagementWindow(client: model.client)
    private lazy var policyEditor = WorkbenchPermissionEditor(client: model.client)

    init(fixtureMode: Bool = false, client: WorkbenchAPIClient = WorkbenchAPIClient()) {
        self.fixtureMode = fixtureMode
        model = WorkbenchViewModel(client: client, fixtureMode: fixtureMode)

        let split = NSSplitViewController()
        split.splitView.dividerStyle = .thin
        split.splitView.isVertical = true
        split.splitView.autosaveName = "WorkbenchSplitPositions"

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
        window.subtitle = L10n.text("Task and execution center")
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
    func presentTask(_ id: String) { present(); manager.presentTask(id) }

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
        manager.close()
        policyEditor.close()
    }

    func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [.refresh, .manager, .policy, .theme, .settings, .permissions, .flexibleSpace]
    }

    func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [.refresh, .manager, .policy, .flexibleSpace, .permissions, .theme, .settings]
    }

    func toolbar(
        _ toolbar: NSToolbar,
        itemForItemIdentifier itemIdentifier: NSToolbarItem.Identifier,
        willBeInsertedIntoToolbar flag: Bool
    ) -> NSToolbarItem? {
        let item = NSToolbarItem(itemIdentifier: itemIdentifier)
        switch itemIdentifier {
        case .refresh:
            item.label = L10n.text("Refresh")
            item.paletteLabel = L10n.text("Refresh")
            item.toolTip = L10n.text("Refresh from AgentDock Core")
            item.image = NSImage(systemSymbolName: "arrow.clockwise", accessibilityDescription: L10n.text("Refresh"))
            item.target = self
            item.action = #selector(refreshToolbar(_:))
        case .manager:
            item.label = L10n.text("Management center")
            item.image = NSImage(systemSymbolName: "list.bullet.rectangle", accessibilityDescription: L10n.text("Management center"))
            item.target = self
            item.action = #selector(openManager)
        case .policy:
            item.label = L10n.text("Permission settings")
            item.image = NSImage(systemSymbolName: "slider.horizontal.3", accessibilityDescription: L10n.text("Permission settings"))
            item.target = self
            item.action = #selector(openPolicy)
        case .theme:
            item.label = L10n.text("Theme")
            item.paletteLabel = L10n.text("Theme")
            item.toolTip = L10n.text("Switch between system, light and dark themes")
            item.image = NSImage(systemSymbolName: "circle.lefthalf.filled", accessibilityDescription: L10n.text("Theme"))
            item.target = self
            item.action = #selector(cycleTheme(_:))
        case .settings:
            item.label = L10n.text("Settings")
            item.paletteLabel = L10n.text("Settings")
            item.toolTip = L10n.text("Open AgentDock settings")
            item.image = NSImage(systemSymbolName: "gearshape", accessibilityDescription: L10n.text("Settings"))
            item.target = self
            item.action = #selector(openSettings(_:))
        case .permissions:
            item.label = L10n.text("System permissions")
            item.paletteLabel = L10n.text("System permissions")
            item.toolTip = L10n.text("Check macOS system permissions")
            item.image = NSImage(systemSymbolName: "lock.shield", accessibilityDescription: L10n.text("System permissions"))
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
        sidebar.onHistory = { [weak self] in self?.manager.present(.conversations) }
        sidebar.onAddWorkspace = { [weak self] in self?.manager.present(.workspaces) }
        detail.onReadPayload = { [weak self] in self?.model.loadPayload(source: $0) }
        detail.onOpenPolicy = { [weak self] in self?.openPolicy() }

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

        detail.onChildCalls = { [weak self] id in self?.manager.present(.calls, parentCallID: id) }
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
        window?.subtitle = model.snapshot.stale ? L10n.text("Offline snapshot") : model.snapshot.message
    }

    private func handleConversationAction(_ action: String) {
        guard let conversation = model.snapshot.selectedConversation else { return }
        switch action {
        case "rename":
            guard let value = prompt(
                title: L10n.text("Rename conversation"),
                message: L10n.text("The name changes only the display title, not the Conversation ID."),
                fields: [(L10n.text("Name"), conversation.title)]
            )?.first, !value.isEmpty else { return }
            model.manageSelectedConversation(action: "rename", title: value)
        case "tags":
            let current = conversation.tags.joined(separator: ", ")
            guard let raw = prompt(
                title: L10n.text("Edit tags"),
                message: L10n.text("Separate tags with commas. Core normalizes and validates them after submission."),
                fields: [(L10n.text("Tags"), current)]
            )?.first else { return }
            let tags = raw.split(separator: ",").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
            model.manageSelectedConversation(action: "tags", tags: tags)
        case "toggle_pin":
            model.manageSelectedConversation(action: conversation.pinned ? "unpin" : "pin")
        case "archive", "unarchive", "trash", "restore":
            if action == "trash", !confirm(title: L10n.text("Move to Trash?"), message: L10n.text("The conversation can be restored from Trash."), destructive: true) { return }
            model.manageSelectedConversation(action: action)
        case "delete":
            guard confirm(
                title: L10n.text("Permanently delete this conversation?"),
                message: L10n.text("This cannot be undone. Core still enforces retention and ownership checks."),
                destructive: true,
                confirmTitle: L10n.text("Permanently delete")
            ) else { return }
            model.manageSelectedConversation(action: "delete", confirmPermanent: true)
        case "link_task":
            guard let taskID = prompt(
                title: L10n.text("Link task"),
                message: L10n.text("Enter an existing Task ID."),
                fields: [("Task ID", conversation.activeTaskID)]
            )?.first, !taskID.isEmpty else { return }
            model.linkSelectedConversation(to: taskID)
        case "current_task":
            guard let values = prompt(
                title: L10n.text("Set current task"),
                message: L10n.text("This binding determines the task and thread inherited by subsequent calls."),
                fields: [("Task ID", conversation.activeTaskID), ("Thread ID", conversation.activeThreadID)]
            ), values.count == 2, !values[0].isEmpty else { return }
            model.setSelectedCurrentTask(taskID: values[0], threadID: values[1])
        case "terminate":
            guard confirm(
                title: L10n.text("Terminate this conversation?"),
                message: L10n.text("Core will reject new business calls. This button does not mark already running operations as stopped."),
                destructive: true,
                confirmTitle: L10n.text("Terminate")
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
                title: L10n.text("Enable full permission?"),
                message: L10n.text("This reduces Core approval prompts for business tools. Explicit deny rules and macOS system permissions remain effective."),
                destructive: true,
                confirmTitle: L10n.text("Enable full permission")
            ) else { return }
        }
        model.updatePermissionMode(mode)
    }

    private func prompt(title: String, message: String, fields: [(String, String)]) -> [String]? {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.alertStyle = .informational
        alert.addButton(withTitle: L10n.text("OK"))
        alert.addButton(withTitle: L10n.text("Cancel"))

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
        confirmTitle: String = L10n.text("Continue")
    ) -> Bool {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.alertStyle = destructive ? .warning : .informational
        alert.addButton(withTitle: confirmTitle)
        alert.addButton(withTitle: L10n.text("Cancel"))
        return alert.runModal() == .alertFirstButtonReturn
    }

    @objc private func openManager() { manager.present() }
    @objc private func openPolicy() {
        policyEditor.present(workspaceID: model.snapshot.selectedConversation?.workspaceID ?? "")
    }
    @objc private func refreshToolbar(_ sender: Any?) { model.refresh() }
    @objc private func cycleTheme(_ sender: Any?) { WorkbenchAppearance.shared.cycle() }
    @objc private func openSettings(_ sender: Any?) { onOpenSettings?() }
    @objc private func openPermissions(_ sender: Any?) { onOpenPermissions?() }
}

private extension NSToolbarItem.Identifier {
    static let refresh = NSToolbarItem.Identifier("workbench.refresh")
    static let manager = NSToolbarItem.Identifier("workbench.manager")
    static let policy = NSToolbarItem.Identifier("workbench.policy")
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
