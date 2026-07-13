import Foundation
@preconcurrency import UserNotifications
import SymDeskCore

@MainActor
final class NotificationManager: NSObject, ObservableObject {
    static let shared = NotificationManager()

    @Published var permissionStatus: UNAuthorizationStatus = .notDetermined
    @Published var deepLinkedDocumentPath: String?

    var isDenied: Bool { permissionStatus == .denied }
    var isAuthorized: Bool {
        permissionStatus == .authorized || permissionStatus == .provisional
    }

    private let scheduler = NotificationScheduler(leadTimeDays: 1)
    let center: NotificationCenterProviding

    init(center: NotificationCenterProviding = UNUserNotificationCenter.current()) {
        self.center = center
        super.init()
        (center as? UNUserNotificationCenter)?.delegate = self
    }

    // MARK: - Permission

    func requestPermission() {
        Task { [weak self] in
            _ = try? await self?.center.requestAuthorization(options: [.alert, .badge, .sound])
            await self?.checkPermissionStatus()
        }
    }

    func checkPermissionStatus() async {
        let settings = await center.notificationSettings()
        permissionStatus = settings.authorizationStatus
    }

    // MARK: - Scheduling

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

    func scheduleNotifications(documents: [DocumentItem], reviewCount: Int) {
        center.removeAllPendingNotificationRequests()

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

        let badgeCount = reviewCount
        Task {
            try? await center.setBadgeCount(badgeCount)
        }
    }

    func cancelAll() {
        center.removeAllPendingNotificationRequests()
        Task {
            try? await center.setBadgeCount(0)
        }
    }
}

// MARK: - UNUserNotificationCenterDelegate

extension NotificationManager: UNUserNotificationCenterDelegate {
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }

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

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        openSettingsFor notification: UNNotification?
    ) {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.notifications?PrivacyNotificationCenter") {
            Task { @MainActor in
                NSWorkspace.shared.open(url)
            }
        }
    }
}
