import AppIntents
import Foundation

// This file is compiled into BOTH the app target and the widget target
// (declared in project.yml), so the same intent types are available to
// Shortcuts/Siri and to widget buttons without duplication.

/// Actions that can arrive while the app is not foregrounded — from
/// Shortcuts/Siri (App Intents), the home-screen widget (button intents)
/// or the home-screen quick actions. They are parked in the shared App
/// Group container; the app observes the store on launch/activation and
/// presents the right surface. The pending action survives app relaunch
/// (e.g. a widget tap that launches a cold start).
enum MobileAppAction: String, Codable, Sendable {
    case newNote
    case scanDocument
}

/// App-Group-backed store for pending actions. The same container is used
/// by the share extension (#327) and the widget (#328) — one shared
/// container decision for all of them.
enum MobileAppActionStore {
    /// Shared App Group container. `var` so tests can isolate the suite.
    nonisolated(unsafe) static var suiteName = "group.com.symaira.desktop.ios"
    static let didSetNotification = Notification.Name("symdesk.mobile.app-action-set")
    private static let key = "symdesk.mobile.pending-action.v1"

    static func defaults() -> UserDefaults {
        UserDefaults(suiteName: suiteName) ?? .standard
    }

    static func pending() -> MobileAppAction? {
        guard let raw = defaults().string(forKey: key) else { return nil }
        return MobileAppAction(rawValue: raw)
    }

    /// Parks the action and notifies observers (the workspace presents the
    /// surface immediately when the app is already running).
    static func set(_ action: MobileAppAction) {
        defaults().set(action.rawValue, forKey: key)
        NotificationCenter.default.post(name: didSetNotification, object: action)
    }

    static func clear() {
        defaults().removeObject(forKey: key)
    }
}

/// "New note" — opens the composer. Discoverable in Shortcuts and by
/// voice; also used by the widget's new-note button. Opens the app when
/// run from a widget.
struct OpenNewNoteIntent: AppIntent {
    static var title: LocalizedStringResource { "New note" }
    static var description: IntentDescription {
        "Opens the SymDesk composer to write a new note."
    }
    static var openAppWhenRun: Bool { true }

    func perform() async throws -> some IntentResult {
        MobileAppActionStore.set(.newNote)
        return .result()
    }
}

/// "Scan document" — opens the document scanner. Discoverable in
/// Shortcuts and by voice; also used by the widget's scan button and the
/// lock-screen accessory.
struct OpenScanDocumentIntent: AppIntent {
    static var title: LocalizedStringResource { "Scan document" }
    static var description: IntentDescription {
        "Opens the SymDesk document scanner to capture a document."
    }
    static var openAppWhenRun: Bool { true }

    func perform() async throws -> some IntentResult {
        MobileAppActionStore.set(.scanDocument)
        return .result()
    }
}
