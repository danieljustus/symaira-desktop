/// Compile-time version embedding for release builds.
///
/// `scripts/build-release.sh` rewrites this file during release builds,
/// replacing `nil` with the tag version. **Never commit a non-nil value.**
/// The committed default (`nil`) means debug/dev builds fall through to the
/// `SYMMEET_VERSION` environment variable or the hardcoded dev version.
enum EmbeddedRelease {
  static let version: String? = nil
}
