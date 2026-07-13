import Foundation
import XCTest
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

    func notificationSettings() async -> UNNotificationSettings {
        await MainActor.run {
            let settings = FakeNotificationSettings(status: authStatus)
            return settings
        }
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
private final class FakeNotificationSettings: UNNotificationSettings {
    private let status: UNAuthorizationStatus

    init(status: UNAuthorizationStatus) {
        self.status = status
    }

    override var authorizationStatus: UNAuthorizationStatus { status }
    override var soundSetting: UNNotificationSetting { .enabled }
    override var badgeSetting: UNNotificationSetting { .enabled }
    override var alertSetting: UNNotificationSetting { .enabled }
    override var notificationCenterSetting: UNNotificationSetting { .enabled }
    override var lockScreenSetting: UNNotificationSetting { .enabled }
    override var carPlaySetting: UNNotificationSetting { .notSupported }
    override var criticalAlertSetting: UNNotificationSetting { .notSupported }
    override var showPreviewsSetting: UNShowPreviewsSetting { .always }
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

        let doc = DocumentItem(
            id: "test-id",
            title: "Test Doc",
            fileName: "test.md",
            filePath: "test.md",
            status: .open,
            createdAt: Date(),
            updatedAt: Date(),
            tags: [],
            metadata: [:]
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
