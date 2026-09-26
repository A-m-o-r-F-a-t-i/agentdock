import AppKit

@MainActor
final class WorkbenchTimelineViewController: NSViewController, NSTableViewDataSource, NSTableViewDelegate, NSTextViewDelegate {
    var onCallSelected: ((String) -> Void)?
    var onInsertionSelected: ((WorkbenchInsertion) -> Void)?
    var onSendInsertion: ((String) -> Void)?
    var onLoadOlder: (() -> Void)?

    private let titleLabel = WorkbenchUI.label("选择一个对话", font: .systemFont(ofSize: 20, weight: .semibold), lines: 2)
    private let metadataLabel = WorkbenchUI.label("", font: .systemFont(ofSize: 11.5), color: WorkbenchPalette.secondaryText, lines: 2)
    private let taskBadge = WorkbenchUI.label("", font: .systemFont(ofSize: 11, weight: .medium), color: WorkbenchPalette.accent)
    private let statusBanner = WorkbenchUI.label("", font: .systemFont(ofSize: 11.5), color: WorkbenchPalette.secondaryText, lines: 2)
    private let table = NSTableView()
    private let scroll = NSScrollView()
    private let loadOlderButton = NSButton()
    private let composerScroll: NSScrollView
    private let composer: NSTextView
    private let composerHint = WorkbenchUI.label("选择活动对话后可插入补充。", font: .systemFont(ofSize: 10.5), color: WorkbenchPalette.secondaryText, lines: 2)
    private let sendButton = NSButton()
    private var entries = [WorkbenchTimelineEntry]()
    private var selectedEntryID = ""
    private var suppressSelection = false

    init() {
        let composerPair = WorkbenchUI.scrollableText(editable: true)
        composerScroll = composerPair.0
        composer = composerPair.1
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { nil }

    override func loadView() {
        let root = NSView()
        root.enableLayerBackground(WorkbenchPalette.windowBackground)

        taskBadge.enableLayerBackground(WorkbenchPalette.accent.withAlphaComponent(0.12), radius: 6)
        taskBadge.alignment = .center
        taskBadge.cell?.lineBreakMode = .byTruncatingTail

        statusBanner.enableLayerBackground(WorkbenchPalette.surface, radius: 7)
        statusBanner.alignment = .left

        let heading = WorkbenchUI.stack(.vertical, spacing: 4)
        heading.addArrangedSubview(titleLabel)
        heading.addArrangedSubview(metadataLabel)
        heading.addArrangedSubview(taskBadge)
        taskBadge.widthAnchor.constraint(lessThanOrEqualToConstant: 430).isActive = true

        let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("timeline"))
        column.resizingMask = .autoresizingMask
        table.addTableColumn(column)
        table.headerView = nil
        table.intercellSpacing = NSSize(width: 0, height: 8)
        table.rowSizeStyle = .custom
        table.selectionHighlightStyle = .regular
        table.allowsMultipleSelection = false
        table.dataSource = self
        table.delegate = self
        table.setAccessibilityIdentifier("workbench.timeline")
        table.setAccessibilityLabel("调用与用户补充时间线")

        scroll.documentView = table
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.drawsBackground = false
        scroll.borderType = .noBorder

        loadOlderButton.title = "加载更早记录"
        loadOlderButton.bezelStyle = .inline
        loadOlderButton.target = self
        loadOlderButton.action = #selector(loadOlder(_:))
        loadOlderButton.setAccessibilityLabel("加载更早的调用记录")

        composer.delegate = self
        composer.setAccessibilityIdentifier("workbench.insertion.editor")
        composer.setAccessibilityLabel("插入用户补充")
        composerScroll.heightAnchor.constraint(equalToConstant: 86).isActive = true

        sendButton.title = "插入对话"
        sendButton.bezelStyle = .rounded
        sendButton.keyEquivalent = "\r"
        sendButton.keyEquivalentModifierMask = [.command]
        sendButton.target = self
        sendButton.action = #selector(sendInsertion(_:))
        sendButton.setAccessibilityIdentifier("workbench.insertion.send")

        let composerFooter = WorkbenchUI.stack(.horizontal, spacing: 10)
        composerFooter.addArrangedSubview(composerHint)
        composerFooter.addArrangedSubview(NSView())
        composerFooter.addArrangedSubview(sendButton)
        composerHint.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)

        let composerBox = WorkbenchUI.stack(.vertical, spacing: 7)
        composerBox.addArrangedSubview(composerScroll)
        composerBox.addArrangedSubview(composerFooter)
        composerBox.enableLayerBackground(WorkbenchPalette.surface, radius: 9)

