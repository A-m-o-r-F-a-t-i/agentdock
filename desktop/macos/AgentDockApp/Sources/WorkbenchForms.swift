import AppKit

@MainActor
enum WorkbenchForms {
    static func fields(title: String, message: String, fields: [(String, String)]) -> [String]? {
        let alert = NSAlert()
        alert.messageText = title; alert.informativeText = message
        alert.addButton(withTitle: L10n.text("OK")); alert.addButton(withTitle: L10n.text("Cancel"))
        let stack = WorkbenchUI.stack(.vertical, spacing: 7)
        var values = [NSTextField]()
        for (label, initial) in fields {
            stack.addArrangedSubview(WorkbenchUI.label(label))
            let input = NSTextField(string: initial)
            input.widthAnchor.constraint(equalToConstant: 440).isActive = true
            input.setAccessibilityLabel(label)
            stack.addArrangedSubview(input); values.append(input)
        }
        alert.accessoryView = stack
        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        return values.map { $0.stringValue.trimmingCharacters(in: .whitespacesAndNewlines) }
    }
    static func confirm(_ title: String, _ message: String) -> Bool {
        let alert = NSAlert(); alert.messageText = title; alert.informativeText = message
        alert.alertStyle = .warning
        alert.addButton(withTitle: L10n.text("Confirm")); alert.addButton(withTitle: L10n.text("Cancel"))
        return alert.runModal() == .alertFirstButtonReturn
    }
    static func popup(_ choices: [String], selected: String) -> NSPopUpButton {
        let control = NSPopUpButton(); control.addItems(withTitles: choices)
        if choices.contains(selected) { control.selectItem(withTitle: selected) }
        return control
    }
    static func copy(_ text: String) {
        NSPasteboard.general.clearContents(); NSPasteboard.general.setString(text, forType: .string)
    }
    static func export(_ text: String, name: String) {
        let panel = NSSavePanel(); panel.nameFieldStringValue = name
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do { try Data(text.utf8).write(to: url, options: .atomic) }
        catch { NSAlert(error: error).runModal() }
    }
}

