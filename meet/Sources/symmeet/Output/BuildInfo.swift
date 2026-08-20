import Foundation

enum BuildInfo {
  /// Resolved version with a three-tier fallback:
  /// 1. Compile-time embed (written by `scripts/build-release.sh`)
  /// 2. `SYMMEET_VERSION` environment variable
  /// 3. Hardcoded dev default
  static let version =
    EmbeddedRelease.version
    ?? ProcessInfo.processInfo.environment["SYMMEET_VERSION"]
    ?? "0.1.0-dev"
}