        let stack = WorkbenchUI.stack(.vertical, spacing: 12)
        stack.alignment = .leading
        stack.addArrangedSubview(heading)
        stack.addArrangedSubview(statusBanner)
        stack.addArrangedSubview(scroll)
        stack.addArrangedSubview(loadOlderButton)
        stack.addArrangedSubview(composerBox)
        root.addSubview(stack)
        stack.pinEdges(to: root, insets: NSEdgeInsets(top: 18, left: 18, bottom: 14, right: 18))
        heading.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        statusBanner.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        statusBanner.heightAnchor.constraint(greaterThanOrEqualToConstant: 32).isActive = true
        scroll.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        scroll.heightAnchor.constraint(greaterThanOrEqualToConstant: 260).isActive = true
        composerBox.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        scroll.setContentHuggingPriority(.defaultLow, for: .vertical)
        scroll.setContentCompressionResistancePriority(.defaultLow, for: .vertical)

        view = root
    }

    func render(_ model: WorkbenchViewModel, selectedInsertionID: String?) {
        let snapshot = model.snapshot
        let conversation = snapshot.selectedConversation
        titleLabel.stringValue = conversation?.title ?? "选择一个对话"
        metadataLabel.stringValue = conversation?.metadataText ?? "从左侧工作区选择对话，查看其任务、调用、审批与用户补充。"
        if let task = snapshot.task {
            taskBadge.stringValue = "任务 · \(task.title) · \(WorkbenchFormatting.state(task.status))"
            taskBadge.isHidden = false
        } else if let conversation, !conversation.activeTaskID.isEmpty {
            taskBadge.stringValue = "任务 · \(conversation.activeTaskID)"
            taskBadge.isHidden = false
        } else {
            taskBadge.stringValue = ""
            taskBadge.isHidden = true
        }

        statusBanner.stringValue = snapshot.message
        statusBanner.textColor = snapshot.stale ? WorkbenchPalette.warning : WorkbenchPalette.secondaryText
        statusBanner.toolTip = snapshot.lastLoadedAt.map { "最近同步：\(WorkbenchFormatting.shortDate($0))" }

        entries = Self.timeline(calls: snapshot.calls.calls, insertions: snapshot.insertions.items)
        selectedEntryID = selectedInsertionID.map { "insertion:\($0)" }
            ?? snapshot.selectedCall.map { "call:\($0.id)" }
            ?? ""
        table.reloadData()
        restoreSelection()
        loadOlderButton.isHidden = !snapshot.calls.hasMore
        loadOlderButton.isEnabled = !model.isOperating && !model.isRefreshing

        let canCompose: Bool
        if let conversation {
            canCompose = !conversation.terminated && !conversation.trashed && !conversation.id.isEmpty && conversation.insertionEligible == true && !snapshot.stale
            if conversation.insertionEligible == true {
                composerHint.stringValue = "插入窗口有效；原始有效期 300 秒，成功附加后等待回执 30 秒。"
            } else if conversation.insertionEligible == false {
                composerHint.stringValue = conversation.insertionEligibilityReason.isEmpty
                    ? "当前不在 180 秒插入窗口内。"
                    : conversation.insertionEligibilityReason
            } else {
                composerHint.stringValue = "Core 尚未确认插入资格；刷新后再提交。"
            }
        } else {
            canCompose = false
            composerHint.stringValue = "选择活动对话后可插入补充。"
        }
        composer.isEditable = canCompose && !model.isOperating
        sendButton.isEnabled = canCompose && !model.isOperating && !composer.string.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    func numberOfRows(in tableView: NSTableView) -> Int { entries.count }

    func tableView(_ tableView: NSTableView, heightOfRow row: Int) -> CGFloat { 82 }

    func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        guard entries.indices.contains(row) else { return nil }
        let identifier = NSUserInterfaceItemIdentifier("TimelineCell")
        let cell = (tableView.makeView(withIdentifier: identifier, owner: self) as? WorkbenchTimelineCellView)
            ?? WorkbenchTimelineCellView(identifier: identifier)
        cell.render(entries[row])
        return cell
    }

    func tableViewSelectionDidChange(_ notification: Notification) {
        guard !suppressSelection, entries.indices.contains(table.selectedRow) else { return }
        switch entries[table.selectedRow].kind {
        case let .call(call): onCallSelected?(call.id)
        case let .insertion(insertion): onInsertionSelected?(insertion)
        }
    }

    func textDidChange(_ notification: Notification) {
        sendButton.isEnabled = composer.isEditable && !composer.string.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    @objc private func sendInsertion(_ sender: Any?) {
        let value = composer.string.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else { return }
        onSendInsertion?(value)
        // Preserve the draft while the submission outcome is unknown.
    }

    @objc private func loadOlder(_ sender: Any?) { onLoadOlder?() }

    private func restoreSelection() {
        guard !selectedEntryID.isEmpty else {
            suppressSelection = true
            table.deselectAll(nil)
            suppressSelection = false
            return
        }
        if let row = entries.firstIndex(where: { $0.identity == selectedEntryID }) {
            suppressSelection = true
            table.selectRowIndexes(IndexSet(integer: row), byExtendingSelection: false)
            table.scrollRowToVisible(row)
            suppressSelection = false
        }
    }

    private static func timeline(calls: [WorkbenchCall], insertions: [WorkbenchInsertion]) -> [WorkbenchTimelineEntry] {
        let insertionIDs = Set(insertions.map(\.id))
        var values = calls.filter { !($0.isInsertion && insertionIDs.contains($0.id)) }
            .map { WorkbenchTimelineEntry(kind: .call($0)) }
        values.append(contentsOf: insertions.map { WorkbenchTimelineEntry(kind: .insertion($0)) })
        values.sort {
            if $0.date == $1.date { return $0.identity > $1.identity }
            return ($0.date ?? .distantPast) > ($1.date ?? .distantPast)
        }
        return values
    }
}

