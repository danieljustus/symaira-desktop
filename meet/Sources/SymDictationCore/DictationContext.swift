import Foundation

/// Where a dictation is going to land.
///
/// The pipeline only needs two facts about the target: which app receives the text and, for
/// browsers, which site. Both are optional because the iPhone keyboard extension often cannot
/// learn its host app, and a native Mac app has no host.
public struct DictationContext: Equatable, Sendable {
  /// Bundle identifier of the receiving app, e.g. `com.apple.mail`.
  public var appBundleID: String?
  /// Host of the focused web page, e.g. `github.com`.
  public var host: String?

  public init(appBundleID: String? = nil, host: String? = nil) {
    self.appBundleID = appBundleID
    self.host = host
  }
}

/// The scope of a correction section in `corrections.md`.
///
/// Corrections without a scope are global. A scope is either an app (bundle identifier) or a
/// web host (`host:<domain>`); the host form exists because one browser bundle id hosts many
/// very different targets (a chat site and a code review site want different spellings).
public enum DictationScope: Hashable, Sendable {
  case app(String)
  case host(String)

  static let hostPrefix = "host:"

  /// Parses the text of a `## <scope>` heading. Returns `nil` for an empty scope.
  public init?(heading: String) {
    let text = heading.trimmingCharacters(in: .whitespaces)
    guard !text.isEmpty else { return nil }
    if text.lowercased().hasPrefix(Self.hostPrefix) {
      let domain = String(text.dropFirst(Self.hostPrefix.count))
        .trimmingCharacters(in: .whitespaces)
      guard !domain.isEmpty else { return nil }
      self = .host(domain)
    } else {
      self = .app(text)
    }
  }

  /// The heading text that parses back to the same scope.
  public var headingText: String {
    switch self {
    case .app(let bundleID): return bundleID
    case .host(let domain): return Self.hostPrefix + domain
    }
  }

  /// Whether this scope applies to `context`. Matching is case-insensitive; a host scope also
  /// matches subdomains (`github.com` matches `gist.github.com`) because users think in sites.
  func matches(_ context: DictationContext) -> Bool {
    switch self {
    case .app(let bundleID):
      guard let current = context.appBundleID else { return false }
      return current.caseInsensitiveCompare(bundleID) == .orderedSame
    case .host(let domain):
      guard let current = context.host?.lowercased(), !current.isEmpty else { return false }
      let wanted = domain.lowercased()
      return current == wanted || current.hasSuffix("." + wanted)
    }
  }
}

/// One `wrong -> right` replacement, global (`scope == nil`) or scoped.
public struct DictationCorrection: Hashable, Sendable {
  public var wrong: String
  public var right: String
  public var scope: DictationScope?

  public init(wrong: String, right: String, scope: DictationScope? = nil) {
    self.wrong = wrong
    self.right = right
    self.scope = scope
  }
}

/// A line in a Markdown settings file that could not be understood.
///
/// Settings files are hand-edited in the user's vault, so bad lines are reported, not thrown:
/// one typo must not disable dictation, and the line stays in the file untouched.
public struct DictationParseWarning: Hashable, Sendable {
  /// 1-based line number in the parsed document.
  public var line: Int
  public var message: String

  public init(line: Int, message: String) {
    self.line = line
    self.message = message
  }
}

/// A parsed value together with the warnings collected while parsing it.
public struct DictationParseResult<Value: Sendable>: Sendable {
  public var value: Value
  public var warnings: [DictationParseWarning]

  public init(value: Value, warnings: [DictationParseWarning] = []) {
    self.value = value
    self.warnings = warnings
  }
}
