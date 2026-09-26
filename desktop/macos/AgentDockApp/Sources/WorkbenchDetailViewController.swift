import AppKit
import UniformTypeIdentifiers

@MainActor
final class WorkbenchDetailViewController: NSViewController {
    var onStopCall: (() -> Void)?
    var onApprovalDecision: ((Bool) -> Void)?
    var onConversationAction: ((String) -> Void)?
    var onPermissionMode: ((String) -> Void)?
    var onInsertionAction: ((String, String) -> Void)?
    var onReadPayload: ((String) -> Void)?
    var onOpenPolicy: (() -> Void)?
    private let readRequestButton = NSButton()
    private let readOutputButton = NSButton()
    private let payloadCaption = WorkbenchUI.label("输出默认隐藏", font: .systemFont(ofSize: 11), lines: 3)
    private var loadedOutput = ""

    private let titleLabel = WorkbenchUI.label("详情", font: .systemFont(ofSize: 16, weight: .semibold), lines: 2)
    private let subtitleLabel = WorkbenchUI.label("选择调用或用户补充。", font: .systemFont(ofSize: 11), color: WorkbenchPalette.secondaryText, lines: 2)
    private let stopButton = NSButton()
    private let approveButton = NSButton()
    private let rejectButton = NSButton()
    private let copyButton = NSButton()
    private let exportButton = NSButton()
    private let conversationMenu = NSPopUpButton()
    private let tabs = NSTabView()

    private let requestText: NSTextView
    private let outputText: NSTextView
    private let taskText: NSTextView
    private let permissionText: NSTextView
    private let insertionText: NSTextView
    private let technicalText: NSTextView
    private let permissionMode = NSPopUpButton()
    private let applyPermissionButton = NSButton()
    private let retryInsertionButton = NSButton()
    private let cancelInsertionButton = NSButton()

    private var currentCall: WorkbenchCall?
    private var currentInsertion: WorkbenchInsertion?
    private var currentPermission: WorkbenchPermissionState?

