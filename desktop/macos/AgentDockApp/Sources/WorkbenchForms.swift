import AppKit

@MainActor
enum WorkbenchForms {
    static func fields(title: String, message: String, fields: [(String, String)]) -> [String]? {
        let alert = NSAlert()
        alert.messageText = title; alert.informativeText = message
        alert.addButton(withTitle: "确定"); alert.addButton(withTitle: "取消")
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
        alert.addButton(withTitle: "确认"); alert.addButton(withTitle: "取消")
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
    private let custom = NSButton(checkboxWithTitle: "启用自定义权限设置", target: nil, action: nil)
    private let filesystem = WorkbenchForms.popup(["deny", "read", "write"], selected: "read")
    private let network = WorkbenchForms.popup(["deny", "allow"], selected: "deny")
    private let boundary = WorkbenchForms.popup(["workspace", "none"], selected: "workspace")
    private let approval = WorkbenchForms.popup(["on-request", "never", "granular"], selected: "on-request")
    private let reviewer = WorkbenchForms.popup(["user", "auto_review"], selected: "user")
    private let executionMode = WorkbenchForms.popup(["rules", "readonly", "full"], selected: "rules")
    private let body = WorkbenchUI.stack(.vertical)
    private let granular = WorkbenchUI.stack(.vertical, spacing: 3)
    private var categories = [String: NSButton]()
    private let note = WorkbenchUI.label("正在读取有效权限…", lines: 6)
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
        window.title = "权限设置"; window.isReleasedWhenClosed = false
        window.minSize = NSSize(width: 560, height: 650)
        super.init(window: window); window.delegate = self
        let stack = WorkbenchUI.stack(.vertical, spacing: 12)
        scope.target = self; scope.action = #selector(scopeChanged)
        scope.setAccessibilityLabel("权限生效范围")
        stack.addArrangedSubview(scope); stack.addArrangedSubview(note)
        stack.addArrangedSubview(row("执行模式", executionMode))
        custom.target = self; custom.action = #selector(toggleCustom)
        custom.setAccessibilityIdentifier("workbench.permission.custom")
        stack.addArrangedSubview(custom)
        body.addArrangedSubview(row("文件系统", filesystem))
        body.addArrangedSubview(row("网络", network))
        body.addArrangedSubview(row("准入边界（非 OS 沙箱）", boundary))
        body.addArrangedSubview(row("Approval Policy", approval))
        body.addArrangedSubview(row("Approval Reviewer", reviewer))
        approval.target = self; approval.action = #selector(toggleCustom)
        for (key, title) in [("file_writes", "文件写入"), ("commands", "命令"), ("network", "网络"),
                              ("mcp", "MCP"), ("management", "管理"), ("other", "其他")] {
            let button = NSButton(checkboxWithTitle: title, target: nil, action: nil)
            categories[key] = button; granular.addArrangedSubview(button)
        }
        body.addArrangedSubview(granular); stack.addArrangedSubview(body)
        stack.addArrangedSubview(WorkbenchUI.label("关闭自定义时保留历史值，仅由执行模式和显式规则决定权限。never 拒绝需要审批的操作。自动审查仅使用 Core 已配置的可信 Reviewer。", lines: 4))
        save.title = "保存当前范围"; save.target = self; save.action = #selector(saveChanges); save.bezelStyle = .rounded
        let controls = WorkbenchUI.stack(.horizontal)
        controls.addArrangedSubview(save)
        controls.addArrangedSubview(WorkbenchUI.button("重新读取", target: self, action: #selector(scopeChanged)))
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
        scope.removeAllItems(); scope.addItem(withTitle: "全局")
        if !workspaceID.isEmpty { scope.addItem(withTitle: "当前工作区 · " + workspaceID); scope.selectItem(at: 1) }
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
                note.stringValue = result.summaryText + "\n保存目标：" + (workspace.isEmpty ? "全局" : workspace)
                executionMode.selectItem(withTitle: result.mode)
                custom.isEnabled = result.customSettingsEnabled != nil
                custom.state = result.customSettingsEnabled == true ? .on : .off
                if result.customSettingsEnabled == nil { note.stringValue += "\n当前 Core 不支持自定义权限开关（待 WB02 集成）。" }
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
        guard WorkbenchForms.confirm("保存权限设置？", "目标：\(workspace.isEmpty ? "全局" : workspace)\n模式：\(mode)\n已有显式禁止规则继续有效。") else { return }
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
                note.stringValue = "保存结果待核对，未自动重试。请重新读取：\(error.localizedDescription)"
            }
        }
    }
}
