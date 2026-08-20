import Foundation

/// XDG-compatible locations used by symmeet on macOS.
public struct SymMeetPaths: Equatable, Sendable {
  public let configFile: URL
  public let modelsDirectory: URL
  public let workDirectory: URL
  public let dataDirectory: URL

  public init(environment: [String: String] = ProcessInfo.processInfo.environment, home: URL) {
    let configRoot = Self.root(
      environment["XDG_CONFIG_HOME"],
      fallback: home.appending(path: ".config", directoryHint: .isDirectory)
    )
    let cacheRoot = Self.root(
      environment["XDG_CACHE_HOME"],
      fallback: home.appending(path: ".cache", directoryHint: .isDirectory)
    )
    let dataRoot = Self.root(
      environment["XDG_DATA_HOME"],
      fallback: home.appending(path: ".local/share", directoryHint: .isDirectory)
    )

    configFile = configRoot.appending(path: "symmeet/config.toml", directoryHint: .notDirectory)
    modelsDirectory = cacheRoot.appending(path: "symmeet/models", directoryHint: .isDirectory)
    workDirectory = cacheRoot.appending(path: "symmeet/work", directoryHint: .isDirectory)
    dataDirectory = dataRoot.appending(path: "symmeet", directoryHint: .isDirectory)
  }

  public init(environment: [String: String] = ProcessInfo.processInfo.environment) {
    self.init(environment: environment, home: FileManager.default.homeDirectoryForCurrentUser)
  }

  private static func root(_ override: String?, fallback: URL) -> URL {
    guard let override, !override.isEmpty else { return fallback }
    return URL(fileURLWithPath: override, isDirectory: true)
  }
}
