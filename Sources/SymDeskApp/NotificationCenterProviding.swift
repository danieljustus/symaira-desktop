import Foundation
@preconcurrency import UserNotifications

@MainActor
protocol NotificationCenterProviding: AnyObject {
    func requestAuthorization(options: UNAuthorizationOptions) async throws -> Bool
    func authorizationStatus() async -> UNAuthorizationStatus
    func removeAllPendingNotificationRequests()
    func add(_ request: UNNotificationRequest)
    func setBadgeCount(_ newBadgeCount: Int) async throws
}

@MainActor
final class UserNotificationCenterAdapter: NSObject, NotificationCenterProviding {
    private let center: UNUserNotificationCenter

    init(center: UNUserNotificationCenter = .current()) {
        self.center = center
        super.init()
    }

    func requestAuthorization(options: UNAuthorizationOptions) async throws -> Bool {
        try await center.requestAuthorization(options: options)
    }

    func authorizationStatus() async -> UNAuthorizationStatus {
        await center.notificationSettings().authorizationStatus
    }

    func removeAllPendingNotificationRequests() {
        center.removeAllPendingNotificationRequests()
    }

    func add(_ request: UNNotificationRequest) {
        center.add(request)
    }

    func setBadgeCount(_ newBadgeCount: Int) async throws {
        try await center.setBadgeCount(newBadgeCount)
    }
}
