import AppKit
import Foundation

@MainActor
final class WorkbenchAppearance {
    static let shared = WorkbenchAppearance()
    static let preferenceKey = "AgentDockWorkbenchTheme"

    private(set) var theme: WorkbenchTheme
    var onChange: ((WorkbenchTheme) -> Void)?

    private init(defaults: UserDefaults = .standard) {
        theme = WorkbenchTheme(rawValue: defaults.string(forKey: Self.preferenceKey) ?? "") ?? .system
    }

    func setTheme(_ value: WorkbenchTheme, persist: Bool = true) {
        theme = value
        if persist { UserDefaults.standard.set(value.rawValue, forKey: Self.preferenceKey) }
        apply()
        onChange?(value)
    }

    func cycle() {
        switch theme {
        case .system: setTheme(.light)
        case .light: setTheme(.dark)
        case .dark: setTheme(.system)
        }
    }

    func apply() {
        switch theme {
        case .system:
            NSApp.appearance = nil
        case .light:
            NSApp.appearance = NSAppearance(named: .aqua)
        case .dark:
            NSApp.appearance = NSAppearance(named: .darkAqua)
        }
    }
}

enum WorkbenchPalette {
    static var windowBackground: NSColor { .windowBackgroundColor }
    static var sidebarBackground: NSColor { NSColor(name: nil) { appearance in
        appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            ? NSColor(calibratedWhite: 0.105, alpha: 1)
            : NSColor(calibratedWhite: 0.965, alpha: 1)
    } }
    static var surface: NSColor { NSColor(name: nil) { appearance in
        appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            ? NSColor(calibratedWhite: 0.145, alpha: 1)
            : .controlBackgroundColor
    } }
    static var elevatedSurface: NSColor { NSColor(name: nil) { appearance in
        appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            ? NSColor(calibratedWhite: 0.18, alpha: 1)
            : .textBackgroundColor
    } }
    static var separator: NSColor { .separatorColor }
    static var primaryText: NSColor { .labelColor }
    static var secondaryText: NSColor { .secondaryLabelColor }
    static var accent: NSColor { .controlAccentColor }
    static var success: NSColor { .systemGreen }
    static var warning: NSColor { .systemOrange }
    static var danger: NSColor { .systemRed }
}

extension NSView {
    func pinEdges(
        to other: NSView,
        insets: NSEdgeInsets = NSEdgeInsets(top: 0, left: 0, bottom: 0, right: 0)
    ) {
        translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            leadingAnchor.constraint(equalTo: other.leadingAnchor, constant: insets.left),
            trailingAnchor.constraint(equalTo: other.trailingAnchor, constant: -insets.right),
            topAnchor.constraint(equalTo: other.topAnchor, constant: insets.top),
            bottomAnchor.constraint(equalTo: other.bottomAnchor, constant: -insets.bottom)
        ])
    }

    func enableLayerBackground(_ color: NSColor, radius: CGFloat = 0) {
        wantsLayer = true
        layer?.backgroundColor = color.cgColor
        layer?.cornerRadius = radius
        layer?.masksToBounds = radius > 0
    }
}

enum WorkbenchUI {
    static func label(
        _ text: String = "",
        font: NSFont = .systemFont(ofSize: NSFont.systemFontSize),
        color: NSColor = WorkbenchPalette.primaryText,
        lines: Int = 1
    ) -> NSTextField {
        let field = NSTextField(labelWithString: text)
        field.font = font
        field.textColor = color
        field.maximumNumberOfLines = lines
        field.lineBreakMode = lines == 1 ? .byTruncatingTail : .byWordWrapping
        field.cell?.wraps = lines != 1
        field.setAccessibilityLabel(text)
        return field
    }

    static func button(_ title: String, target: AnyObject?, action: Selector?) -> NSButton {
        let button = NSButton(title: title, target: target, action: action)
        button.bezelStyle = .rounded
        button.controlSize = .regular
        button.setAccessibilityLabel(title)
        return button
    }

    static func scrollableText(editable: Bool = false, monospaced: Bool = false) -> (NSScrollView, NSTextView) {
        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = monospaced
        scroll.autohidesScrollers = true
        scroll.borderType = .noBorder
        let text = NSTextView()
        text.isEditable = editable
        text.isSelectable = true
        text.drawsBackground = true
        text.backgroundColor = WorkbenchPalette.elevatedSurface
        text.textColor = WorkbenchPalette.primaryText
        text.font = monospaced ? .monospacedSystemFont(ofSize: 12, weight: .regular) : .systemFont(ofSize: 13)
        text.textContainerInset = NSSize(width: 10, height: 10)
        text.isRichText = false
        text.isAutomaticQuoteSubstitutionEnabled = false
        text.isAutomaticDashSubstitutionEnabled = false
        text.isAutomaticTextReplacementEnabled = false
        if !editable { text.isAutomaticSpellingCorrectionEnabled = false }
        scroll.documentView = text
        return (scroll, text)
    }

    static func stack(_ orientation: NSUserInterfaceLayoutOrientation, spacing: CGFloat = 8) -> NSStackView {
        let stack = NSStackView()
        stack.orientation = orientation
        stack.spacing = spacing
        stack.alignment = orientation == .horizontal ? .centerY : .leading
        stack.distribution = .fill
        return stack
    }
}
