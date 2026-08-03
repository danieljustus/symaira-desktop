import SwiftUI
import UIKit

@main
struct SymDeskMobileApp: App {
    @UIApplicationDelegateAdaptor(MobileAppDelegate.self) private var appDelegate
    @StateObject private var vault = MobileVaultStore()

    var body: some Scene {
        WindowGroup {
            MobileRootView()
                .environmentObject(vault)
                .preferredColorScheme(.dark)
                .tint(MobileTheme.gold)
                .onOpenURL { url in
                    // Spotlight deep link or manual symdesk://open/<path>.
                    vault.openDeepLink(url)
                }
                .onContinueUserActivity(MobileNoteActivity.activityType) { activity in
                    // Handoff from another device (or Siri suggestion).
                    if let url = activity.webpageURL ?? MobileNoteActivity.url(from: activity) {
                        vault.openDeepLink(url)
                    }
                }
        }
    }
}

/// Routes home-screen quick actions (long-press icon) into the shared
/// action store; the workspace presents the surface on activation.
final class MobileAppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        performActionFor shortcutItem: UIApplicationShortcutItem,
        completionHandler: @escaping (Bool) -> Void
    ) {
        switch shortcutItem.type {
        case "com.symaira.desktop.ios.new-note":
            MobileAppActionStore.set(.newNote)
        case "com.symaira.desktop.ios.scan":
            MobileAppActionStore.set(.scanDocument)
        default:
            break
        }
        completionHandler(true)
    }
}

/// NSUserActivity donation for opened notes: enables Handoff to the Mac
/// app and Siri Suggestions. The activity carries the same deep link the
/// Spotlight index uses, so every entry point resolves to one open path.
enum MobileNoteActivity {
    static let activityType = "com.symaira.desktop.ios.open-note"

    static func donate(for note: MobileNote) {
        let activity = NSUserActivity(activityType: activityType)
        activity.title = note.title
        activity.webpageURL = MobileSpotlightIndexer.deepLink(for: note)
        activity.userInfo = ["path": note.path]
        activity.isEligibleForHandoff = true
        activity.isEligibleForSearch = true
        activity.isEligibleForPrediction = true
        activity.becomeCurrent()
    }

    static func url(from activity: NSUserActivity) -> URL? {
        if let url = activity.webpageURL { return url }
        guard let path = activity.userInfo?["path"] as? String else { return nil }
        return MobileSpotlightIndexer.deepLink(forPath: path)
    }
}
