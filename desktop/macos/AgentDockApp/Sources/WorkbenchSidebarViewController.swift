import AppKit

@MainActor
final class WorkbenchSidebarViewController: NSViewController, NSOutlineViewDataSource, NSOutlineViewDelegate, NSSearchFieldDelegate {
    var onSearch: ((String) -> Void)?
    var onViewChanged: ((WorkbenchListView) -> Void)?
    var onConversationSelected: ((String) -> Void)?
    var onLoadMore: ((String) -> Void)?

    private let segmented = NSSegmentedControl(
        labels: WorkbenchListView.allCases.map(\.title),
        trackingMode: .selectOne,
        target: nil,
        action: nil
    )
    private let searchField = NSSearchField()
    private let outline = NSOutlineView()
    private let scroll = NSScrollView()
    private let footer = WorkbenchUI.label("", font: .systemFont(ofSize: 11), color: WorkbenchPalette.secondaryText, lines: 2)
    private var roots = [WorkspaceNode]()
    private var selectedConversationID = ""
    private var suppressSelection = false

    override func loadView() {
        let root = NSView()
        root.enableLayerBackground(WorkbenchPalette.sidebarBackground)

        segmented.target = self
        segmented.action = #selector(viewChanged(_:))
        segmented.selectedSegment = 0
        segmented.segmentStyle = .texturedRounded
        segmented.setAccessibilityLabel("对话视图")

        searchField.placeholderString = "搜索对话"
        searchField.delegate = self
        searchField.sendsSearchStringImmediately = true
        searchField.setAccessibilityIdentifier("workbench.search.conversations")
        searchField.setAccessibilityLabel("搜索对话")

        let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("sidebar"))
        column.resizingMask = .autoresizingMask
        outline.addTableColumn(column)
        outline.outlineTableColumn = column
        outline.headerView = nil
        outline.rowSizeStyle = .custom
        outline.intercellSpacing = NSSize(width: 0, height: 2)
        outline.indentationPerLevel = 12
        outline.floatsGroupRows = false
        outline.selectionHighlightStyle = .sourceList
        outline.allowsMultipleSelection = false
        outline.dataSource = self
        outline.delegate = self
        outline.setAccessibilityIdentifier("workbench.sidebar")
        outline.setAccessibilityLabel("工作区与对话")

        scroll.documentView = outline
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.drawsBackground = false
        scroll.borderType = .noBorder

        let heading = WorkbenchUI.label("AgentDock Workbench", font: .systemFont(ofSize: 16, weight: .semibold))
        let subtitle = WorkbenchUI.label("任务与执行中心", font: .systemFont(ofSize: 11), color: WorkbenchPalette.secondaryText)
        let titleStack = WorkbenchUI.stack(.vertical, spacing: 2)
        titleStack.addArrangedSubview(heading)
        titleStack.addArrangedSubview(subtitle)

        let stack = WorkbenchUI.stack(.vertical, spacing: 10)
        stack.alignment = .leading
        stack.addArrangedSubview(titleStack)
        stack.addArrangedSubview(segmented)
        stack.addArrangedSubview(searchField)
        stack.addArrangedSubview(scroll)
        stack.addArrangedSubview(footer)
        root.addSubview(stack)
        stack.pinEdges(to: root, insets: NSEdgeInsets(top: 16, left: 12, bottom: 10, right: 12))
        segmented.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        searchField.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        scroll.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        scroll.heightAnchor.constraint(greaterThanOrEqualToConstant: 240).isActive = true
        footer.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        scroll.setContentHuggingPriority(.defaultLow, for: .vertical)
        scroll.setContentCompressionResistancePriority(.defaultLow, for: .vertical)