    init() {
        let requestPair = WorkbenchUI.scrollableText(monospaced: true)
        let outputPair = WorkbenchUI.scrollableText(monospaced: true)
        let taskPair = WorkbenchUI.scrollableText()
        let permissionPair = WorkbenchUI.scrollableText(monospaced: true)
        let insertionPair = WorkbenchUI.scrollableText()
        let technicalPair = WorkbenchUI.scrollableText(monospaced: true)
        requestText = requestPair.1
        outputText = outputPair.1
        taskText = taskPair.1
        permissionText = permissionPair.1
        insertionText = insertionPair.1
        technicalText = technicalPair.1
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { nil }

    override func loadView() {
        let root = NSView()
        root.enableLayerBackground(WorkbenchPalette.surface)

        configureButton(stopButton, title: "停止调用", action: #selector(stopCall(_:)))
        configureButton(approveButton, title: "审批", action: #selector(approve(_:)))
        configureButton(rejectButton, title: "拒绝", action: #selector(reject(_:)))
        configureButton(copyButton, title: "复制", action: #selector(copyDetail(_:)))
        configureButton(exportButton, title: "导出", action: #selector(exportDetail(_:)))

        conversationMenu.addItem(withTitle: "对话操作…")
        conversationMenu.menu?.addItem(.separator())
        for item in conversationActions {
            let menuItem = NSMenuItem(title: item.title, action: nil, keyEquivalent: "")
            menuItem.representedObject = item.key
            conversationMenu.menu?.addItem(menuItem)
        }
        conversationMenu.target = self
        conversationMenu.action = #selector(conversationAction(_:))
        conversationMenu.setAccessibilityLabel("对话操作")

        let headerActions = WorkbenchUI.stack(.horizontal, spacing: 6)
        for button in [stopButton, approveButton, rejectButton, copyButton, exportButton] {
            headerActions.addArrangedSubview(button)
        }

        let header = WorkbenchUI.stack(.vertical, spacing: 5)
        header.addArrangedSubview(titleLabel)
        header.addArrangedSubview(subtitleLabel)
        header.addArrangedSubview(headerActions)
        header.addArrangedSubview(conversationMenu)

        tabs.addTabViewItem(tab(label: "调用与输出", view: executionView()))
        tabs.addTabViewItem(tab(label: "任务", view: textTab(taskText, identifier: "workbench.detail.task")))
        tabs.addTabViewItem(tab(label: "权限", view: permissionView()))
        tabs.addTabViewItem(tab(label: "用户补充", view: insertionView()))
        tabs.addTabViewItem(tab(label: "技术", view: textTab(technicalText, identifier: "workbench.detail.technical")))
        tabs.tabViewType = .topTabsBezelBorder
        tabs.setAccessibilityIdentifier("workbench.detail.tabs")

        let stack = WorkbenchUI.stack(.vertical, spacing: 10)
        stack.alignment = .leading
        stack.addArrangedSubview(header)
        stack.addArrangedSubview(tabs)
        root.addSubview(stack)
        stack.pinEdges(to: root, insets: NSEdgeInsets(top: 16, left: 14, bottom: 12, right: 14))
        header.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        tabs.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        tabs.heightAnchor.constraint(greaterThanOrEqualToConstant: 390).isActive = true
        tabs.setContentHuggingPriority(.defaultLow, for: .vertical)
        tabs.setContentCompressionResistancePriority(.defaultLow, for: .vertical)
        view = root
    }

    func render(_ model: WorkbenchViewModel, selectedInsertion: WorkbenchInsertion?) {
        let snapshot = model.snapshot
        currentInsertion = selectedInsertion
        currentCall = selectedInsertion == nil ? snapshot.selectedCall : nil
        currentPermission = snapshot.permission

        if let insertion = selectedInsertion {
            titleLabel.stringValue = "用户补充"
            subtitleLabel.stringValue = insertion.detailText
            insertionText.string = insertion.text + "\n\n" + insertion.raw.prettyPrinted
            tabs.selectTabViewItem(at: 3)
        } else if let call = snapshot.selectedCall {
            titleLabel.stringValue = call.title
            subtitleLabel.stringValue = call.metadataText
            requestText.string = call.requestText.isEmpty ? "调用参数未内联；可按需读取分页载荷。" : call.requestText
            outputText.string = call.responseText.isEmpty ? "工具输出未记录或仍在执行。" : call.responseText
        } else if let conversation = snapshot.selectedConversation {
            titleLabel.stringValue = conversation.title
            subtitleLabel.stringValue = conversation.metadataText
            requestText.string = "选择时间线中的调用以查看参数。"
            outputText.string = "选择时间线中的调用以查看真实工具输出。"
        } else {
            titleLabel.stringValue = "详情"
            subtitleLabel.stringValue = "选择调用或用户补充。"
            requestText.string = ""
            outputText.string = ""
        }

        taskText.string = snapshot.task?.detailText ?? "当前对话没有活动任务，或此 Core 版本未提供任务详情接口。"
        if let permission = snapshot.permission {
            permissionText.string = permission.summaryText + "\n\n设置\n" + permission.settings.prettyPrinted
            let mode = permission.mode == "read_only" ? "readonly" : permission.mode
            permissionMode.selectItem(withTitle: modeTitle(mode))
        } else {
            permissionText.string = "权限接口不可用；Workbench 不会用本地默认值替代 Core 的有效权限。"
            permissionMode.selectItem(at: 0)
        }

        if selectedInsertion == nil {
            insertionText.string = snapshot.insertions.items.isEmpty
                ? "当前没有排队或历史用户补充。"
                : snapshot.insertions.items.map { "\($0.detailText)\n\($0.text)" }.joined(separator: "\n\n——\n\n")
        }

        technicalText.string = technicalDetail(snapshot: snapshot, selectedInsertion: selectedInsertion)
        if let slice = model.payloadSlices["request"] { requestText.string = slice.text }
        if let slice = model.payloadSlices["response"] {
            loadedOutput = slice.text
            outputText.string = slice.text
            payloadCaption.stringValue = slice.caption
        } else {
            loadedOutput = ""
            outputText.string = "输出尚未展开；点击读取后按 Unicode 字符分段加载。"
            payloadCaption.stringValue = "未展开输出时不读取载荷；每段最多 10000 个 Unicode 字符。"
        }
        readRequestButton.isEnabled = currentCall != nil && !model.isReadingPayload && model.payloadSlices["request"]?.hasMore != false
        readOutputButton.isEnabled = currentCall != nil && !model.isReadingPayload && model.payloadSlices["response"]?.hasMore != false
        updateActions(model)
    }

    private var conversationActions: [(title: String, key: String)] {
        [
            ("重命名…", "rename"),
            ("编辑标签…", "tags"),
            ("置顶 / 取消置顶", "toggle_pin"),
            ("归档", "archive"),
            ("取消归档", "unarchive"),
            ("移入回收站", "trash"),
            ("从回收站恢复", "restore"),
            ("永久删除…", "delete"),
            ("关联任务…", "link_task"),
            ("设为当前任务…", "current_task"),
            ("终止对话…", "terminate"),
            ("恢复对话", "resume")
        ]
    }

    private func executionView() -> NSView {
        let requestScroll = enclosingScroll(for: requestText)
        let outputScroll = enclosingScroll(for: outputText)
        requestText.setAccessibilityIdentifier("workbench.detail.request")
        outputText.setAccessibilityIdentifier("workbench.detail.response")

        let left = WorkbenchUI.stack(.vertical, spacing: 6)
        left.addArrangedSubview(WorkbenchUI.label("调用参数", font: .systemFont(ofSize: 12, weight: .semibold)))
        configureButton(readRequestButton, title: "读取参数 / 下一段", action: #selector(readRequest))
        left.addArrangedSubview(readRequestButton)
        left.addArrangedSubview(requestScroll)
        let right = WorkbenchUI.stack(.vertical, spacing: 6)
        right.addArrangedSubview(WorkbenchUI.label("真实工具输出", font: .systemFont(ofSize: 12, weight: .semibold)))
        configureButton(readOutputButton, title: "展开输出 / 下一段", action: #selector(readOutput))
        readOutputButton.setAccessibilityIdentifier("workbench.output.load")
        right.addArrangedSubview(readOutputButton)
        right.addArrangedSubview(WorkbenchUI.button("复制当前输出段", target: self, action: #selector(copyOutput)))
        right.addArrangedSubview(payloadCaption)
        right.addArrangedSubview(outputScroll)

        let split = NSSplitView()
        split.isVertical = false
        split.dividerStyle = .thin
        split.addArrangedSubview(left)
        split.addArrangedSubview(right)
        left.heightAnchor.constraint(greaterThanOrEqualToConstant: 120).isActive = true
        right.heightAnchor.constraint(greaterThanOrEqualToConstant: 180).isActive = true
        return split
    }

    private func permissionView() -> NSView {
        let scroll = enclosingScroll(for: permissionText)
        permissionText.setAccessibilityIdentifier("workbench.detail.permission")
        permissionMode.addItems(withTitles: ["需要审批", "完全权限", "只读"])
        permissionMode.setAccessibilityLabel("权限模式")
        applyPermissionButton.title = "应用权限模式"
        applyPermissionButton.target = self
        applyPermissionButton.action = #selector(applyPermission(_:))
        applyPermissionButton.bezelStyle = .rounded
        let controls = WorkbenchUI.stack(.horizontal, spacing: 8)
        controls.addArrangedSubview(permissionMode)
        controls.addArrangedSubview(applyPermissionButton)
        let note = WorkbenchUI.label(
            "修改使用 Core 修订号执行 CAS；完全权限需要显式确认。自定义权限设置仅在 Core 暴露该能力时显示。",
            font: .systemFont(ofSize: 10.5),
            color: WorkbenchPalette.secondaryText,
            lines: 3
        )
        let stack = WorkbenchUI.stack(.vertical, spacing: 8)
        stack.addArrangedSubview(controls)
        stack.addArrangedSubview(note)
        stack.addArrangedSubview(WorkbenchUI.button("完整权限设置…", target: self, action: #selector(openPolicy)))
        stack.addArrangedSubview(scroll)
        return stack
    }

    private func insertionView() -> NSView {
        let scroll = enclosingScroll(for: insertionText)
        insertionText.setAccessibilityIdentifier("workbench.detail.insertion")
        retryInsertionButton.title = "有限重投"
        retryInsertionButton.target = self
        retryInsertionButton.action = #selector(retryInsertion(_:))
        retryInsertionButton.bezelStyle = .rounded
        cancelInsertionButton.title = "取消补充"
        cancelInsertionButton.target = self
        cancelInsertionButton.action = #selector(cancelInsertion(_:))
        cancelInsertionButton.bezelStyle = .rounded
        let controls = WorkbenchUI.stack(.horizontal, spacing: 8)
        controls.addArrangedSubview(retryInsertionButton)
        controls.addArrangedSubview(cancelInsertionButton)
        let stack = WorkbenchUI.stack(.vertical, spacing: 8)
        stack.addArrangedSubview(controls)
        stack.addArrangedSubview(scroll)
        return stack
    }

    private func textTab(_ text: NSTextView, identifier: String) -> NSView {
        text.setAccessibilityIdentifier(identifier)
        return enclosingScroll(for: text)
    }

    private func enclosingScroll(for text: NSTextView) -> NSScrollView {
        if let scroll = text.enclosingScrollView { return scroll }
        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = text.font?.fontName.lowercased().contains("mono") == true
        scroll.autohidesScrollers = true
        scroll.borderType = .noBorder
        scroll.documentView = text
        return scroll
    }

    private func tab(label: String, view: NSView) -> NSTabViewItem {
        let item = NSTabViewItem(identifier: label)
        item.label = label
        item.view = view
        return item
    }

    private func configureButton(_ button: NSButton, title: String, action: Selector) {
        button.title = title
        button.bezelStyle = .rounded
        button.target = self
        button.action = action
        button.setAccessibilityLabel(title)
    }

    private func updateActions(_ model: WorkbenchViewModel) {
        let call = currentCall
        stopButton.isEnabled = call?.canStop == true && !model.isOperating
        stopButton.isHidden = call == nil
        approveButton.isEnabled = call?.needsApproval == true && !model.isOperating
        rejectButton.isEnabled = call?.needsApproval == true && !model.isOperating
        approveButton.isHidden = call?.needsApproval != true
        rejectButton.isHidden = call?.needsApproval != true
        copyButton.isEnabled = currentCall != nil || currentInsertion != nil
        exportButton.isEnabled = currentCall != nil || currentInsertion != nil
        conversationMenu.isEnabled = !model.selectedConversationID.isEmpty && !model.isOperating && !model.snapshot.stale
        applyPermissionButton.isEnabled = currentPermission != nil && !model.selectedConversationID.isEmpty && !model.isOperating && !model.snapshot.stale
        retryInsertionButton.isEnabled = currentInsertion?.manualRetryAvailable == true && currentInsertion?.terminal == false && !model.isOperating
        cancelInsertionButton.isEnabled = currentInsertion?.terminal == false && !model.isOperating
    }

    private func technicalDetail(snapshot: WorkbenchSnapshot, selectedInsertion: WorkbenchInsertion?) -> String {
        if let selectedInsertion { return selectedInsertion.raw.prettyPrinted }
        guard let call = snapshot.selectedCall else {
            return snapshot.selectedConversation?.raw.prettyPrinted ?? ""
        }
        var sections = [String]()
        if !call.command.isEmpty { sections.append("命令\n\(call.command)") }
        if !call.workdir.isEmpty { sections.append("工作目录\n\(call.workdir)") }
        sections.append("耗时\n\(call.timingText)")
        sections.append("文件编辑\n\(call.fileEditText)")
        sections.append("原始记录\n\(call.raw.prettyPrinted)")
        return sections.joined(separator: "\n\n")
    }

    private func modeTitle(_ mode: String) -> String {
        switch mode {
        case "full": return "完全权限"
        case "readonly", "read_only": return "只读"
        default: return "需要审批"
        }
    }

    private func selectedMode() -> String {
        switch permissionMode.titleOfSelectedItem {
        case "完全权限": return "full"
        case "只读": return "readonly"
        default: return "rules"
        }
    }

    @objc private func stopCall(_ sender: Any?) { onStopCall?() }
    @objc private func readRequest() { onReadPayload?("request") }
    @objc private func readOutput() { onReadPayload?("response") }
    @objc private func openPolicy() { onOpenPolicy?() }
    @objc private func copyOutput() {
        guard !loadedOutput.isEmpty else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(loadedOutput, forType: .string)
    }
    @objc private func approve(_ sender: Any?) { onApprovalDecision?(true) }
    @objc private func reject(_ sender: Any?) { onApprovalDecision?(false) }
    @objc private func applyPermission(_ sender: Any?) { onPermissionMode?(selectedMode()) }

    @objc private func conversationAction(_ sender: NSPopUpButton) {
        defer { sender.selectItem(at: 0) }
        guard let key = sender.selectedItem?.representedObject as? String else { return }
        onConversationAction?(key)
    }

    @objc private func retryInsertion(_ sender: Any?) {
        guard let insertion = currentInsertion else { return }
        onInsertionAction?(insertion.id, "retry")
    }

    @objc private func cancelInsertion(_ sender: Any?) {
        guard let insertion = currentInsertion else { return }
        onInsertionAction?(insertion.id, "cancel")
    }

    @objc private func copyDetail(_ sender: Any?) {
        let value = currentInsertion?.raw.prettyPrinted ?? currentCall?.raw.prettyPrinted ?? ""
        guard !value.isEmpty else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
    }

    @objc private func exportDetail(_ sender: Any?) {
        let value = currentInsertion?.raw.prettyPrinted ?? currentCall?.raw.prettyPrinted ?? ""
        guard !value.isEmpty else { return }
        let identifier = currentInsertion?.id ?? currentCall?.id ?? "detail"
        let safe = identifier.replacingOccurrences(of: "/", with: "-")
        let panel = NSSavePanel()
        panel.nameFieldStringValue = "agentdock-\(safe).json"
        panel.allowedContentTypes = [.json]
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do {
            try Data(value.utf8).write(to: url, options: .atomic)
        } catch {
            let alert = NSAlert(error: error)
            alert.runModal()
        }
    }
}
