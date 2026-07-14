import Foundation
import XCTest
@testable import SymDesk
@testable import SymDeskCore
@preconcurrency import UserNotifications

@MainActor
private final class FakeNotificationCenter: NotificationCenterProviding {
    var requestedOptions: UNAuthorizationOptions?
    var requestResult: Bool = true
    var authStatus: UNAuthorizationStatus = .notDetermined
    var addedRequests: [UNNotificationRequest] = []
    var removedAll = false
    var badgeCount: Int?

    func requestAuthorization(options: UNAuthorizationOptions) async throws -> Bool {
        requestedOptions = options
        return requestResult
    }

    func authorizationStatus() async -> UNAuthorizationStatus {
        authStatus
    }

    func removeAllPendingNotificationRequests() {
        removedAll = true
    }

    func add(_ request: UNNotificationRequest) {
        addedRequests.append(request)
    }

    func setBadgeCount(_ newBadgeCount: Int) async throws {
        badgeCount = newBadgeCount
    }
}

@MainActor
final class NotificationManagerTests: XCTestCase {
    func testRequestPermissionCallsCenter() async {
        let fake = FakeNotificationCenter()
        let manager = NotificationManager(center: fake)

        manager.requestPermission()
        try? await Task.sleep(for: .milliseconds(50))

        XCTAssertEqual(fake.requestedOptions, [.alert, .badge, .sound])
    }

    func testCheckPermissionStatusUpdatesFromCenter() async {
        let fake = FakeNotificationCenter()
        fake.authStatus = .authorized
        let manager = NotificationManager(center: fake)

        await manager.checkPermissionStatus()

        XCTAssertEqual(manager.permissionStatus, .authorized)
        XCTAssertTrue(manager.isAuthorized)
        XCTAssertFalse(manager.isDenied)
    }

    func testIsDeniedReturnsTrueForDeniedStatus() async {
        let fake = FakeNotificationCenter()
        fake.authStatus = .denied
        let manager = NotificationManager(center: fake)

        await manager.checkPermissionStatus()

        XCTAssertTrue(manager.isDenied)
        XCTAssertFalse(manager.isAuthorized)
    }

    func testScheduleNotificationsAddsRequests() {
        let fake = FakeNotificationCenter()
        let manager = NotificationManager(center: fake)

        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withFullDate]
        let dueDate = formatter.string(from: Calendar.current.date(byAdding: .day, value: 2, to: Date())!)
        let doc = DocumentItem(
            path: "test.md",
            title: "Test Doc",
            documentDate: "",
            person: "",
            status: "open",
            dueDate: dueDate,
            confidence: 95,
            correspondent: "",
            documentType: "invoice"
        )

        manager.scheduleNotifications(documents: [doc], reviewCount: 2)

        XCTAssertTrue(fake.removedAll)
        XCTAssertFalse(fake.addedRequests.isEmpty)
    }

    func testCancelAllRemovesAndClearsBadge() {
        let fake = FakeNotificationCenter()
        let manager = NotificationManager(center: fake)

        manager.cancelAll()

        XCTAssertTrue(fake.removedAll)
        Task {
            try? await Task.sleep(for: .milliseconds(50))
            XCTAssertEqual(fake.badgeCount, 0)
        }
    }
}
