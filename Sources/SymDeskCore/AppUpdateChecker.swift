import Foundation
import SymairaUpdateCheck

public enum AppUpdateStatus: Equatable, Sendable {
    case unknown
    case upToDate
    case available(ReleaseInfo)
    case skipped(ReleaseInfo)
    case error(String)
}

/// Persists which release versions the user dismissed, so they are not re-prompted for them.
public protocol SkippedVersionStore: Sendable {
    func skippedTag() -> String?
    func setSkippedTag(_ tag: String?)
}

public struct UserDefaultsSkippedVersionStore: SkippedVersionStore, @unchecked Sendable {
    private static let key = "com.symaira.desktop.updateSkippedTag"
    private let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    public func skippedTag() -> String? {
        defaults.string(forKey: Self.key)
    }

    public func setSkippedTag(_ tag: String?) {
        if let tag {
            defaults.set(tag, forKey: Self.key)
        } else {
            defaults.removeObject(forKey: Self.key)
        }
    }
}

/// Checks for a newer SymDesk release and gates re-prompting for a version the user already skipped.
@MainActor
public final class AppUpdateChecker: ObservableObject {
    public static let shared = AppUpdateChecker()

    @Published public private(set) var status: AppUpdateStatus = .unknown

    private let checker: UpdateChecker
    private let store: SkippedVersionStore
    private let currentVersion: () -> String

    public init(
        checker: UpdateChecker = UpdateChecker(owner: "danieljustus", repo: "symaira-desktop"),
        store: SkippedVersionStore = UserDefaultsSkippedVersionStore(),
        currentVersion: @escaping () -> String = {
            Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "0.0.0"
        }
    ) {
        self.checker = checker
        self.store = store
        self.currentVersion = currentVersion
    }

    /// Check for a newer release. `force` bypasses both the disk cache and the skip gate.
    public func checkForUpdate(force: Bool = false) async {
        do {
            guard let release = try await checker.check(currentVersion: currentVersion(), force: force) else {
                status = .upToDate
                return
            }
            if !force, store.skippedTag() == release.tagName {
                status = .skipped(release)
            } else {
                status = .available(release)
            }
        } catch {
            status = .error(String(describing: error))
        }
    }

    /// Dismiss a specific release so the user is not re-prompted for it.
    public func skip(_ release: ReleaseInfo) {
        store.setSkippedTag(release.tagName)
        status = .skipped(release)
    }
}
