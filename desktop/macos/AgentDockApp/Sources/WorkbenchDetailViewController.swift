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
    private let payloadCaption = WorkbenchUI.label(L10n.text("Output hidden by default"), font: .systemFont(ofSize: 11), lines: 3)
    private var loadedOutput = ""

    private let titleLabel = WorkbenchUI.label(L10n.text("Details"), font: .systemFont(ofSize: 16, weight: .semibold), lines: 2)
    private let subtitleLabel = WorkbenchUI.label(L10n.text("Select a call or user supplement."), font: .systemFont(ofSize: 11), color: WorkbenchPalette.secondaryText, lines: 2)
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

        configureButton(stopButton, title: L10n.text("Stop call"), action: #selector(stopCall(_:)))
        configureButton(approveButton, title: L10n.text("Approval"), action: #selector(approve(_:)))
        configureButton(rejectButton, title: L10n.text("Reject"), action: #selector(reject(_:)))
        configureButton(copyButton, title: L10n.text("Copy"), action: #selector(copyDetail(_:)))
        configureButton(exportButton, title: L10n.text("Export"), action: #selector(exportDetail(_:)))

        conversationMenu.addItem(withTitle: L10n.text("Conversation actions…"))
        conversationMenu.menu?.addItem(.separator())
        for item in conversationActions {
            let menuItem = NSMenuItem(title: item.title, action: nil, keyEquivalent: "")
            menuItem.representedObject = item.key
            conversationMenu.menu?.addItem(menuItem)
        }
        conversationMenu.target = self
        conversationMenu.action = #selector(conversationAction(_:))
        conversationMenu.setAccessibilityLabel(L10n.text("Conversation actions"))

        let headerActions = WorkbenchUI.stack(.horizontal, spacing: 6)
        for button in [stopButton, approveButton, rejectButton, copyButton, exportButton] {
            headerActions.addArrangedSubview(button)
        }

        let header = WorkbenchUI.stack(.vertical, spacing: 5)
        header.addArrangedSubview(titleLabel)
        header.addArrangedSubview(subtitleLabel)
        header.addArrangedSubview(headerActions)
        header.addArrangedSubview(conversationMenu)

        tabs.addTabViewItem(tab(label: L10n.text("Call and output"), view: executionView()))
        tabs.addTabViewItem(tab(label: L10n.text("Task"), view: textTab(taskText, identifier: "workbench.detail.task")))
        tabs.addTabViewItem(tab(label: L10n.text("Permissions"), view: permissionView()))
        tabs.addTabViewItem(tab(label: L10n.text("User supplement"), view: insertionView()))
        tabs.addTabViewItem(tab(label: L10n.text("Technical"), view: textTab(technicalText, identifier: "workbench.detail.technical")))
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
            titleLabel.stringValue = L10n.text("User supplement")
            subtitleLabel.stringValue = insertion.detailText
            insertionText.string = insertion.text + "\n\n" + insertion.raw.prettyPrinted
            tabs.selectTabViewItem(at: 3)
        } else if let call = snapshot.selectedCall {
            titleLabel.stringValue = call.title
            subtitleLabel.stringValue = call.metadataText
            requestText.string = call.requestText.isEmpty ? L10n.text("Call arguments are not inline; read the paged payload on demand.") : call.requestText
            outputText.string = call.responseText.isEmpty ? L10n.text("Tool output is not recorded or the call is still running.") : call.responseText
        } else if let conversation = snapshot.selectedConversation {
            titleLabel.stringValue = conversation.title
            subtitleLabel.stringValue = conversation.metadataText
            requestText.string = L10n.text("Select a timeline call to inspect its arguments.")
            outputText.string = L10n.text("Select a timeline call to inspect its actual tool output.")
        } else {
            titleLabel.stringValue = L10n.text("Details")
            subtitleLabel.stringValue = L10n.text("Select a call or user supplement.")
            requestText.string = ""
            outputText.string = ""
        }

        taskText.string = snapshot.task?.detailText ?? L10n.text("This conversation has no active task, or this Core version does not provide task details.")
        if let permission = snapshot.permission {
            permissionText.string = permission.summaryText + L10n.text("\n\nSettings\n") + permission.settings.prettyPrinted
            let mode = permission.mode == "read_only" ? "readonly" : permission.mode
            permissionMode.selectItem(withTitle: modeTitle(mode))
        } else {
            permissionText.string = L10n.text("The permission interface is unavailable. Workbench will not replace effective Core permissions with local defaults.")
            permissionMode.selectItem(at: 0)
        }

        if selectedInsertion == nil {
            insertionText.string = snapshot.insertions.items.isEmpty
                ? L10n.text("There are no queued or historical user supplements.")
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
            outputText.string = L10n.text("Output is not expanded. Click Read to load chunks measured in Unicode scalars.")
            payloadCaption.stringValue = L10n.text("Hidden output does not fetch payloads. Each chunk contains at most 10000 Unicode scalars.")
        }
        readRequestButton.isEnabled = currentCall != nil && !model.isReadingPayload && model.payloadSlices["request"]?.hasMore != false
        readOutputButton.isEnabled = currentCall != nil && !model.isReadingPayload && model.payloadSlices["response"]?.hasMore != false
        updateActions(model)
    }

    private var conversationActions: [(title: String, key: String)] {
        [
            (L10n.text("Rename…"), "rename"),
            (L10n.text("Edit tags…"), "tags"),
            (L10n.text("Pin / Unpin"), "toggle_pin"),
            (L10n.text("Archive"), "archive"),
            (L10n.text("Unarchive"), "unarchive"),
            (L10n.text("Move to Trash"), "trash"),
            (L10n.text("Restore from Trash"), "restore"),
            (L10n.text("Permanently delete…"), "delete"),
            (L10n.text("Link task…"), "link_task"),
            (L10n.text("Set current task…"), "current_task"),
            (L10n.text("Terminate conversation…"), "terminate"),
            (L10n.text("Resume conversation"), "resume")
        ]
    }

    private func executionView() -> NSView {
        let requestScroll = enclosingScroll(for: requestText)
        let outputScroll = enclosingScroll(for: outputText)
        requestText.setAccessibilityIdentifier("workbench.detail.request")
        outputText.setAccessibilityIdentifier("workbench.detail.response")

        let left = WorkbenchUI.stack(.vertical, spacing: 6)
        left.addArrangedSubview(WorkbenchUI.label(L10n.text("Call arguments"), font: .systemFont(ofSize: 12, weight: .semibold)))
        configureButton(readRequestButton, title: L10n.text("Read arguments / Next chunk"), action: #selector(readRequest))
        left.addArrangedSubview(readRequestButton)
        left.addArrangedSubview(requestScroll)
        let right = WorkbenchUI.stack(.vertical, spacing: 6)
        right.addArrangedSubview(WorkbenchUI.label(L10n.text("Actual tool output"), font: .systemFont(ofSize: 12, weight: .semibold)))
        configureButton(readOutputButton, title: L10n.text("Expand output / Next chunk"), action: #selector(readOutput))
        readOutputButton.setAccessibilityIdentifier("workbench.output.load")
        right.addArrangedSubview(readOutputButton)
        right.addArrangedSubview(WorkbenchUI.button(L10n.text("Copy current output chunk"), target: self, action: #selector(copyOutput)))
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
        permissionMode.addItems(withTitles: [L10n.text("Approval required"), L10n.text("Full permission"), L10n.text("Read only")])
        permissionMode.setAccessibilityLabel(L10n.text("Permission mode"))
        applyPermissionButton.title = L10n.text("Apply permission mode")
        applyPermissionButton.target = self
        applyPermissionButton.action = #selector(applyPermission(_:))
        applyPermissionButton.bezelStyle = .rounded
        let controls = WorkbenchUI.stack(.horizontal, spacing: 8)
        controls.addArrangedSubview(permissionMode)
        controls.addArrangedSubview(applyPermissionButton)
        let note = WorkbenchUI.label(
            L10n.text("Changes use the Core revision for compare-and-swap. Full permission requires explicit confirmation. Custom permission settings appear only when Core exposes that capability."),
            font: .systemFont(ofSize: 10.5),
            color: WorkbenchPalette.secondaryText,
            lines: 3
        )
        let stack = WorkbenchUI.stack(.vertical, spacing: 8)
        stack.addArrangedSubview(controls)
        stack.addArrangedSubview(note)
        stack.addArrangedSubview(WorkbenchUI.button(L10n.text("Complete permission settings…"), target: self, action: #selector(openPolicy)))
        stack.addArrangedSubview(scroll)
        return stack
    }

    private func insertionView() -> NSView {
        let scroll = enclosingScroll(for: insertionText)
        insertionText.setAccessibilityIdentifier("workbench.detail.insertion")
        retryInsertionButton.title = L10n.text("Bounded redelivery")
        retryInsertionButton.target = self
        retryInsertionButton.action = #selector(retryInsertion(_:))
        retryInsertionButton.bezelStyle = .rounded
        cancelInsertionButton.title = L10n.text("Cancel supplement")
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
        if !call.command.isEmpty { sections.append(L10n.format("Command\n%@", String(describing: call.command))) }
        if !call.workdir.isEmpty { sections.append(L10n.format("Working directory\n%@", String(describing: call.workdir))) }
        sections.append(L10n.format("Timing\n%@", String(describing: call.timingText)))
        sections.append(L10n.format("File editing\n%@", String(describing: call.fileEditText)))
        sections.append(L10n.format("Original record\n%@", String(describing: call.raw.prettyPrinted)))
        return sections.joined(separator: "\n\n")
    }

    private func modeTitle(_ mode: String) -> String {
        switch mode {
        case "full": return L10n.text("Full permission")
        case "readonly", "read_only": return L10n.text("Read only")
        default: return L10n.text("Approval required")
        }
    }

    private func selectedMode() -> String {
        switch permissionMode.titleOfSelectedItem {
        case L10n.text("Full permission"): return "full"
        case L10n.text("Read only"): return "readonly"
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
