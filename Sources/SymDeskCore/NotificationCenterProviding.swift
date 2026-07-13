import Foundation
@preconcurrency import UserNotifications

protocol NotificationCenterProviding {
    @MainActor func requestAuthorization(options: UNAuthorizationOptions) async throws -> Bool
    @MainActor func notificationSettings() async -> UNNotificationSettings
    func removeAllPendingNotificationRequests()
    func add(_ request: UNNotificationRequest)
    @MainActor func setBadgeCount(_ newBadgeCount: Int) async throws
}

extension UNUserNotificationCenter: NotificationCenterProviding {}
