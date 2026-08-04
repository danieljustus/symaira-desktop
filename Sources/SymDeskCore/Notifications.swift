import Foundation

extension Notification.Name {
    /// Posted by `DeskCore` after the active vault changed to a different
    /// registered vault (switch, create) so the app shell can restart the
    /// event watcher and reload all vault-derived UI state (issue #296).
    public static let vaultSwitched = Notification.Name("symdesk.vaultSwitched")

    /// Posted when the active vault association is cleared from within the
    /// running app (changing/resetting a local vault, leaving demo mode) so
    /// onboarding should reappear in-place, without a relaunch.
    public static let vaultReset = Notification.Name("symdesk.vaultReset")
}