@MainActor
final class WorkbenchPermissionEditor: NSWindowController, NSWindowDelegate {
    private let client: WorkbenchAPIClient
    private let scope = NSPopUpButton()
    private let custom = NSButton(checkboxWithTitle: L10n.text("Enable custom permission settings"), target: nil, action: nil)
    private let filesystem = WorkbenchForms.popup(["deny", "read", "write"], selected: "read")
    private let network = WorkbenchForms.popup(["deny", "allow"], selected: "deny")
    private let boundary = WorkbenchForms.popup(["workspace", "none"], selected: "workspace")
    private let approval = WorkbenchForms.popup(["on-request", "never", "granular"], selected: "on-request")
    private let reviewer = WorkbenchForms.popup(["user", "auto_review"], selected: "user")
    private let executionMode = WorkbenchForms.popup(["rules", "readonly", "full"], selected: "rules")
    private let body = WorkbenchUI.stack(.vertical)
    private let granular = WorkbenchUI.stack(.vertical, spacing: 3)
    private var categories = [String: NSButton]()
    private let note = WorkbenchUI.label(L10n.text("Reading effective permissions…"), lines: 6)
    private let save = NSButton()
    private var state: WorkbenchPermissionState?
    private var workspaceID = ""
    private var generation = 0
    private var request: Task<Void, Never>?
    private var writing = false
    init(client: WorkbenchAPIClient) {
        self.client = client
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 620, height: 760),
            styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        window.title = L10n.text("Permission settings"); window.isReleasedWhenClosed = false
        window.minSize = NSSize(width: 560, height: 650)
        super.init(window: window); window.delegate = self
        let stack = WorkbenchUI.stack(.vertical, spacing: 12)
        scope.target = self; scope.action = #selector(scopeChanged)
        scope.setAccessibilityLabel(L10n.text("Permission scope"))
        stack.addArrangedSubview(scope); stack.addArrangedSubview(note)
        stack.addArrangedSubview(row(L10n.text("Execution mode"), executionMode))
        custom.target = self; custom.action = #selector(toggleCustom)
        custom.setAccessibilityIdentifier("workbench.permission.custom")
        stack.addArrangedSubview(custom)
        body.addArrangedSubview(row(L10n.text("Filesystem"), filesystem))
        body.addArrangedSubview(row(L10n.text("Network"), network))
        body.addArrangedSubview(row(L10n.text("Admission boundary (not an OS sandbox)"), boundary))
        body.addArrangedSubview(row("Approval Policy", approval))
        body.addArrangedSubview(row("Approval Reviewer", reviewer))
        approval.target = self; approval.action = #selector(toggleCustom)
        for (key, title) in [("file_writes", L10n.text("File writes")), ("commands", L10n.text("Command")), ("network", L10n.text("Network")),
                              ("mcp", "MCP"), ("management", L10n.text("Management")), ("other", L10n.text("Other"))] {
            let button = NSButton(checkboxWithTitle: title, target: nil, action: nil)
            categories[key] = button; granular.addArrangedSubview(button)
        }
        body.addArrangedSubview(granular); stack.addArrangedSubview(body)
        stack.addArrangedSubview(WorkbenchUI.label(L10n.text("Turning custom settings off preserves their saved values. Execution mode and explicit rules decide permissions. The never policy rejects operations requiring approval. Automatic review uses only the trusted Reviewer configured in Core."), lines: 4))
        save.title = L10n.text("Save current scope"); save.target = self; save.action = #selector(saveChanges); save.bezelStyle = .rounded
        let controls = WorkbenchUI.stack(.horizontal)
        controls.addArrangedSubview(save)
        controls.addArrangedSubview(WorkbenchUI.button(L10n.text("Read again"), target: self, action: #selector(scopeChanged)))
        stack.addArrangedSubview(controls)
        window.contentView?.addSubview(stack)
        stack.pinEdges(to: window.contentView!, insets: NSEdgeInsets(top: 18, left: 20, bottom: 18, right: 20))
    }
    required init?(coder: NSCoder) { nil }
    deinit { request?.cancel() }
    private func row(_ title: String, _ control: NSView) -> NSView {
        let row = WorkbenchUI.stack(.horizontal)
        row.addArrangedSubview(WorkbenchUI.label(title)); row.addArrangedSubview(control)
        control.setAccessibilityLabel(title); return row
    }
    func present(workspaceID: String) {
        guard !writing else { showWindow(nil); return }
        self.workspaceID = workspaceID
        scope.removeAllItems(); scope.addItem(withTitle: L10n.text("Global"))
        if !workspaceID.isEmpty { scope.addItem(withTitle: L10n.text("Current workspace · ") + workspaceID); scope.selectItem(at: 1) }
        showWindow(nil); window?.makeKeyAndOrderFront(nil); read()
    }
    func windowWillClose(_ notification: Notification) { generation += 1; request?.cancel() }
    @objc private func scopeChanged() { guard !writing else { return }; read() }
    @objc private func toggleCustom() {
        body.isHidden = custom.state != .on
        granular.isHidden = approval.titleOfSelectedItem != "granular"
    }
    private func read() {
        request?.cancel(); generation += 1
        let current = generation, workspace = scope.indexOfSelectedItem == 1 ? workspaceID : ""
        state = nil; save.isEnabled = false
        request = Task { [weak self] in
            guard let self else { return }
            do {
                let result = try await client.permission(workspaceID: workspace)
                try Task.checkCancellation()
                guard current == generation else { return }
                state = result
                note.stringValue = result.summaryText + L10n.text("\nSave target: ") + (workspace.isEmpty ? L10n.text("Global") : workspace)
                executionMode.selectItem(withTitle: result.mode)
                custom.isEnabled = result.customSettingsEnabled != nil
                custom.state = result.customSettingsEnabled == true ? .on : .off
                if result.customSettingsEnabled == nil { note.stringValue += L10n.text("\nThis Core does not support the custom permission switch (WB02 integration pending).") }
                let settings = result.configuredSettings
                filesystem.selectItem(withTitle: settings["permission_profile"].text("filesystem"))
                network.selectItem(withTitle: settings["permission_profile"].text("network"))
                boundary.selectItem(withTitle: settings["permission_profile"].text("sandbox_boundary"))
                approval.selectItem(withTitle: settings["approval_policy"].text("mode"))
                reviewer.selectItem(withTitle: settings.text("approval_reviewer"))
                for (key, button) in categories { button.state = settings["approval_policy"]["granular"].flag(key) ? .on : .off }
                toggleCustom(); save.isEnabled = result.revision > 0
            } catch {
                guard current == generation, !Task.isCancelled else { return }
                note.stringValue = error.localizedDescription
            }
        }
    }
    @objc private func saveChanges() {
        guard !writing, let state, state.revision > 0, state.revision <= UInt64(Int64.max) else { return }
        let workspace = scope.indexOfSelectedItem == 1 ? workspaceID : ""
        let mode = executionMode.titleOfSelectedItem ?? "rules"
        guard WorkbenchForms.confirm(L10n.text("Save permission settings?"), L10n.format("Target: %@\nMode: %@\nExisting explicit deny rules remain effective.", String(describing: workspace.isEmpty ? L10n.text("Global") : workspace), String(describing: mode))) else { return }
        var fields: [String: WorkbenchJSON] = ["scope": .string(workspace.isEmpty ? "global" : "workspace"),
            "scope_id": .string(workspace), "expected_revision": .integer(Int64(state.revision)),
            "mode": .string(mode), "confirm_full": .bool(mode == "full")]
        if state.customSettingsEnabled != nil {
            fields["custom_permissions_enabled"] = .bool(custom.state == .on)
            if custom.state == .on {
                var policy: [String: WorkbenchJSON] = ["mode": .string(approval.titleOfSelectedItem ?? "on-request")]
                if approval.titleOfSelectedItem == "granular" { policy["granular"] = .object(categories.mapValues { .bool($0.state == .on) }) }
                fields["settings"] = .object([
                    "permission_profile": .object(["filesystem": .string(filesystem.titleOfSelectedItem ?? "deny"),
                        "network": .string(network.titleOfSelectedItem ?? "deny"),
                        "sandbox_boundary": .string(boundary.titleOfSelectedItem ?? "workspace")]),
                    "approval_policy": .object(policy), "approval_reviewer": .string(reviewer.titleOfSelectedItem ?? "user")])
            }
        }
        writing = true; scope.isEnabled = false; save.isEnabled = false
        let current = generation
        request = Task { [weak self] in
            guard let self else { return }
            defer { writing = false; scope.isEnabled = true }
            do {
                _ = try await client.updatePermission(.object(fields))
                guard current == generation, !Task.isCancelled else { return }
                read()
            } catch {
                guard current == generation, !Task.isCancelled else { return }
                note.stringValue = L10n.format("Save result needs verification; no automatic retry was made. Read again: %@", String(describing: error.localizedDescription))
            }
        }
    }
}