        view = root
    }

    func render(_ model: WorkbenchViewModel) {
        segmented.selectedSegment = WorkbenchListView.allCases.firstIndex(of: model.listView) ?? 0
        if searchField.stringValue != model.searchText { searchField.stringValue = model.searchText }
        selectedConversationID = model.selectedConversationID
        roots = model.snapshot.sidebar.groups.map(WorkspaceNode.init)
        footer.stringValue = model.snapshot.sidebar.groups.isEmpty
            ? (model.snapshot.stale ? "Core 离线；保留最近一次快照。" : "没有匹配的对话。")
            : "\(model.snapshot.sidebar.total) 个对话 · 列表顺序由 Core 管理"
        outline.reloadData()
        for root in roots { outline.expandItem(root) }
        restoreSelection()
    }

    func controlTextDidChange(_ notification: Notification) {
        onSearch?(searchField.stringValue)
    }

    @objc private func viewChanged(_ sender: NSSegmentedControl) {
        guard WorkbenchListView.allCases.indices.contains(sender.selectedSegment) else { return }
        onViewChanged?(WorkbenchListView.allCases[sender.selectedSegment])
    }

    func outlineView(_ outlineView: NSOutlineView, numberOfChildrenOfItem item: Any?) -> Int {
        if let root = item as? WorkspaceNode { return root.children.count }
        return roots.count
    }

    func outlineView(_ outlineView: NSOutlineView, child index: Int, ofItem item: Any?) -> Any {
        if let root = item as? WorkspaceNode { return root.children[index] }
        return roots[index]
    }

    func outlineView(_ outlineView: NSOutlineView, isItemExpandable item: Any) -> Bool {
        item is WorkspaceNode
    }

    func outlineView(_ outlineView: NSOutlineView, shouldSelectItem item: Any) -> Bool {
        item is ConversationNode || item is LoadMoreNode
    }

    func outlineView(_ outlineView: NSOutlineView, heightOfRowByItem item: Any) -> CGFloat {
        if item is ConversationNode { return 58 }
        if item is WorkspaceNode { return 34 }
        return 36
    }

    func outlineView(_ outlineView: NSOutlineView, viewFor tableColumn: NSTableColumn?, item: Any) -> NSView? {
        if let root = item as? WorkspaceNode {
            let identifier = NSUserInterfaceItemIdentifier("WorkspaceHeader")
            let cell = (outlineView.makeView(withIdentifier: identifier, owner: self) as? NSTableCellView) ?? {
                let value = NSTableCellView()
                value.identifier = identifier
                let label = WorkbenchUI.label("", font: .systemFont(ofSize: 12, weight: .semibold))
                label.tag = 1
                value.addSubview(label)
                label.pinEdges(to: value, insets: NSEdgeInsets(top: 7, left: 2, bottom: 6, right: 4))
                return value
            }()
            let count = root.group.hasMore ? "\(root.group.shown)/\(root.group.total)" : "\(root.group.total)"
            (cell.viewWithTag(1) as? NSTextField)?.stringValue = "\(root.group.title)   \(count)"
            cell.toolTip = root.group.root
            return cell
        }
        if let node = item as? ConversationNode {
            let identifier = NSUserInterfaceItemIdentifier("ConversationCell")
            let cell = (outlineView.makeView(withIdentifier: identifier, owner: self) as? WorkbenchSidebarConversationCellView)
                ?? WorkbenchSidebarConversationCellView(identifier: identifier)
            cell.render(node.conversation)
            return cell
        }
        if item is LoadMoreNode {
            let identifier = NSUserInterfaceItemIdentifier("LoadMoreCell")
            let cell = (outlineView.makeView(withIdentifier: identifier, owner: self) as? NSTableCellView) ?? {
                let value = NSTableCellView()
                value.identifier = identifier
                let label = WorkbenchUI.label("显示更多…", font: .systemFont(ofSize: 12), color: WorkbenchPalette.accent)
                label.alignment = .center
                label.tag = 1
                value.addSubview(label)
                label.pinEdges(to: value, insets: NSEdgeInsets(top: 8, left: 4, bottom: 7, right: 4))
                return value
            }()
            return cell
        }
        return nil
    }

    func outlineViewSelectionDidChange(_ notification: Notification) {
        guard !suppressSelection else { return }
        let row = outline.selectedRow
        guard row >= 0, let item = outline.item(atRow: row) else { return }
        if let node = item as? ConversationNode {
            onConversationSelected?(node.conversation.id)
        } else if let node = item as? LoadMoreNode {
            suppressSelection = true
            outline.deselectRow(row)
            suppressSelection = false
            onLoadMore?(node.workspaceID)
        }
    }

    private func restoreSelection() {
        guard !selectedConversationID.isEmpty else {
            suppressSelection = true
            outline.deselectAll(nil)
            suppressSelection = false
            return
        }
        for row in 0..<outline.numberOfRows {
            guard let node = outline.item(atRow: row) as? ConversationNode else { continue }
            if node.conversation.id == selectedConversationID {
                suppressSelection = true
                outline.selectRowIndexes(IndexSet(integer: row), byExtendingSelection: false)
                outline.scrollRowToVisible(row)
                suppressSelection = false
                return
            }
        }
    }
}

