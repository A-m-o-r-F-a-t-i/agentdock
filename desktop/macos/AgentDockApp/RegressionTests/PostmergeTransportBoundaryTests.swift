import XCTest
import Foundation
@testable import WorkbenchKit

final class PostmergeTransportBoundaryTests: XCTestCase {
    func testIPv6LoopbackConnectionIsAccepted() throws {
        let url = try XCTUnwrap(URL(string: "http://[::1]:18765/"))
        let connection = try WorkbenchConnection(baseURL: url, bearerToken: "isolated-ipv6-fixture")
        XCTAssertEqual(connection.baseURL, url)
    }

    func testNonLoopbackIPv6ConnectionStaysRejected() throws {
        for address in ["::2", "2001:db8::1"] {
            let url = try XCTUnwrap(URL(string: "http://[\(address)]:18765/"))
            XCTAssertThrowsError(try WorkbenchConnection(baseURL: url, bearerToken: "isolated-ipv6-fixture"))
        }
    }
}
