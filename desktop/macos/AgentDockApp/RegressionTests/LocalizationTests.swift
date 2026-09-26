import XCTest
@testable import WorkbenchKit

final class LocalizationTests: XCTestCase {
    func testWorkbenchUsesRealChineseAndEnglishResources() {
        let previous = L10n.languagePreference()
        defer { L10n.setLanguagePreference(previous) }
        L10n.setLanguagePreference(.simplifiedChinese)
        XCTAssertEqual(L10n.text("Search conversations"), "搜索对话")
        XCTAssertEqual(L10n.text("Enable custom permission settings"), "启用自定义权限设置")
        XCTAssertEqual(L10n.format("Revision: %@ · Source: %@", "7", "execution_mode"), "修订：7 · 来源：execution_mode")
        L10n.setLanguagePreference(.english)
        XCTAssertEqual(L10n.text("Search conversations"), "Search conversations")
        XCTAssertEqual(L10n.text("Enable custom permission settings"), "Enable custom permission settings")
        XCTAssertEqual(L10n.format("Revision: %@ · Source: %@", "7", "execution_mode"), "Revision: 7 · Source: execution_mode")
    }
}
