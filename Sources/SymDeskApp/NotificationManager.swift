import Foundation
import AppKit
@preconcurrency import UserNotifications
import SymDeskCore

/// Manages macOS user notifications for due-date reminders and the review queue.
///
/// Owns `UNUserNotificationCenter` permission flow, schedules local notifications,
/// and exposes deep-link state so the SwiftUI layer can navigate to a document.
@MainActor
final class NotificationManager: NSObject, ObservableObject {
    static let shared = NotificationManager()

    @Published var permissionStatus: UNAuthorizationStatus = .notDetermined

    /// When the user taps a notification, this is set to the document path.
    /// ContentView observes it and opens the document.
    @Published var deepLinkedDocumentPath: String?

    /// Whether the in-app permission-denied banner should be shown.
    var isDenied: Bool { permissionStatus == .denied }

    /// Whether notifications are enabled (authorized or provisional).
    var isAuthorized: Bool {
        permissionStatus == .authorized || permissionStatus == .provisional
    }

    private let scheduler = NotificationScheduler(leadTimeDays: 1)

    private override init() {
        super.init()
        UNUserNotificationCenter.current().delegate = self
    }

    // MARK: - Permission

    func requestPermission() {
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .badge, .sound]) { [weak self] _, _ in
            Task { @MainActor [weak self] in
                self?.checkPermissionStatus()
            }
        }
        checkPermissionStatus()
    }

    func checkPermissionStatus() {
        UNUserNotificationCenter.current().getNotificationSettings { [weak self] settings in
            let status = settings.authorizationStatus
            Task { @MainActor [weak self] in
                self?.permissionStatus = status
            }
        }
    }

    // MARK: - Scheduling

    /// Fetches documents and review queue from the core, then cancels all
    /// pending notifications and reschedules fresh ones.
    func refreshNotifications(with core: DeskCore) async {
        guard isAuthorized else { return }

        do {
            let documents = try await core.docsList()
            let reviewDocs = try await core.docsReview()

            scheduleNotifications(documents: documents, reviewCount: reviewDocs.count)
        } catch {
            print("NotificationManager: refresh failed – \(error)")
        }
    }

    /// Schedules notifications for the given documents and updates the dock badge.
    func scheduleNotifications(documents: [DocumentItem], reviewCount: Int) {
        let center = UNUserNotificationCenter.current()

        // Cancel all previously scheduled notifications so we start clean
        center.removeAllPendingNotificationRequests()

        // Due-date notifications
        let dueNotifications = scheduler.upcomingDueNotifications(from: documents)
        for note in dueNotifications {
            let content = UNMutableNotificationContent()
            content.title = note.title
            content.body = note.body
            content.sound = .default
            content.categoryIdentifier = "due_date"
            content.userInfo = ["documentPath": note.documentPath]

            let components = Calendar.current.dateComponents(
                [.year, .month, .day, .hour, .minute],
                from: note.fireDate
            )
            let trigger = UNCalendarNotificationTrigger(dateMatching: components, repeats: false)

            let request = UNNotificationRequest(
                identifier: "due-\(note.id.hashValue)",
                content: content,
                trigger: trigger
            )
            center.add(request)
        }

        // Dock badge for review queue
        let badgeCount = reviewCount
        Task {
            try? await center.setBadgeCount(badgeCount)
        }
    }

    /// Cancels all pending notifications and clears the badge.
    func cancelAll() {
        let center = UNUserNotificationCenter.current()
        center.removeAllPendingNotificationRequests()
        Task {
            try? await center.setBadgeCount(0)
        }
    }
}

// MARK: - UNUserNotificationCenterDelegate

extension NotificationManager: UNUserNotificationCenterDelegate {
    /// Called when the user taps a notification while the app is in the foreground.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }

    /// Called when the user taps a notification (app in background or foreground).
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let userInfo = response.notification.request.content.userInfo
        if let docPath = userInfo["documentPath"] as? String {
            Task { @MainActor [weak self] in
                self?.deepLinkedDocumentPath = docPath
            }
        }
        completionHandler()
    }

    /// Opens System Settings for notifications when the user clicks the Settings
    /// button in the notification permission prompt.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        openSettingsFor notification: UNNotification?
    ) {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.notifications?PrivacyNotificationCenter") {
            NSWorkspace.shared.open(url)
        }
    }
}
