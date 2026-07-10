import Foundation

/// A notification to fire when a document's due date approaches.
public struct DueDateNotification: Identifiable, Equatable, Sendable {
    public let id: String
    public let title: String
    public let body: String
    public let fireDate: Date
    public let documentPath: String

    public init(id: String, title: String, body: String, fireDate: Date, documentPath: String) {
        self.id = id
        self.title = title
        self.body = body
        self.fireDate = fireDate
        self.documentPath = documentPath
    }
}

/// Pure-logic scheduler that determines which documents need notifications.
///
/// This struct has no dependency on `UNUserNotificationCenter`; the app layer
/// translates its output into actual notification requests.
public struct NotificationScheduler: Sendable {
    public let leadTimeDays: Int

    public init(leadTimeDays: Int = 1) {
        self.leadTimeDays = leadTimeDays
    }

    /// Returns notifications for documents whose due date is within `leadTimeDays`
    /// from now (i.e. the fire date is `dueDate - leadTimeDays`).
    ///
    /// Documents with empty or unparseable due dates are skipped.
    /// Only notifications whose fire date is still in the future are returned.
    public func upcomingDueNotifications(from documents: [DocumentItem]) -> [DueDateNotification] {
        let calendar = Calendar.current
        let now = Date()
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withFullDate]

        var notifications: [DueDateNotification] = []

        for doc in documents where !doc.dueDate.isEmpty {
            guard let dueDate = formatter.date(from: doc.dueDate) else { continue }

            guard let fireDate = calendar.date(byAdding: .day, value: -leadTimeDays, to: dueDate) else { continue }

            // Only schedule if the fire date is today or in the future
            let startOfToday = calendar.startOfDay(for: now)
            let startOfFireDate = calendar.startOfDay(for: fireDate)
            guard startOfFireDate >= startOfToday else { continue }

            let daysUntilDue = calendar.dateComponents([.day], from: now, to: dueDate).day ?? 0

            let title: String
            let body: String

            if daysUntilDue == 0 {
                title = "Document due today"
                body = "\"\(doc.title)\" is due today."
            } else if daysUntilDue == 1 {
                title = "Document due tomorrow"
                body = "\"\(doc.title)\" is due tomorrow."
            } else {
                title = "Document due in \(daysUntilDue) days"
                body = "\"\(doc.title)\" is due on \(doc.dueDate)."
            }

            notifications.append(DueDateNotification(
                id: doc.path,
                title: title,
                body: body,
                fireDate: fireDate,
                documentPath: doc.path
            ))
        }

        return notifications.sorted { $0.fireDate < $1.fireDate }
    }

    /// Returns a notification payload for the review queue badge, or nil if empty.
    public func reviewQueueNotification(count: Int) -> (title: String, body: String)? {
        guard count > 0 else { return nil }
        return (
            title: "\(count) document\(count == 1 ? " needs" : "s need") review",
            body: count == 1
                ? "There is 1 document in the review queue."
                : "There are \(count) documents in the review queue."
        )
    }
}