private struct WorkbenchTimelineEntry {
    enum Kind {
        case call(WorkbenchCall)
        case insertion(WorkbenchInsertion)
    }

    let kind: Kind

    var identity: String {
        switch kind {
        case let .call(call): return "call:\(call.id)"
        case let .insertion(insertion): return "insertion:\(insertion.id)"
        }
    }

    var date: Date? {
        switch kind {
        case let .call(call): return call.updatedAt ?? call.createdAt
        case let .insertion(insertion): return insertion.createdAt
        }
    }
}

private final class WorkbenchTimelineCellView: NSTableCellView {
    private let marker = NSView()
    private let titleLabel = WorkbenchUI.label("", font: .systemFont(ofSize: 13.5, weight: .semibold))
    private let metadataLabel = WorkbenchUI.label("", font: .systemFont(ofSize: 10.5), color: WorkbenchPalette.secondaryText)
    private let previewLabel = WorkbenchUI.label("", font: .systemFont(ofSize: 11.5), color: WorkbenchPalette.secondaryText, lines: 2)

    init(identifier: NSUserInterfaceItemIdentifier) {
        super.init(frame: .zero)
        self.identifier = identifier
        enableLayerBackground(WorkbenchPalette.surface, radius: 9)
        marker.wantsLayer = true
        marker.layer?.cornerRadius = 3
        let labels = WorkbenchUI.stack(.vertical, spacing: 3)
        labels.addArrangedSubview(titleLabel)
        labels.addArrangedSubview(metadataLabel)
        labels.addArrangedSubview(previewLabel)
        addSubview(marker)
        addSubview(labels)
        marker.translatesAutoresizingMaskIntoConstraints = false
        labels.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            marker.leadingAnchor.constraint(equalTo: leadingAnchor, constant: 12),
            marker.topAnchor.constraint(equalTo: topAnchor, constant: 13),
            marker.widthAnchor.constraint(equalToConstant: 6),
            marker.heightAnchor.constraint(equalToConstant: 26),
            labels.leadingAnchor.constraint(equalTo: marker.trailingAnchor, constant: 10),
            labels.trailingAnchor.constraint(equalTo: trailingAnchor, constant: -12),
            labels.centerYAnchor.constraint(equalTo: centerYAnchor)
        ])
    }

    required init?(coder: NSCoder) { nil }

    func render(_ entry: WorkbenchTimelineEntry) {
        switch entry.kind {
        case let .call(call):
            titleLabel.stringValue = call.title
            metadataLabel.stringValue = call.metadataText
            previewLabel.stringValue = Self.preview(call.summary.isEmpty ? call.responseText : call.summary)
            marker.layer?.backgroundColor = Self.color(call.status).cgColor
            setAccessibilityLabel(call.title)
            setAccessibilityValue(call.metadataText)
        case let .insertion(insertion):
            titleLabel.stringValue = "用户补充"
            metadataLabel.stringValue = insertion.detailText
            previewLabel.stringValue = Self.preview(insertion.text)
            marker.layer?.backgroundColor = WorkbenchPalette.accent.cgColor
            setAccessibilityLabel("用户补充")
            setAccessibilityValue(insertion.detailText)
        }
    }

    private static func preview(_ value: String) -> String {
        let normalized = value.replacingOccurrences(of: "\n", with: " ").trimmingCharacters(in: .whitespacesAndNewlines)
        return normalized.isEmpty ? "没有可显示的摘要。" : String(normalized.prefix(220))
    }

    private static func color(_ status: String) -> NSColor {
        switch status {
        case "running", "created": return WorkbenchPalette.accent
        case "pending_approval": return WorkbenchPalette.warning
        case "succeeded", "completed": return WorkbenchPalette.success
        case "failed", "cancelled", "canceled": return WorkbenchPalette.danger
        default: return WorkbenchPalette.secondaryText
        }
    }
}
