import Foundation
import XCTest
@testable import SymDesk

/// The View menu's "Dashboard" command posted `openDiscover`, so choosing it
/// opened the capabilities screen instead of the Dashboard (issue #443).
/// These pin the two destinations as separate names so they cannot be
/// collapsed back into one.
final class MenuCommandNotificationsTests: XCTestCase {
    func testDashboardAndDiscoverAreDistinctDestinations() {
        XCTAssertNotEqual(
            Notification.Name.openDashboard,
            Notification.Name.openDiscover,
            "the Dashboard and Discover screens must be reachable independently"
        )
    }

    func testMenuCommandNotificationNamesAreStable() {
        XCTAssertEqual(Notification.Name.openDashboard.rawValue, "symdesk.openDashboard")
        XCTAssertEqual(Notification.Name.openDiscover.rawValue, "symdesk.openDiscover")
    }
}
