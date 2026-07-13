import Foundation
@preconcurrency import UserNotifications

protocol NotificationCenterProviding: Sendable {
    func requestAuthorization(options: UNAuthorizationOptions) async throws -> Bool
    func notificationSettings() async -> UNNotificationSettings
    func removeAllPendingNotificationRequests()
    func add(_ request: UNNotificationRequest)
    func setBadgeCount(_ newBadgeCount: Int) async throws
}

extension UNUserNotificationCenter: NotificationCenterProviding {}
