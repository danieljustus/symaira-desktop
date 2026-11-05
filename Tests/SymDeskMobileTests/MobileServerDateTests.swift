import XCTest
@testable import SymDeskMobile

/// The server writes snapshot `modified_at` as RFC 3339 with nanoseconds (Go
/// `RFC3339Nano`; the Rust core keeps the same text). `.iso8601` cannot parse
/// that, so `MobileRemoteClient` uses `MobileServerDate`.
final class MobileServerDateTests: XCTestCase {
    func testParsesNanosecondRFC3339() throws {
        let date = try XCTUnwrap(MobileServerDate.parse("2026-11-05T08:12:18.302234634Z"))
        XCTAssertEqual(date.timeIntervalSince1970, 1_793_866_338.302234634, accuracy: 0.000_01)
    }

    func testParsesWholeSecondsAndOffsets() throws {
        let utc = try XCTUnwrap(MobileServerDate.parse("2026-01-02T03:04:05Z"))
        XCTAssertEqual(utc.timeIntervalSince1970, 1_767_323_045, accuracy: 0.001)
        let offset = try XCTUnwrap(MobileServerDate.parse("2026-01-02T04:04:05.5+01:00"))
        XCTAssertEqual(offset.timeIntervalSince1970, 1_767_323_045.5, accuracy: 0.001)
    }

    func testRejectsNonRFC3339() {
        XCTAssertNil(MobileServerDate.parse("2026-01-02"))
        XCTAssertNil(MobileServerDate.parse("yesterday"))
        XCTAssertNil(MobileServerDate.parse("2026-01-02T03:04:05"))
    }
}
