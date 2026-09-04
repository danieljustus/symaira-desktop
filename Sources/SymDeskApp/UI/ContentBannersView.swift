import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore

// MARK: - App Error Message

/// A short-lived, dismissible error banner for app-level failures that would
/// previously have been visible only in the Xcode console (print() calls).
struct AppErrorMessage: Identifiable, Equatable {
    let id: UUID
    let message: String
    var detail: String?

    init(id: UUID = UUID(), message: String, detail: String? = nil) {
        self.id = id
        self.message = message
        self.detail = detail
    }

    static func == (lhs: AppErrorMessage, rhs: AppErrorMessage) -> Bool {
        lhs.id == rhs.id
    }
}

// MARK: - Version Mismatch Banner

/// Persistent, dismissible banner shown when the installed `symdesk` CLI is
/// older than the app version. An older CLI silently applies older vault rules,
/// so the mismatch is surfaced rather than ignored (issue #246).
struct VersionMismatchBanner: View {
    let appVersion: String
    let coreVersion: String
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(SymairaTheme.goldPrimary)
            VStack(alignment: .leading, spacing: 1) {
                Text("CLI version mismatch")
                    .symairaText(.caption).fontWeight(.semibold)
                    .foregroundStyle(SymairaTheme.textPrimary)
                Text("App v\(appVersion) is driving CLI v\(coreVersion). Run `brew upgrade symdesk` to update.")
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            Spacer(minLength: 12)
            Button(action: dismiss) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(SymairaTheme.textSecondary)
            .help("Dismiss version mismatch warning")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
        .padding(.horizontal, 16)
        .padding(.top, 10)
        .accessibilityElement(children: .contain)
    }
}

// MARK: - Notification Permission Denied Banner

struct NotificationDeniedBanner: View {
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "bell.slash.fill")
                .foregroundStyle(SymairaTheme.goldPrimary)
            VStack(alignment: .leading, spacing: 1) {
                Text("Notifications are off")
                    .symairaText(.caption).fontWeight(.semibold)
                    .foregroundStyle(SymairaTheme.textPrimary)
                Text("Enable them in System Settings to receive review reminders.")
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            Spacer(minLength: 12)
            Button("Open Settings") {
                if let url = URL(string: "x-apple.systempreferences:com.apple.preference.notifications?PrivacyNotificationCenter") {
                    NSWorkspace.shared.open(url)
                }
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            Button(action: dismiss) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(SymairaTheme.textSecondary)
            .help("Dismiss notification reminder")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
        .padding(.horizontal, 16)
        .padding(.top, 10)
        .accessibilityElement(children: .contain)
    }
}

// MARK: - App Error Banner

struct AppErrorBanner: View {
    let error: AppErrorMessage
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(SymairaTheme.goldPrimary)
            VStack(alignment: .leading, spacing: 1) {
                Text(error.message)
                    .symairaText(.caption).fontWeight(.semibold)
                    .foregroundStyle(SymairaTheme.textPrimary)
                if let detail = error.detail {
                    Text(detail)
                        .symairaText(.caption)
                        .foregroundStyle(SymairaTheme.textSecondary)
                }
            }
            Spacer(minLength: 12)
            Button(action: dismiss) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(SymairaTheme.textSecondary)
            .help("Dismiss error")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
        .padding(.horizontal, 16)
        .padding(.top, 10)
        .accessibilityElement(children: .contain)
        .transition(.move(edge: .top).combined(with: .opacity))
    }
}

// MARK: - Load Error Banner

/// Red banner shown when a note's backing file could not be read (issue
/// #650): names the file, offers Retry, and — when the note is known —
/// "Remove from index" to reconcile the stale sidecar entry away.
struct LoadErrorBanner: View {
    let message: String
    let showsRemoveAction: Bool
    let onRetry: () -> Void
    let onRemoveFromIndex: () -> Void
    let onDismiss: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.red)
            Text(message)
                .symairaText(.caption)
                .foregroundColor(SymairaTheme.textSecondary)
            Spacer()
            Button("Retry", action: onRetry)
                .buttonStyle(.bordered)
                .controlSize(.small)
            if showsRemoveAction {
                Button("Remove from index", action: onRemoveFromIndex)
                    .buttonStyle(.bordered)
                    .controlSize(.small)
            }
            Button(action: onDismiss) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(SymairaTheme.textSecondary)
        }
        .padding(8)
        .background(Color.red.opacity(0.12))
        .cornerRadius(6)
        .padding(.horizontal)
        .padding(.top, 8)
    }
}

// MARK: - Top Banners View

/// Renders the persistent and dismissible overlay banners at the top of ContentView.
struct ContentTopBannersView: View {
    @ObservedObject var model: ContentViewModel
    @EnvironmentObject var core: DeskCore
    @EnvironmentObject var notificationManager: NotificationManager

    @AppStorage("dismissedNotificationPermissionBanner") private var dismissedNotificationPermissionBanner = false
    @AppStorage("dismissedVersionMismatchBanner") private var dismissedVersionMismatchBanner = false

    var body: some View {
        VStack(spacing: 0) {
            if !dismissedVersionMismatchBanner, let coreVer = core.coreVersion {
                let appVer = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
                if !appVer.isEmpty && coreVer != appVer && appVer.compare(coreVer, options: .numeric) == .orderedDescending {
                    VersionMismatchBanner(appVersion: appVer, coreVersion: coreVer) {
                        dismissedVersionMismatchBanner = true
                    }
                }
            }
            if notificationManager.isDenied && !dismissedNotificationPermissionBanner {
                NotificationDeniedBanner {
                    dismissedNotificationPermissionBanner = true
                }
            }
            // Ephemeral error banners — dismissible, shown for app-level failures
            // that would previously have been print()-only console errors.
            ForEach(model.appErrors) { err in
                AppErrorBanner(error: err) {
                    model.dismissAppError(err)
                }
            }
        }
    }
}