private final class WorkspaceNode: NSObject {
    let group: WorkbenchWorkspaceGroup
    let children: [NSObject]

    init(group: WorkbenchWorkspaceGroup) {
        self.group = group
        var values: [NSObject] = group.conversations.map(ConversationNode.init)
        if group.hasMore { values.append(LoadMoreNode(workspaceID: group.id)) }
        children = values
    }
}

private final class ConversationNode: NSObject {
    let conversation: WorkbenchConversation
    init(conversation: WorkbenchConversation) { self.conversation = conversation }
}

private final class LoadMoreNode: NSObject {
    let workspaceID: String
    init(workspaceID: String) { self.workspaceID = workspaceID }
}

private final class WorkbenchSidebarConversationCellView: NSTableCellView {
    private let dot = NSView()
    private let titleLabel = WorkbenchUI.label("", font: .systemFont(ofSize: 13, weight: .medium))
    private let metadataLabel = WorkbenchUI.label("", font: .systemFont(ofSize: 10.5), color: WorkbenchPalette.secondaryText)
    private let pinLabel = WorkbenchUI.label("", font: .systemFont(ofSize: 10), color: WorkbenchPalette.secondaryText)

    init(identifier: NSUserInterfaceItemIdentifier) {
        super.init(frame: .zero)
        self.identifier = identifier
        dot.wantsLayer = true
        dot.layer?.cornerRadius = 4
        let labels = WorkbenchUI.stack(.vertical, spacing: 3)
        labels.addArrangedSubview(titleLabel)
        labels.addArrangedSubview(metadataLabel)
        addSubview(dot)
        addSubview(labels)
        addSubview(pinLabel)
        dot.translatesAutoresizingMaskIntoConstraints = false
        labels.translatesAutoresizingMaskIntoConstraints = false
        pinLabel.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            dot.leadingAnchor.constraint(equalTo: leadingAnchor, constant: 3),
            dot.centerYAnchor.constraint(equalTo: centerYAnchor),
            dot.widthAnchor.constraint(equalToConstant: 8),
            dot.heightAnchor.constraint(equalToConstant: 8),
            labels.leadingAnchor.constraint(equalTo: dot.trailingAnchor, constant: 9),
            labels.centerYAnchor.constraint(equalTo: centerYAnchor),
            labels.trailingAnchor.constraint(lessThanOrEqualTo: pinLabel.leadingAnchor, constant: -6),
            pinLabel.trailingAnchor.constraint(equalTo: trailingAnchor, constant: -6),
            pinLabel.centerYAnchor.constraint(equalTo: centerYAnchor)
        ])
    }

    required init?(coder: NSCoder) { nil }

    func render(_ conversation: WorkbenchConversation) {
        titleLabel.stringValue = conversation.title
        metadataLabel.stringValue = conversation.metadataText
        pinLabel.stringValue = conversation.pinned ? "置顶" : ""
        let color: NSColor
        if conversation.inFlight { color = WorkbenchPalette.accent }
        else if conversation.recentlyActive { color = WorkbenchPalette.success }
        else if conversation.pendingCount > 0 { color = WorkbenchPalette.warning }
        else { color = WorkbenchPalette.secondaryText.withAlphaComponent(0.45) }
        dot.layer?.backgroundColor = color.cgColor
        toolTip = conversation.title + "\n" + conversation.metadataText
        setAccessibilityLabel(conversation.title)
        setAccessibilityValue(conversation.metadataText)
    }
}
