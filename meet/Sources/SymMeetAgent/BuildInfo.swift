import Foundation

/// Version info for the SymMeetAgent app bundle.
///
/// Reads `CFBundleShortVersionString` from the Info.plist at runtime.
/// Falls back to `"0.0.0"` when running outside a bundle (e.g. from
/// `swift run` or Xcode previews).
enum BuildInfo {
  static let version: String = {
    Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "0.0.0"
  }()
}
